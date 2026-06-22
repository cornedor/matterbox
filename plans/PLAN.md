# matterbox — plan & status

A minimal Mattermost TUI client targeting `https://chat.emico.io`.
Auth uses the session token saved by `mm_login.py` at
`~/.config/matterbox/mm_token.json` (sent as `Authorization: Bearer …`).

Stack: Go 1.26, Bubble Tea + Lipgloss + Bubbles/viewport, official
SDK `github.com/mattermost/mattermost/server/public/model` (Client4).

## Layout

```
matterbox/
├── main.go                       entry: one-liner around cli.Execute()
├── mm_login.py                   (pre-existing) GitLab SSO → save token
├── scripts/llama-embeddings.sh   manual-launch embeddings server (EmbeddingGemma)
└── internal/
    ├── auth/token.go             reads mm_token.json
    ├── config/config.go          ~/.config/matterbox/config.yaml loader
    ├── mm/client.go              Client4 wrapper (REST + websocket)
    ├── cli/                      cobra command tree
    │   ├── root.go               root cmd → runTUI (default); dial() shared
    │   ├── send/read/unread/digest/whoami/embed.go   scriptable verbs
    │   ├── timeargs.go           --since/--until parsing (yesterday, 7d, dates)
    │   ├── resolve.go            spec (team/channel, @user) → channel
    │   └── wait.go               --wait websocket await (awaitMessage)
    ├── store/                    local SQLite (+FTS5 +vectors), pure-Go modernc
    │   ├── store.go schema.go    open/migrate: posts + posts_fts + post_vectors
    │   ├── posts.go              upsert, recent/around, AuthoredBetween, keyword search (SearchSpec)
    │   ├── vectors.go            int8 embedding BLOBs + missing-vector queue
    │   └── vectorsearch.go       hybrid keyword+semantic RRF (SearchHybrid)
    ├── embed/                    embeddings HTTP client (OpenAI /v1/embeddings)
    │   ├── client.go             generic transport
    │   └── prompt.go             EmbeddingGemma query/document prefixes
    ├── semindex/indexer.go       background + on-demand embed orchestration
    └── ui/                       Bubble Tea v2 TUI (model/update/view + features:
                                  feed, search, aisearch, summary, polls,
                                  attachments, reactions, edit, switcher, …)
```

## Status

### Done — step 1: view-only browser

- [x] Go module + dep set (Bubble Tea, Lipgloss, Bubbles, Mattermost SDK).
- [x] Token loader from `~/.config/matterbox/mm_token.json`.
- [x] Client4 wrapper: `Me`, `Teams`, `AllChannels`, `Posts`, `UsersByIDs`.
- [x] 2-pane TUI: Channels (left) | Messages + disabled input box (right),
      with bottom team-tab strip.
- [x] Async fetch chain: Me → Teams + AllChannels (parallel) → Posts(first).
- [x] Focus cycling (`tab`/`shift+tab`) across Channels / Messages / Teams.
- [x] Per-pane navigation: ↑/↓ or j/k on lists, ←/→ on team tabs,
      viewport scrolling on the messages pane.
- [x] User-id → username resolution cached per session.
- [x] 401 handled gracefully with "re-run mm_login.py" hint.

### Done — step 1.5: DMs + filter

- [x] Switched to `GetChannelsForUserWithLastDeleteAt` — one fetch covers
      every team channel **plus** DMs and group-DMs.
- [x] DM channels: parse `Name` ("userA__userB"), resolve partner
      username, render as `@username`. Group-DMs render as `·displayName`.
      Private channels prefixed `🔒`, public channels prefixed `#`.
- [x] Synthetic **DMs** tab at the end of the team strip.
- [x] Switching team tabs is instant (no API round-trip — buckets cached).
- [x] Channel filter: `/` opens a filter input at the top of the channels
      pane, live substring match (case-insensitive), `enter` opens the
      highlighted result, `esc` clears.
- [x] `go vet` + `go build ./...` clean.

### Done — step 2: sending messages

- [x] Replaced the decorative input box with `bubbles/textarea`
      (`m.input`, prompt `> `, line-numbers off).
