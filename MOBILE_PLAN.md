# Mobile app — plan

Goal: a phone app with the same features as the matterbox TUI — channels,
DMs, threads, the unread feed, reactions, attachments, image preview,
keyword/semantic/agentic search, AI summaries, and the Jira / GitLab
panels — **driven by matterbox itself** rather than reimplemented against
Mattermost. The phone is a thin UI; the existing daemon (`internal/listen`)
grows into a personal headless server that holds the one WebSocket, owns the
SQLite cache, and runs the AI features. The app talks to *it*, not directly
to Mattermost.

Status: planning. Nothing built yet. This document is the roadmap; each
phase below leaves the tree green and is independently shippable.

Decisions (locked):

1. **Thin client + daemon API.** Extend `listen` into a headless server that
   exposes HTTP + WebSocket. The mobile app is a thin client. Maximum reuse
   of `internal/mm`, `internal/store`, `internal/aisearch`, `internal/embed`,
   `internal/chat`, `internal/jira`, `internal/gitlab` — no business logic is
   re-implemented on the phone.
2. **Transport: Tailscale / WireGuard.** The daemon stays private; the phone
   joins the same tailnet and reaches it at a stable MagicDNS name. No public
   exposure, no reverse proxy, no certificate management for v1.
3. **MVP first, then parity.** Ship a usable read/send/react/notify app early,
   then layer threads, search, AI, and the side panels.

---

## Background: what already exists

The codebase is unusually well-positioned for this. The lower layers are
already UI-free and the daemon is already a headless engine:

- **`internal/mm/client.go`** — a thin, context-aware wrapper over the
  official Mattermost SDK. 43 methods covering everything the app needs:
  `Send` (mm/client.go:179), `PostsAfter`/`PostsSince` (mm/client.go:98,120),
  `Thread` (mm/client.go:212), `AddReaction`/`RemoveReaction`
  (mm/client.go:272,286), `UploadFile`/`DownloadFile` (mm/client.go:243,232),
  `ViewChannel` (mm/client.go:152), `DialWS` (mm/client.go:44),
  `DoPostAction` for polls (mm/client.go:313). No TUI or storage deps.
- **`internal/store`** — SQLite cache with FTS5 keyword search
  (`Search`/`SearchSpec`), quantized-vector semantic search (`SearchHybrid`),
  edit history, and the daemon's own `meta` / `notif_targets` tables. Pure
  data layer, WAL-mode, safe for concurrent readers.
- **`internal/listen/engine.go`** — already holds **one** long-lived
  WebSocket (`Engine.Run`, engine.go:214), upserts every event into the store,
  reconnects with backoff, respects mutes/quiet-hours, and fires a
  notification callback. Its package doc literally says: *"Sharing one
  connection (TUI as a thin client of the daemon) is a future step."* The
  mobile server **is** that future step.
- **The Telegram bridge** (`internal/telegram`, `internal/listen/commands.go`)
  is a working proof that the daemon can be a two-way remote: it forwards
  mentions, accepts replies, forwards reactions, and runs `/ask` / `/search`
  / `/unread` / `/digest`. The mobile API generalizes that bridge from "one
  Telegram chat" to "any number of authenticated devices speaking JSON."
- **`internal/aisearch`** — the agentic search loop (`Ask`) is already pure
  business logic with no transport coupling; both the TUI and the Telegram
  `/ask` call it. The mobile API calls the same function.

So the work is **not** "rebuild matterbox for mobile." It is "add a network
seam in front of the layers that already exist, plus a phone UI."

---

## Target architecture

```
            ┌─────────────────────── home box / VPS (tailnet) ──────────────────────┐
            │                                                                         │
  Mattermost│   ┌──────────────┐   one WS    ┌───────────────────────────────────┐   │
  server ───┼──▶│ internal/mm  │◀───────────▶│         matterbox serve           │   │
            │   └──────────────┘             │  (internal/listen Engine, grown)  │   │
            │   ┌──────────────┐  upserts    │  ┌─────────────────────────────┐  │   │
            │   │ internal/    │◀────────────┤  │ internal/server (NEW)       │  │   │
            │   │ store (SQLite│  queries    │  │  REST  /api/*               │  │   │
            │   │  + FTS + vec)│────────────▶│  │  WS    /api/stream (events) │  │   │
            │   └──────────────┘             │  │  push  FCM / APNs           │  │   │
            │   aisearch / chat / embed /    │  └─────────────────────────────┘  │   │
            │   jira / gitlab (reused as-is) └───────────────▲───────────────────┘   │
            └────────────────────────────────────────────────┼───────────────────────┘
                                                              │ HTTPS + WSS over Tailscale
                                                ┌─────────────┴─────────────┐
                                                │   Mobile app (thin UI)     │
                                                │   React Native / Expo      │
                                                │   local SQLite mirror      │
                                                └────────────────────────────┘
```

Two new things; everything else is reuse:

1. **`internal/server`** (new Go package) — an HTTP + WebSocket facade over
   the existing layers, hosted inside the daemon process.
2. **`app/`** (new, separate from the Go module) — the phone client.

### Server: `matterbox serve` (grow `listen`)

Add the server alongside the existing daemon so one process holds the WS,
warms the cache, *and* serves the phone. Concretely:

- Add a `serve` subcommand (`internal/cli/serve.go`) — or a
  `listen.serve_addr` config key that makes `listen` also bind the API. Reuse
  `dial()` (cli/root.go:71) and `store.Open` exactly as `runListen` does
  (cli/listen.go:58).
- The `Engine` already produces the event stream the WS needs. Today its
  notification callback formats Telegram messages; refactor that fan-out into
  an interface so the Engine publishes **typed events** (`post.created`,
  `post.edited`, `post.deleted`, `reaction.added`, `typing`, `channel.viewed`,
  `presence`) to N subscribers. Two subscribers ship: the existing Telegram
  bridge and the new WS hub. This is the central refactor and it is small —
  the data already flows through `Engine.handle` (engine.go:286).
- REST handlers are thin adapters over `mm.Client` and `store`. They add no
  logic; they translate JSON ⇄ method calls.

#### REST surface (v1)

All under `/api`, all requiring a device token (see Auth). JSON in/out.

| Method & path | Backed by | Purpose |
|---|---|---|
| `GET /api/me` | `mm.Me` | current user |
| `GET /api/channels` | `store` + `mm.AllChannels`/`ChannelMembers` | sidebar: teams, channels, DMs, unread/mention counts, mute state |
| `GET /api/channels/{id}/posts?before=&after=&limit=` | `store.RecentForChannel` / `BeforeInChannel` / `AfterInChannel`, fallback `mm.PostsBefore`/`After` | paged history, cache-first |
| `GET /api/threads/{rootId}` | `store` + `mm.Thread` | full thread |
| `POST /api/channels/{id}/posts` | `mm.Send` | send / reply (`rootId`, `fileIds`) |
| `PATCH /api/posts/{id}` | `mm.EditPost` | edit own message |
| `DELETE /api/posts/{id}` | `mm.DeletePost` | delete own message |
| `GET /api/posts/{id}/revisions` | `store.Revisions` | edit history |
| `POST /api/posts/{id}/reactions` / `DELETE …/{emoji}` | `mm.AddReaction` / `RemoveReaction` | reactions |
| `POST /api/channels/{id}/view` | `mm.ViewChannel` | mark read |
| `POST /api/channels/{id}/mute` | `mm.SetChannelMuted` | mute/unmute |
| `GET /api/feed` | reuse `cmdUnread` logic (listen/commands.go) | unread feed |
| `POST /api/files` (multipart) | `mm.UploadFile` | upload, returns fileId |
| `GET /api/files/{id}` / `…/thumb` | `mm.DownloadFile` | attachment + image bytes (server transcodes/sizes) |
| `GET /api/search?q=&mode=keyword\|semantic&channel=&from=` | `store.Search` / `SearchHybrid` | keyword + semantic |
| `POST /api/ask` (streams via SSE/WS) | `aisearch.Ask` | agentic search with live trace |
| `POST /api/summary` (streams) | `chat.Complete` | channel / feed summary |
| `GET /api/users/{id}` , `GET /api/users?ids=` | `mm.UsersByIDs` / `UsernamesByIDs` | profile resolution |
| `GET /api/presence?ids=` | `mm.UsersStatuses` | presence dots |
| `PUT /api/me/status` , `PUT /api/me/custom-status` | `mm.UpdateStatus` / `UpdateCustomStatus` | own presence |
| `POST /api/autocomplete/users` | `mm.Autocomplete` | @-mention |
| `GET /api/emoji` , `GET /api/emoji/{name}/image` | `mm.AllCustomEmoji` / `CustomEmojiImage` | custom emoji |
| `POST /api/posts/{id}/action` | `mm.DoPostAction` | matterpoll vote/end/add |
| `GET /api/jira/{key}` , `PATCH /api/jira/{key}` | `internal/jira` | issue panel + inline edit |
| `GET /api/gitlab/mr?project=&iid=` , `POST …/approve` , `POST …/merge` | `internal/gitlab` | MR panel + actions |
| `POST /api/devices` , `DELETE /api/devices/{id}` | new | register/unregister push token |