- [x] `mm.Client.Send` wraps `Client4.CreatePost`.
- [x] New `focusInput` focus state (4th in the tab cycle:
      channels → messages → input → teams). `enter` inside the input
      sends, `esc` returns focus to the messages pane.
- [x] Optimistic append on send (`appendOptimistic`); `postSentMsg`
      then refetches the channel so the canonical post replaces the
      stub. The WS-driven refetch may double this up — harmless.
- [x] Newline binding rebound to `alt+enter` / `ctrl+j` /
      `shift+enter` (whichever the terminal emits) so plain `enter`
      stays free for sending.
- [x] Read-only channels: relying on the API error for now (surfaces
      via `errMsg` → status line). No client-side pre-check yet.

### Done — step 3: live updates (currently-visible channel)

- [x] WebSocket client (`Client.DialWS` → `model.NewWebSocketClient4` +
      `Listen`). URL derived from `ServerURL` (`https` → `wss`).
- [x] Bubble Tea bridge: `connectWS` cmd dials and yields
      `wsConnectedMsg`; `waitWSEvent` recursively reads from
      `ws.EventChannel` and yields `wsEventMsg` (or `wsClosedMsg` on
      channel close). Re-scheduled after each event.
- [x] `posted` / `post_edited` / `post_deleted` events trigger a
      refetch of the focused channel's posts via the existing
      `fetchPosts` cmd.

### Done — step 3.5: live-updates polish

- [x] Unread + mention counters per channel (`m.unread`, `m.mentions`).
      Baseline seeded from the server via
      `GetChannelMembersWithTeamData` (`Channel.TotalMsgCount -
      ChannelMember.MsgCount` for unread; `ChannelMember.MentionCount`
      for mentions), then live-bumped by `posted` WS events.
      Rendered as ` N` (bold) or ` N!` (red bold) suffix on each
      channel row, with the label truncated to fit the badge.
- [x] `Client.ViewChannel` called after `postsLoadedMsg` and after any
      `posted` event lands in the focused channel, so server-side
      read state stays in sync (badges don't reappear on restart).
- [x] Optimistic local apply: `posted` / `post_edited` / `post_deleted`
      events now parse the embedded `post` from `data["post"]` and
      mutate `m.posts` in place. `sender_name` from the event data
      seeds `m.userNames` so the new row renders with the right name.
      Refetch only on parse failure (defensive).
- [x] Own-send dedup: when `applyPosted` sees a post from us matching
      a stub (empty `Id`, same `UserId`+`Message`), it drops the stub
      first so the canonical post replaces it cleanly.
- [x] WS reconnect with exponential backoff (`wsBackoff`: 1s, 2s, 4s,
      8s, 16s, 32s, capped). `wsClosedMsg` schedules a `tea.Tick` →
      `wsReconnectMsg` → `connectWS`; `wsConnectedMsg` resets the
      retry counter and clears any WS-related status line.

### Done — step 4: basic markdown

- [x] `internal/ui/markdown.go` renders inline `**bold**`, `*italic*`,
      `` `code` ``, `~~strike~~`, fenced ```` ``` ```` code blocks, and
      `>` block quotes (heavy vertical bar `┃` in dim grey, content
      pushed one column right and still inline-formatted).
      Code spans are stashed under a sentinel before the inline passes so
      their contents aren't reinterpreted; bold runs before italic so
      `**x**` isn't eaten. Underscore italics intentionally skipped to
      keep `snake_case_vars` literal.
- [x] `renderMessages` calls `renderMarkdown(p.Message)` in place of the
      previous raw line-split loop.

### Done — step 5: threads

- [x] `mm.Client.Thread` wraps `Client4.GetPostThread`; `Send` now
      accepts a `rootID` (empty = top-level, non-empty = reply).
- [x] Main feed marks replies with a dim `↳ ` prefix on the header
      and root posts with `↪ N` reply counter (from `Post.ReplyCount`).
- [x] New `focusThread` (cycle: channels → messages → thread → input
      → teams; skipped automatically when the sidebar is closed).
      Right-side body splits in half when open — messages left, thread
      sidebar right (min 24 cols each).
- [x] `enter` on a selected message opens the thread sidebar. If the
      message is itself a reply, its `RootId` is used as the thread
      root; otherwise the post is treated as the root. `esc` (from
      messages or thread focus) closes the sidebar.
- [x] `fetchThread` loads root + replies, sorts chronologically,
      resolves unseen senders' usernames. Loading state shows
      "Thread (loading…)" then the reply count.
- [x] Input box stays anchored under the messages pane but its prompt
      flips to `↳ ` while a thread is open; pressing enter sends with
      `RootId = threadRootID` to the thread's channel (even if the
      sidebar's selected channel differs). Mention autocomplete is
      re-scoped to the thread's channel/team for the same reason.
- [x] WS events keep the sidebar live: `posted` / `post_edited` /
      `post_deleted` for posts whose `Id == threadRootID` or
      `RootId == threadRootID` apply to `m.threadPosts` (with
      own-send stub dedup). Deletion of the root closes the sidebar.

### Done — step 6: channel switcher (`ctrl+k`)

- [x] `ctrl+k` opens a modal switcher overlay from anywhere (channels,
      messages, thread, input, filter) — intercepted at the top of
      `handleKey` before the textarea can bind it to delete-to-end.
- [x] `internal/ui/switcher.go`: `switcherResults` walks every bucket
      (teams + DMs + group-DMs), fuzzy-scores against the
      lower-cased channel label. Substring hits (earliest position +
      shortest haystack) always rank above subsequence fallbacks.
      Limit of 12 rows.
- [x] `renderSwitcher` draws a centered popup with title, textinput,
      and result rows. Each row shows the channel label, unread/mention
      badges (same vocab as the sidebar), and a dim team-name suffix
      ("DM" for direct/group, team display name otherwise) to
      disambiguate same-named channels across teams.
- [x] `up/ctrl+p` and `down/ctrl+n` move the cursor; `enter` switches
      via `switchToChannelHomeTeam`, clears any active channel filter,
      and kicks `fetchPosts`; `esc` / `ctrl+k` close the popup. Query
      changes reset the cursor to the first result.
- [x] Sort tiers (lower = higher priority): score → mention/unread
      attention → persisted open count → last-opened timestamp →
      alphabetical. With an empty query every score ties at 0 so the
      attention + frecency signals fall through naturally, surfacing
      unreads first and frequent channels next.

### Done — step 6.5: persisted channel usage

- [x] `internal/ui/stats.go` persists per-channel open counts +
      last-opened timestamps to
      `~/.config/matterbox/channel_stats.json` (atomic write via
      tmp + rename in the same dir). Missing / unreadable file is
      silently treated as "no stats yet" — failure of a usage signal
      shouldn't surface as a startup error.
- [x] `bumpChannelStat` increments in-memory and returns a `tea.Cmd`
      that persists a snapshot off the UI goroutine, so the disk write
      can't stall a keystroke.
- [x] Bumped on explicit selections only: enter on the channels list,
      enter on the filter (`/` mode), and enter on the `ctrl+k`
      switcher. Auto-loads (team switch, initial post fetch,
      jump-to-unread) deliberately don't count — those don't reflect
      "I want this channel" intent.
- [x] Switcher sort uses the open count as the next signal after
      attention tier; recency (last_opened) breaks count ties.

### Done — step 7: multi-line input + shift+enter (Bubble Tea v2)

- [x] Migrated off `github.com/charmbracelet/{bubbletea,bubbles,lipgloss}`
      and onto `charm.land/{bubbletea,bubbles,lipgloss}/v2` (`v2.0.6 /
      v2.1.0 / v2.0.3`). Same project (Charm), new module path. Driven
      by needing native Kitty keyboard protocol support that v1 didn't
      have.
- [x] API surface migrated:
      - `View() string` → `View() tea.View`, with `tea.NewView(s)` to
        wrap content. AltScreen is now a per-frame flag on the view
        (`v.AltScreen = true`) — `tea.WithAltScreen()` is gone.
      - `tea.KeyMsg` → `tea.KeyPressMsg` (v2 also has `KeyReleaseMsg`,
        not used here). `key.Matches` is now generic on the Stringer
        message type so the existing matching code carried over.
      - viewport / textinput / textarea fields `Width`, `Height`,
        `YOffset` became methods (`Width()`, `SetWidth(n)`, …). All
        call sites updated.
      - `viewport.New(w, h)` is now `viewport.New(opts ...)`; we keep
        the no-arg form and let `resizeMessagesViewport` set sizes
        afterwards.
      - `help.Width = x` → `help.SetWidth(x)`.
      - **lipgloss v2 changed `Style.Width(n)` semantics**: it now
        sets the OUTER box including border + padding, whereas v1's
        `Width(n)` set the content area. Every full-bordered pane
        (channels / messages / thread / switcher / mention popup)
        was sized via `Width(width - 2)` in v1 to compensate for
        v1's "content width" interpretation; under v2 that produces
        an outer box 2 cells too narrow, wrapping inner content into
        a stray "──" row. Fixed by passing the intended outer width
        directly (`Width(width)`).
      - textarea has a built-in `DynamicHeight` flag in v2 that grows
        the widget between `MinHeight` and `MaxHeight` as content
        changes; enabled it and kept the existing `syncInputHeight`
        helper as a thin wrapper that re-flows the messages pane
        when the input grows/shrinks.
      - textarea defaults render the prompt on every visual line and
        underline the cursor's row; replaced the static `Prompt`
        with a `SetPromptFunc` that returns "> "/"↳ " on line 0
        and two spaces on continuation lines, and cleared
        `Styles.Focused.CursorLine` / `Styles.Blurred.CursorLine` to
        kill the underline artifact.
- [x] Input textarea grows with content. `syncInputHeight`
      (`internal/ui/view.go`) sets the textarea height to
      `clamp(LineCount(), 1, maxInputHeight=6)` and reflows the
      messages viewport so the split stays consistent. Called after
      every keystroke that touches the input, after `Reset()` on send,
      and after `SetValue` from the mention-accept path.
- [x] `resizeMessagesViewport` subtracts `m.input.Height()` instead of
      assuming 1 — the messages pane shrinks as the input grows, then
      expands back after send.
- [x] shift+enter works. The Kitty keyboard protocol is enabled
      per-frame via `v.KeyboardEnhancements.ReportAllKeysAsEscapeCodes
      = true` (default disambiguate flag would leave Enter+Shift on
      the legacy `\r` path). v2's input parser reconstructs ordinary
      key events from the resulting CSI u sequences so the rest of the
      UI is unaffected. `alt+enter` / `ctrl+j` still listed as
      fallback bindings.

### Done — step 8: dates, config, reactions

- [x] Header timestamps include a short date once a post is older than
      24h (`Jan 2 15:04`), or `Jan 2 2006 15:04` across years. Same
      helper feeds the search result lines so they read the same way.
- [x] `~/.config/matterbox/config.yaml` loaded at startup
      (`internal/config/config.go`). Fields: `server_url` and
      `reactions` (emoji shortcodes for the picker). File is created
      with documented defaults on first run.
- [x] `mm.Client` takes the server URL as a constructor arg; the WS
      URL is derived per-call (no more hard-coded `chat.emico.io`).
- [x] Reactions render below each post body — grouped by emoji with
      count, self-reactions tinted bright blue.
- [x] `R` on a selected post (messages or thread pane) opens a modal
      picker listing the configured emojis with digit accelerators
      (`1`-`9`), arrow + enter navigation, and a `✓` marker on
      reactions the user already made. Selecting toggles add/remove.
- [x] WS `reaction_added` / `reaction_removed` events parse
      `data["reaction"]` and mutate the local post in place; the
      updated post is re-persisted so cached reopens render the same
      reaction set.

### Done — step 9: combined unread feed + team breadcrumbs

- [x] New synthetic **Feed** tab (between Unread and Search; green in the
      tab strip with the same `N` / `N!` badge as Unread). Full-width
      page like Search, rendering one bordered bubble per unread channel
      across every team + DMs — a passive "keep it open" monitor.
- [x] Each bubble: channel **breadcrumb** + unread/mention count on the
      top border, up to 2 already-read context messages (dim), a `─ new`
      divider, then the unread messages. Busy channels collapse the
      overflow into a `↑ +N earlier unread` row (`feedUnreadCap`). The
      bubble box is the shared `bubbleBox` helper extracted from the
      search tab so Search and Feed stay visually identical.
- [x] Data: `ChannelMember.LastViewedAt` is the read boundary;
      `mm.Client.PostsSince` (`GetPostsSince`) pulls the unread posts per
      channel, `store.BeforeInChannel` supplies the cached context, and
      fetched posts are persisted to grow the corpus. Sorted mentions
      first, then most-recent activity. System / deleted posts filtered.
- [x] Monitor-only read semantics: scrolling never marks read. `enter`
      opens the channel (marks read as usual) and jumps to the first
      unread; `m` marks the channel read in place and drops the bubble;
      `r` refreshes. Built lazily on first landing / focus, and kept live
      by folding background `posted` / `post_deleted` WS events into the
      entries (`feedAppendPosted` / `feedRemovePost`).
- [x] Opened via `ctrl+shift+u` from anywhere, or by selecting the tab.
- [x] **Search bubbles now show the team as a breadcrumb too**
      (`channelBreadcrumb`: `Engineering › #general`, `DMs › @alice`),
      replacing the bare channel label on the bubble header.

### Done — step 10: local message store (SQLite + FTS5)

- [x] `internal/store`: persistent per-channel cache at
      `~/.config/matterbox/messages.db` (pure-Go `modernc.org/sqlite`, no
      CGO). `posts` table + `posts_fts` FTS5 virtual table kept in sync by
      triggers (`schema.go`). Best-effort: if `store.Open` fails the app
      falls back to fresh fetches — every store access guards on `m.store`.
- [x] On channel reopen, paint from the store instantly, then gap-fill via
      `GetPostsAfter(latestStoredId)`. Posts from **unfocused** channels are
      captured from WS events too (`applyPosted` writes regardless of focus),
      so the corpus grows continuously. Store writes always run as `tea.Cmd`s
      off the UI goroutine (`internal/ui/postcache.go`).
- [x] Edits/deletes/file-infos persist through the same path; revisions kept
      (`Revisions`). Render window stays cache-backed (see memory:
      render-window — O(loaded), not O(history)).

### Done — step 11: keyword search tab

- [x] **Search** synthetic tab: live FTS5 over the local store, debounced
      (`searchDebounce`), results as clickable hit bubbles with a context
      window around each match and a team breadcrumb header.
- [x] `store.SearchSpec` (posts.go) compiles `AllOf`/`AnyOf`/`Phrases`/
      `NoneOf` into one FTS5 expr plus `ChannelIDs`/`AuthorIDs`/`After`/
      `Before` SQL filters; returns a capped total + supports `offset`
      paging. Plain typed search ranks strict create_at DESC; the agent
      path ranks by RRF of bm25 + recency (`rankByRelevanceAndRecency`).

### Done — step 12: polls, attachments, edit/delete

- [x] **Matterpoll** rendering + voting: parses `custom_matterpoll` posts and
      their action buttons, votes via `Client.DoPostAction`, add-option/end via
      `SubmitDialog` (`internal/ui/polls.go`).
- [x] **Attachments**: file attachments render under a post; download to disk
      (`DownloadFile`) and attach-on-send (`UploadFile`), capped at 5/post to
      mirror the web client (`internal/ui/attachments.go`).
- [x] **Edit / delete** own posts: `e` opens the post in the input
      (`EditPost`), delete via `DeletePost`; WS `post_edited`/`post_deleted`
      reconcile (`internal/ui/edit.go`).

### Done — step 13: AI summary command

- [x] On-demand LLM summary of a channel/thread streamed into a panel
      (`internal/ui/summary.go`), rendered through glamour. Talks to a local
      OpenAI-compatible endpoint set by the `summary:` config block
      (`endpoint`/`model`/`prompt`). See memory: local-llm-tool-calling.

### Done — step 14: agentic AI search

- [x] A Search-tab query ending in `?` is handed to the local LLM, which
      drives tools (`search_messages`, `list_channels`, `read_around`,
      `finish`) in a bounded loop to find messages instead of plain FTS
      (`internal/ui/aisearch.go`). One expressive `search_messages` primitive
      exposes any_of/all_of/phrase/none_of/channel/team/author/after/before +
      offset paging (see memory: ai-search-tool-redesign). Collected hits feed
      the same clickable bubbles; the prose answer renders as a selectable
      banner with an in-box follow-up input that continues the conversation.
- [x] Reuses the summary endpoint/model; only `ai_search.prompt` +
      `ai_search.max_steps` are separate config. Live trace while running.

### Done — step 15: semantic search (embeddings + hybrid RRF)

- [x] Int8-quantized, L2-normalized embeddings stored as BLOBs in
      `post_vectors` (`store/vectors.go`); brute-force cosine in pure Go
      (sqlite-vec is out — modernc can't load C extensions). EmbeddingGemma-300M
      (QAT) on a second llama-server (:8322, `scripts/llama-embeddings.sh`,
      manual launch). See memory: semantic-search for the full design.
- [x] `internal/embed` client (+ query/document prefixes), `internal/semindex`
      orchestrates embed+store with chunking for the 2048-token limit. TUI
      background indexer (`embedindex.go`, gated by `embeddings.auto_index`) +
      on-demand `matterbox embed` backfill, both SIGINT-resumable.
- [x] `store.SearchHybrid` (vectorsearch.go) fuses bm25 + cosine via RRF
      (no age decay — it wrecks rank-fused relevance). UI: a leading `~` on the
      Search tab = semantic mode; the AI `search_messages` tool takes a
      `mode` (keyword|semantic|hybrid) knob.

### Done — step 16: headless CLI (cobra)

- [x] `internal/cli`: cobra root whose default action launches the TUI (bare
      `matterbox` unchanged), plus scriptable verbs `send`, `read`, `unread`,
      `embed`. `--pprof` is a root flag. Channels addressed `team/channel` or
      `@user`, resolved by URL slug (`resolve.go`). stdout = data, stderr =
      diagnostics, so commands compose in pipelines.
- [x] `read --wait` / `unread --wait` (`+--timeout`): print history/unread,
      then block on the websocket until a genuinely new message arrives, print
      it, and exit (socket dialled before the fetch; `create_at > since`
      dedupes). Wait machinery in `wait.go`. See memory: cli-cobra.

### Done — step 17: CLI feature expansion

Cross-cutting conventions: keep stdout=data / stderr=diagnostics; reuse
`resolveChannel` + the `labeler` for addresses; relative time args
(`yesterday`, `7d`, `2026-06-08`) parsed by one shared helper. Order roughly
by how often each was actually missed.

- [x] **`matterbox digest` (alias `activity`)** — *highest value.* Lists my own
      messages across all channels for a time range — "what did I work on":
      `matterbox digest --since yesterday`. Sourced from the local store via a
      new `store.AuthoredBetween` (author + `create_at` range, no MATCH term —
      `SearchSpec`/`searchFTS` bail on an empty FTS expr). Grouped by channel
      (most-recently-active first) and stamped with dates since a digest can
      span days. `--since` defaults to the start of today; metadata for the
      channel/DM labels is best-effort (degrades to raw ids if the API hiccups).
- [x] **Date filtering on `read`** — `--since` / `--until` (shared
      `timeargs.go` parser: `now`/`today`/`yesterday`/`7d`/`2h`/`2006-01-02`,
      `--until` exclusive). `--since` maps to `PostsSince` and shows the whole
      window by default (no silent `--limit` truncation across day boundaries);
      `--until` is a client-side `create_at` filter. A single day is
      `--since 2026-06-08 --until 2026-06-09`. *Not yet:* `--on DATE` sugar (it
      would just expand to that since/until pair) and back-paging for an
      `--until`-only window (today it only filters the recent page).
- [x] **`matterbox whoami`** — prints my username / user_id / email (wraps
      `Client.Me`), aligned single-token values so a field is `awk`-able.
- [x] **`matterbox search`** — scriptable counterpart to the Search tab over
      the same store (`search.go`): `matterbox search "release plan" --channel
      eng/general --since 7d`. Keyword FTS via `SearchSpec` (`AllOf` = the query
      words, prefix-matched); `--semantic` embeds the query (`embed.QueryText` +
      `EmbedOne`) and blends it with the keyword ranking via `SearchHybrid`
      (needs the embeddings server up). `--channel`/`--from`/`--since`/`--until`/
      `--limit`/`--offset`/`--context`, `--json`. Hits print under their channel
      breadcrumb; the shown/total + paging hint go to stderr.
- [x] **`read --from @user`** — filter a channel read to one author (resolve via
      `UserByUsername`, filtered before the `--limit` tail). Pairs with the date
      filters for "what did X say this week". The websocket predicate gained a
      symmetric `authorID` scope, so `read --from @x --wait` blocks for x's next
      message specifically.
- [x] **`--json` / `-o json`** on `read` / `unread` / `search` / `digest` —
      JSON Lines (one object per message) for `jq`. Shared helper (`output.go`)
      resolves usernames up front (DM channels otherwise surface as raw
      `userid__userid`) and stamps each line with the channel address + id;
      `--json` is shorthand for `-o json`, and an unknown `-o` value errors. The
      `--wait` live message emits JSON too.
- [x] **`matterbox channels`** — list teams/channels and resolve id↔name
      (`channels.go`). Built from `Teams` + `AllChannels`, DM partners resolved
      for `@user` addresses, team metadata best-effort. Text groups under a
      `# team` header (id, type, address, display name; id-first for `awk`);
      `--json` emits one channel per line. Deterministic order (teams
      alphabetical, DMs last).
- [x] **`read --thread <root_id>`** — print a full thread (root + every reply,
      chronological) via `Client.Thread` (`GetPostThread`) instead of the flat
      tail. The channel arg becomes optional in thread mode; honours `--json`
      (the reply structure shows in each line's `root_id`).

### Backlog (no order)

- [ ] Unread/mention indicators on **team tabs** (channel-row badges
      done in step 3.5).
- [ ] Token refresh flow inside the TUI (today: drop to shell, re-run
      `mm_login.py`).

## How to run

```sh
cd /home/corne/Development/matterbox
go run .                          # or: ./matterbox after a build — opens the TUI

# scriptable subcommands (stdout = data, stderr = diagnostics):
matterbox send eng/general "hi"
matterbox read eng/general --limit 50
matterbox read eng/general --since yesterday          # date-bounded read
matterbox read eng/general --since 2026-06-08 --until 2026-06-09
matterbox read eng/general --from @alice --since 7d   # one author's week
matterbox read --thread <root_id>                     # whole thread
matterbox read @alice --wait --timeout 5m
matterbox read eng/general --json | jq .              # JSON Lines for jq
matterbox unread
matterbox search "release plan" --channel eng/general --since 7d
matterbox search "how do we deploy" --semantic        # hybrid (needs embeddings)
matterbox channels --json                             # id <-> name map
matterbox digest --since yesterday                    # my own posts, all channels
matterbox whoami                                      # my id / username / email
matterbox embed                   # backfill semantic-search vectors
```

Keys: `tab` / `shift+tab` cycle focus; `↑↓` or `j/k` navigate;
`←/→` switch teams; `enter` opens; `/` filter, `ctrl+k` switcher,
`ctrl+shift+u` feed; `q` quits. Search tab: `~` = semantic, trailing
`?` = agentic AI search.

## Verification checklist (step 1)

1. `go build ./...` — clean.
2. `go run .` — TUI launches, populates teams/channels/messages.
3. Cross-check one channel's posts via:
   `curl -H "Authorization: Bearer $(jq -r .token ~/.config/matterbox/mm_token.json)" \
        https://chat.emico.io/api/v4/channels/<id>/posts`
4. Invalidate the token (edit `mm_token.json`) → footer shows the
   "auth failed — re-run `python mm_login.py`" hint, no crash.