#### Event stream: `GET /api/stream` (WebSocket)

The phone opens one WS to the daemon (not to Mattermost). The daemon
multiplexes the single upstream Mattermost WS to all connected devices. On
connect the client sends its `last_seen_ms`; the server replays missed events
from the store (the daemon already tracks this cursor via
`store.GetMeta("listen.last_seen_ms")`, store/listen.go) then streams live.
Event envelope: `{type, channelId, payload, ts}`. This removes per-device
Mattermost connections and battery-hungry direct sockets — the phone keeps one
cheap WS to a nearby tailnet peer.

### Auth & pairing

The daemon already holds the Mattermost session token (`internal/auth`,
shared via `dial()`); the phone never sees it. Instead:

- **Device tokens.** First launch shows a pairing screen. The user runs
  `matterbox serve --pair` on the host (or scans a QR the daemon prints),
  which mints a per-device bearer token stored in a new `devices` table
  (alongside `notif_targets` in `internal/store`). The phone stores it in the
  platform keychain (iOS Keychain / Android Keystore).
- Every REST/WS call carries `Authorization: Bearer <device-token>`.
- Tokens are revocable from the host (`matterbox serve --list-devices` /
  `--revoke <id>`), mirroring how `matterbox login --clear` works today.
- Because traffic rides Tailscale, transport encryption and network identity
  are handled by WireGuard; the device token is an app-level second factor and
  the revocation handle.

### Push notifications

Reuse the daemon's existing mention/DM detection verbatim — it already decides
*what* is notification-worthy (`isDirectMention`, events.go:117, plus
quiet-hours, mute, and the `NotifyDelaySeconds` read-check against
`mm.ChannelMember`). Only the *delivery* changes:

- Generalize the current single-sink notify path into the same publisher
  interface used for the WS hub. Sinks: Telegram (existing) **and** mobile
  push (new).
- Mobile push sink sends to **FCM** (Android) and **APNs** (iOS) using the
  tokens from `POST /api/devices`. Payload is a thin pointer
  (`channelId`, `postId`, summary text); the app fetches full content over the
  API when opened — same shape as the Telegram notification today, which
  already carries an optional LLM summary (`Engine.summarize`).
- A tiny push relay is needed because APNs/FCM require provider credentials.
  Default: route through a minimal first-party relay (or Expo's push service
  for the Expo path) so the self-hosted daemon needs no Apple/Google secrets.
  Document the trade-off; allow direct FCM/APNs keys for the privacy-maximal
  user.

### Mobile client: React Native + Expo (recommended)

Recommendation: **React Native via Expo**, because (a) one codebase for iOS +
Android, (b) `expo-notifications` gives FCM+APNs with the least credential
plumbing, (c) `expo-sqlite` gives a local mirror of the same data shapes, and
(d) TypeScript types can be generated from the Go API structs so the wire
contract stays honest. Flutter is a fine alternative with similar structure;
the API is client-agnostic, so this choice is reversible and does not affect
the server work.

App structure:
- **`app/api/`** — typed client for the REST + WS surface above.
- **`app/db/`** — local SQLite mirror (channels, recent posts) for instant
  cold-open and offline reading; hydrated from `/api/stream` replay.
- **`app/screens/`** — Channels/DMs list, Channel view, Thread view, Feed,
  Search, Compose, Settings/Pairing.
- **`app/components/`** — message bubble + markdown, reaction pills, attachment
  chips, image preview, mention autocomplete, presence dots.

---

## Feature parity matrix

Every TUI feature, where it lives now, and how the app gets it. "API" =
covered by a REST/WS endpoint above; "reuse" = the daemon already does the
heavy lifting; "client" = pure presentation in the app.

| Feature (TUI file) | Mobile delivery | Phase |
|---|---|---|
| Teams / channels / DMs sidebar (ui/view.go, model.go) | `GET /api/channels` + client list | 1 |
| Channel history, grouping, markdown (ui/messages.go, markdown.go) | `GET …/posts` + client render | 1 |
| Send / reply (ui/update.go) | `POST …/posts` | 1 |
| Reactions + picker (ui/reactions.go) | reaction endpoints + client picker | 1 |
| Mark-read + unread badges (ui/model.go, feed.go) | `…/view` + counts in `/api/channels` | 1 |
| Real-time updates (ui/update.go WS) | `/api/stream` | 1 |
| Push on mention/DM (listen/engine.go, events.go) | push sink (reuse detection) | 1 |
| Pairing / auth (internal/auth) | device-token flow | 1 |
| Threads panel (ui/messages.go) | `GET /api/threads/{root}` | 2 |
| Unread feed (ui/feed.go) | `GET /api/feed` | 2 |
| Attachments up/download (ui/attachments.go) | `/api/files` | 2 |
| Image preview + GIF (ui/preview.go) | `…/thumb` + client viewer | 2 |
| Edit / delete / history (ui/edit.go, history.go) | PATCH/DELETE/`/revisions` | 2 |
| @-mention autocomplete (ui/mention.go) | `/api/autocomplete/users` | 2 |
| Presence + custom status (ui/model.go) | `/api/presence`, status endpoints | 2 |
| Typing indicators (ui/typing.go) | `typing` WS events | 2 |
| Keyword search (ui/search.go, store FTS) | `/api/search?mode=keyword` | 3 |
| Semantic search (store SearchHybrid, embed) | `/api/search?mode=semantic` | 3 |
| Agentic search `?` (ui/aisearch.go, aisearch.Ask) | `POST /api/ask` (streamed trace) | 3 |
| AI summaries Ctrl+K (ui/summary.go, chat) | `POST /api/summary` (streamed) | 3 |
| Custom emoji images (ui/emojiimg.go) | `/api/emoji/*` | 3 |
| Giphy paste (ui/giphy.go) | client expands; posts as markdown | 3 |
| Polls / matterpoll (ui/polls.go) | `/api/posts/{id}/action` + client | 4 |
| Jira side panel + edit (ui/jira.go, jira_edit.go) | `/api/jira/*` | 4 |
| GitLab MR panel + approve/merge (ui/gitlab.go) | `/api/gitlab/*` | 4 |
| Group-DM creation (ui/commands.go) | reuse `mm.GroupChannel` via send addr | 4 |
| `> command palette` actions (ui/commands.go) | client command sheet over endpoints | 4 |

The TUI's terminal-only concerns (keybindings, Kitty graphics protocol,
powerline glyphs, resize handling) have **no** mobile analog and are dropped;
their *intent* (quick actions, image preview, reaction speed) is met with
native gestures.

---

## Phases

Each phase ends with the Go tree green (`go test ./...`) and an installable
app build.

### Phase 0 — Server scaffold & event seam
- New `internal/server` package: router, device-token middleware, `/api/me`,
  `/api/channels`, `/api/channels/{id}/posts`, `/api/channels/{id}/view`.
- New `serve` command (or `listen.serve_addr`) wiring `dial()` + `store` +
  `Engine` + the HTTP server in one process.
- Refactor `Engine`'s notify fan-out into a publisher/subscriber interface;
  keep the Telegram sink working unchanged (regression guard via existing
  `internal/listen` tests).
- `/api/stream` WS hub with store-replay on connect.
- Device-token pairing (`--pair`, `--list-devices`, `--revoke`) + `devices`
  table in `internal/store`.
- Tests: handler tests against an in-memory store; a fake `mm.Client`
  interface so handlers test without a live server.

### Phase 1 — MVP app (read · send · react · notify)
- Expo app skeleton, keychain token storage, pairing screen.
- Channels/DMs list, channel view with live updates, compose, reactions,
  mark-read.
- Push notifications end-to-end (register device → mention → FCM/APNs → deep
  link into channel). Reuses the daemon's existing mention logic.
- Local SQLite mirror for instant cold-open.
- **Outcome:** a daily-usable client for the 80% path.

### Phase 2 — Conversations parity
- Threads, unread feed, attachments (upload + image preview + GIF), edit /
  delete / history, @-mention autocomplete, presence + custom status, typing
  indicators.

### Phase 3 — Search & AI
- Keyword, semantic, and agentic (`/api/ask`) search with streamed trace;
  AI summaries (channel + feed); custom-emoji images; Giphy paste.
- Reuses `aisearch.Ask`, `store.SearchHybrid`, `chat.Complete`,
  `internal/embed` unchanged.

### Phase 4 — Integrations & polish
- Polls, Jira panel + inline edit, GitLab MR panel + approve/merge, group-DM
  creation, command sheet, settings, app-store packaging.

---

## Cross-cutting concerns

- **Offline / flaky links.** Cache-first reads from the local mirror; the API
  is already cache-first on the server side too (`store` before `mm`). Sends
  queue and retry. The `/api/stream` replay cursor reconciles after
  reconnect.
- **One source of truth.** All clients (TUI, Telegram, mobile) read the same
  store and ride the same single Mattermost WS the daemon owns — no
  duplicate connections, consistent unread state, and the daemon's
  `NotifyDelaySeconds` read-check already suppresses pings for things read on
  another device.
- **Security.** No public ports (Tailscale). Mattermost token never leaves
  the host. Per-device revocable tokens. Push payloads carry pointers +
  optional summary, not full message bodies, unless the user opts in.
- **Compatibility.** The server is purely additive; the TUI and `matterbox
  listen`/Telegram bridge keep working. Running `serve` alongside the TUI is
  safe — same WAL-mode store, idempotent upserts (as `listen` is today).
- **Testing.** Introduce an interface for the subset of `mm.Client` the
  handlers use so the server is testable with a fake; keep the existing
  `internal/listen` and `internal/store` tests as the regression net for the
  refactor.

## Open questions / risks

1. **Push relay credentials.** Expo's push service is the low-friction
   default; direct APNs/FCM keys are the privacy-max option. Pick per user;
   both supported.
2. **iOS background WS.** iOS suspends sockets in the background — push is the
   wakeup, the WS is foreground-only. Plan assumes this (push for delivery, WS
   for live foreground use).
3. **App distribution.** TestFlight + Play internal testing for a personal
   tool; full store listings only if it goes public. Apple developer account
   required for APNs and TestFlight.
4. **Multi-account.** TUI is single-account; the app inherits that for v1.
   The device→daemon mapping leaves room for multiple daemons later.
</content>
</invoke>
