# Configuration — `config.yaml`

Everything matterbox lets you change lives in one YAML file. It is written for
you on first run with every key at its default and a comment block explaining
each one, so the file itself is the quickest reference — this document is the
long version: what each key does, what it defaults to, when a change takes
effect, and which keys interact.

Nothing here is required. A config with a single `server_url` line is a complete
config; the rest exists so you can change your mind about a default.

## Where it lives

| | Path |
|---|---|
| Linux | `~/.config/matterbox/config.yaml` (honours `XDG_CONFIG_HOME`) |
| macOS | `~/Library/Application Support/matterbox/config.yaml` |
| Anywhere | `$MATTERBOX_CONFIG_DIR/config.yaml` |

`MATTERBOX_CONFIG_DIR` names the directory *verbatim* — nothing is appended —
and relocates everything matterbox keeps on disk, not just the config. That
makes it the way to run a second profile against another server:

```sh
MATTERBOX_CONFIG_DIR=~/.config/matterbox-work matterbox
```

The rest of the directory:

| File | What it is |
|---|---|
| `config.yaml` | this file — mode `0600`, it holds tokens |
| `config.schema.json` | JSON Schema for editor autocomplete, rewritten to match the running build |
| `mm_token.json` | the Mattermost session token (`matterbox login` writes it) |
| `messages.db` | the local message cache + FTS5 index + embeddings — see [the message database](database.md) |
| `templates.json` | composer templates (`/tmpl`) |
| `channel_stats.json`, `picker_stats.json` | usage counts that order the channel switcher and pickers |
| `tui.sock` | control socket a running TUI listens on (`matterbox open`, the daemon's "are you reading this?" check) |

Your Mattermost credentials are **not** in `config.yaml` — the session token
lives in `mm_token.json`. The config does hold the Jira, GitLab and Telegram
secrets, which is why it is owner-only; matterbox tightens the permissions
every time it reads the file, not only when it rewrites it.

## First run, and when matterbox rewrites the file

- **No file?** Defaults are written to disk, so you can discover what is
  configurable by opening it.
- **Missing a section?** When a build adds settings, the first load fills them in
  and rewrites the file, so new keys show up as editable defaults rather than
  staying invisible.
- **A syntax error is fatal.** matterbox reports the YAML error and exits instead
  of silently falling back to defaults — a typo should not quietly change how
  your client behaves.
- **An unknown key is ignored.** The loader skips keys it doesn't recognise, so a
  misspelled setting fails *silently* at runtime. Your editor is what catches
  those (see below) — the schema rejects unknown keys.

A rewrite keeps your values but regenerates the file from scratch: the header
comments come back, keys are re-ordered canonically, and **any comments you
added are lost**. Four things trigger one:

1. the file did not exist;
2. a section or key from a newer build was missing;
3. `matterbox welcome` saved your answers;
4. you reordered the team tabs in the TUI with `<` / `>` (which persists
   `team_order`).

So treat the file as data rather than a place to keep notes, and if you want
commentary that survives, keep it in a copy under version control.

## Editor autocomplete

matterbox drops a JSON Schema next to the config and puts a modeline at the top
of the file:

```yaml
# yaml-language-server: $schema=config.schema.json
```

Any editor running the YAML language server (VS Code's YAML extension, Neovim's
`yamlls`, Helix, …) then gives you key completion, hover docs, enum values, and
a warning on unknown or mistyped keys — with no per-editor setup, because the
schema path is relative to the config itself. The file is refreshed on every
load, so it always describes the build you are running.

## What takes effect when

- **The TUI** reads the config once, at startup. Restart it after an edit.
- **`matterbox listen`** re-reads the config on `SIGHUP`, but only swaps the
  `rules:` block in place (`systemctl --user reload matterbox-listen`). Changing
  any other `listen:`/`telegram:` option needs a restart of the daemon.
- **CLI subcommands** read the config per invocation, so they pick up an edit
  immediately.

Values that name a fixed set of choices (`nav_modifier`, `vim_nav`,
`emoji_images`, `image_thumbnails`, `file_previews`, `image_click`, `code_theme`) **fall back to their default**
when you write something unrecognised — a typo costs you the setting, not the
app. Keybinding overrides are the exception: an unknown action
id, an unparseable chord, or a binding that collides with another action is a
hard startup error, because a silently shadowed key is worse than a failure to
launch.

## Minimal config

```yaml
server_url: https://mattermost.example.com
```

Then `matterbox login`. Everything below is optional.

## Reference

### The server

| Key | Default | What it does |
|---|---|---|
| `server_url` | `https://mattermost.example.com` | Base URL of your Mattermost instance. |

The default is a **placeholder**: commands that need a real server (`login`)
treat a `server_url` still equal to it as "not configured yet" and stop, naming
the file to edit and the command to re-run. A `matterbox` with no saved login
runs the setup wizard instead, which is where the value usually comes from.

### Reading and display

| Key | Default | What it does |
|---|---|---|
| `reactions` | `[+1, -1, heart, tada, eyes, rocket, laughing, thinking_face]` | Quick list in the reaction picker (`R` on a message), in the order you write it — listed after any emoji already placed on that message. The picker's search box reaches every server emoji regardless, so an empty list is not a dead end. |
| `team_order` | *(empty)* | Left-to-right order of the team tabs, by team URL name (case-insensitive; a display name is accepted too). Teams you don't list are appended alphabetically. Reordering with `<` / `>` in the TUI writes this key. |
| `mark_read_delay_seconds` | `5` | How long a channel must stay open before it is marked read on the server. A shorter peek leaves it unread. `0` marks read the moment you open it. |
| `group_message_seconds` | `120` | Consecutive messages from the same person, sent within this many seconds and with nobody else posting in between, render as bare continuation lines under one name+time header. `0` gives every message its own header. |
| `collapse_long_messages` | `12` | Fold a message whose body wraps to more than this many rows down to a preview plus a `… N more lines` footer, so a log dump doesn't bury the conversation. `z` expands/re-folds the selected message. `0` disables folding. |
| `collapse_preview_lines` | two-thirds of `collapse_long_messages` | How many leading rows a folded message keeps. Clamped to `1 … collapse_long_messages`; ignored when folding is off. |
| `custom_status` | `true` | Show DM partners' custom statuses (`🌴 On vacation`) in the header and as a sidebar hint glyph. `false` leaves only the presence dots. |
| `date_separators` | `true` | Draw a labelled rule (`Today`, `Yesterday`, or a date) above the first message of each local calendar day. |
| `feed_show_muted` | `false` | Let muted channels into the unread Feed and its tab badge. This is only the *startup* state — `M` on the Feed tab toggles it for the session. |
| `mouse` | `true` | Mouse support: wheel-scroll the transcript/thread/result lists, click a tab, channel or message to select it, drag to select text, hover to highlight. Set `false` to keep your terminal's native click-drag selection, which capturing the mouse otherwise replaces (most terminals fall back to shift-drag). |
| `sql_tab` | `false` | Add the read-only SQL tab — a query editor over the local message cache whose result rows render as chat messages. See [the message database](database.md). |
| `kaomoji_options` | *(empty)* | Extra entries for the `/kaomoji` picker, listed after the built-in set. |
| `code_theme` | `monokai` | Chroma style used to highlight fenced code blocks: `dracula`, `github-dark`, `gruvbox`, `nord`, `onedark`, `catppuccin-mocha`, `tokyonight-night`, … plus the bundled `everforest-dark`. An unknown name falls back to the default; `NO_COLOR` disables code colour entirely. |

### Attachments, images and motion

| Key | Default | What it does |
|---|---|---|
| `download_dir` | `~/Downloads` | Where `s` on a message saves attachments. A leading `~` is expanded; the directory is created on first download. |
| `attach_on_drop` | `true` | Attach a file dragged onto the terminal. Terminals have no drag-and-drop protocol — the emulator delivers a drop by *pasting the path* — so this is a heuristic: a paste that is nothing but existing absolute file paths becomes an attachment. `false` pastes such paths as text. |
| `emoji_images` | `auto` | `auto` renders custom (server) emoji as real inline images on a Kitty/Ghostty-class truecolor terminal outside tmux; `off` keeps literal `:name:` text everywhere. Unicode emoji are unaffected — they are always font glyphs. |
| `image_thumbnails` | `off` | `auto` draws image attachments as inline thumbnails in the transcript, wherever `emoji_images` works (same terminal gate). `off` shows only the 🖼️ filename line. Space opens the full-size preview either way. |
| `file_previews` | `auto` | `auto` previews the attachments that are text rather than pixels: the first 10 lines of a log, diff, JSON or source file, syntax-highlighted, and a CSV/TSV drawn as a box table, above the file's own 📄 chip. `off` leaves them as plain chips. Needs no terminal graphics. Only files under 2MB are fetched, and only when they scroll near the viewport; `z` collapses them along with any thumbnails. |
| `image_click` | `preview` | What a mouse click on a rendered inline thumbnail does (`image_thumbnails: auto`). `preview` opens the in-app full-size preview (same as space); `open` hands it to the OS/browser (same as `o`); `download` saves it to `download_dir` (same as `s` for attachments); `off` leaves the click as a plain message select. Only the thumbnail cells themselves are clickable — not the filename chip or the rest of the message. Also settable live via `>` → **Image click on thumbnail**. |

`animations:` groups the motion toggles, so movement you find distracting can go
away one piece at a time:

| Key | Default | What it does |
|---|---|---|
| `animations.custom_emoji` | `true` | Animate GIF custom emoji in place. No effect unless `emoji_images` renders them as images at all. |
| `animations.image_preview` | `true` | Animate GIFs in the space-to-preview modal. |
| `animations.inline_images` | `true` | Animate GIF thumbnails in the transcript — only while they are on screen, so a channel full of GIFs costs nothing once scrolled away. |
| `animations.native_animation` | `false` | **Experimental.** Play all of the above through the Kitty graphics protocol's native animation frames: every frame is uploaded once and the terminal times and loops it, instead of matterbox re-transmitting on a timer. In a binary built with the `video` tag it also unlocks video (mp4/webm/mov/animated-webp/animated-avif) — looping inline previews, and space streams the whole clip. A still WebP, BMP or TIFF needs none of this and always renders; HEIC, AVIF and JPEG XL need only the `video` tag, not this flag (AVIF additionally needs an ffmpeg built with libdav1d and JXL one built with libjxl — the release binaries have both, and `matterbox --version` says what yours has). An animated AVIF or JXL plays under this flag exactly as an animated WebP does: all three loop, since they are images standing in for a GIF, while a real clip streams once and stops on its last frame. Opt-in because it needs animation-frame support beyond what most Kitty-class terminals implement, and a terminal that only does the basics may show a frozen or blank image rather than falling back. |

`native_gif_protocol` is the former name of `native_animation` and is still read,
so an old config keeps working; the next rewrite drops it in favour of the new
key.

### `keybindings`

```yaml
keybindings:
  nav_modifier: ctrl
  vim_nav: global
  bindings:
    compose: [i, a]
    delete_post: shift+d
    quit: []
```

| Key | Default | What it does |
|---|---|---|
| `nav_modifier` | `ctrl` | Modifier for arrow-key team (`←`/`→`) and channel (`↑`/`↓`) navigation: `ctrl`, `alt`, `shift`, `super` (⌘ / Windows key; `cmd` also accepted), `meta`, `hyper`, or `none` to turn arrow-nav off and free `ctrl+←/→` for the composer's word-jump. |
| `vim_nav` | `global` | When `ctrl+h/j/k/l` switch team/channel: `global` from any focus, even while typing; `reading` only outside text inputs, so `ctrl+h` / `ctrl+k` stay as the composer's emacs editing keys; `off` never. Arrow-nav is unaffected. |
| `bindings` | *(empty)* | Per-action overrides: an action id mapped to one key or a list of keys. |

On macOS `ctrl`+arrows collide with Mission Control — `shift` is the most
broadly compatible alternative, and `super` (⌘) works on terminals that speak
the Kitty keyboard protocol (Ghostty, kitty, WezTerm) but not on Terminal.app or
iTerm2. Some chords only arrive at all on a Kitty-protocol terminal:
`shift+enter`, for instance, *sends* on a legacy terminal instead of inserting a
newline — use `alt+enter` there.

**`bindings` rules of the road.** A value is a single chord (`shift+d`) or a list
(`[i, a]`); an empty list or the string `none` unbinds the action. Modifiers are
`ctrl`, `alt`, `shift`, `super`, `meta`, `hyper`. Rebinding a navigation action
drops its modifier-arrow alias too. `ctrl+c` always quits, no matter what you do
to `quit`. An unknown action id, an unparseable chord, or an override that makes
two actions collide in layers active at the same time is reported at startup,
with the full list of valid ids.

`matterbox keys` prints every action, its default keys, your effective keys, and
marks the ones you overrode — the authoritative list. In the TUI, `?` expands
the footer into every key active right now and `f1 › Keys` opens the full
scrollable cheatsheet. The action ids, by layer:

- **Focus / motion** — `focus_next`, `focus_prev`, `up`, `down`, `left`,
  `right`, `top`, `bottom`, `page_up`, `page_down`, `input_up`, `input_down`
- **Navigation** — `team_prev`, `team_next`, `channel_prev`, `channel_next`,
  `goto_team`, `goto_dm`, `goto_feed`, `switcher`, `command_picker`,
  `load_team`, `move_team_left`, `move_team_right`
- **Sidebar** — `filter`, `clear_filter`, `open_channel`, `mark_read`,
  `channel_info`
- **Messages** — `open_thread`, `reply_in_thread`, `goto_parent`, `edit_post`,
  `delete_post`, `react`, `collapse_message`, `close_thread`,
  `prev_own_message`, `copy_markdown`, `copy_code_block`, `edit_history`,
  `open_attachment`, `download_attachment`, `open_reference`, `preview_image`
- **Search** — `search_here`, `search_all`, `next_match`, `prev_match`
- **Feed** — `feed_mark_all_read`, `feed_toggle_muted`, `feed_reply`, `refresh`
- **Composer** — `compose`, `send`, `newline`, `leave_input`, `clear_input`,
  `undo`, `redo`, `paste`, `attachment_remove`, `apply_open`, `cancel_edit`,
  `select_left`, `select_right`, `select_up`, `select_down`, `copy_selection`,
  `cut_selection`
- **Reference panel** — `jira_status`, `jira_priority`, `jira_points`,
  `jira_assignee`, `jira_comment`, `jira_reply`, `gitlab_approve`,
  `gitlab_merge`, `gitlab_jobs`
- **Misc** — `confirm_yes`, `confirm_no`, `sheet_remove`, `help`, `quit`

The jump-to actions (`goto_team` = `alt+1…9`, `goto_dm` = `alt+d`, `goto_feed` =
`alt+u`) need `alt` to reach the app at all; on macOS that means
`macos-option-as-alt = true` in Ghostty, or rebinding them here
(`goto_team: [super+1, super+2, …]`).

### Search, summaries and AI

All of it is optional and all of it talks to OpenAI-compatible endpoints, so a
local [llama.cpp](https://github.com/ggml-org/llama.cpp) server keeps your chat
history on your machine. Leave the sections alone and the features simply stay
unused; when a server is down, semantic search degrades to keyword and summaries
fall back to raw text.

`summary:` — the `> Summarize` command (`ctrl+k`), and the chat model the
`listen` daemon and agentic search reuse:

| Key | Default | What it does |
|---|---|---|
| `summary.endpoint` | `http://127.0.0.1:8321` | Base URL of the chat server; matterbox appends `/v1/chat/completions` (a trailing `/v1` is accepted). |
| `summary.api_key` | *(empty)* | Optional Bearer token — unnecessary locally, required by hosted APIs. |
| `summary.model` | `gemma-4-E4B-it-UD-Q4_K_XL.gguf` | Model id, sent verbatim. `curl <endpoint>/v1/models` shows what your server actually has loaded. |
| `summary.prompt` | *(a summarising system prompt)* | System prompt prepended to the transcript. Your `@username` is appended at request time so the model can flag where you were mentioned. |

`ai_search:` — agentic search, triggered by ending a Search-tab query with `?`.
It reuses `summary.endpoint` and `summary.model`; these keys tune the agent:

| Key | Default | What it does |
|---|---|---|
| `ai_search.prompt` | *(the shipped agent prompt)* | Frames the loop: how to route who/where/what, when to stop, never to answer from its own knowledge. Team names and the current scope are appended per request. |
| `ai_search.max_steps` | `32` | Cap on tool-call rounds before the model must answer with what it has — keeps a small model from looping. |
| `ai_search.timeout_minutes` | `4` | Bound on the whole run, all rounds together. Raise it for a slow server or a high `max_steps`. |

> **The agent prompt is version-managed.** matterbox knows the hash of every
> default agent prompt it has ever shipped. If yours is byte-identical to one of
> them it was never edited, so an upgrade rolls it forward to the current
> default — otherwise a config written a year ago would keep instructing the
> model to use tool parameters that no longer exist. Change one character and
> the prompt is yours: matterbox never touches it again.

`embeddings:` — semantic search. A **separate** server from `summary`, because an
embedding model has to be loaded with `--embeddings`; see
[`scripts/llama-embeddings.sh`](../scripts/llama-embeddings.sh).

| Key | Default | What it does |
|---|---|---|
| `embeddings.endpoint` | `http://127.0.0.1:8322` | Base URL; matterbox appends `/v1/embeddings`. Note the different port. |
| `embeddings.api_key` | *(empty)* | Optional Bearer token. |
| `embeddings.model` | `embeddinggemma-300m-qat-Q8_0.gguf` | Embedding model id, sent verbatim. |
| `embeddings.dim` | `256` | Truncate each vector to its first *n* components and renormalise — a Matryoshka model stays meaningful smaller, and the on-disk vector shrinks to `dim` bytes. `0` keeps the model's native dimensionality. |
| `embeddings.auto_index` | `true` | Let the TUI embed not-yet-indexed messages in the background (newest first, plus new arrivals). `false` reserves the GPU for the chat model and leaves indexing to `matterbox embed`. |

Vectors are stored tagged with `model@dim`, so changing either key makes the
existing ones "not ours": every message is re-pended for embedding rather than
compared against vectors from a different model. Expect a full re-index after
such a change (`matterbox embed`, or let the background indexer catch up).

`search:` — ranking for both the Search tab and AI search:

| Key | Default | What it does |
|---|---|---|
| `search.recency_half_life_days` | `90` | A match's relevance weight halves per this many days of age, so recent discussion outranks stale chat unless an old message is much more relevant. Lower = stronger recency bias. |

### Composer helpers

`giphy:` — turn a pasted Giphy link into an inline `![alt](url)` image. The
expansion happens instantly and offline from the link's id; with a key, the line
is then upgraded in place with the GIF's real title.

| Key | Default | What it does |
|---|---|---|
| `giphy.api_key` | *(empty)* | Key from [developers.giphy.com](https://developers.giphy.com). `GIPHY_API_KEY` overrides it. |
| `giphy.rendition` | `fixed_height` | Which size to post: `fixed_height` (200px tall, what the Mattermost picker posts), `fixed_height_small` (100px), `fixed_width`, `downsized` / `downsized_medium` (full dimensions, size-capped — need `api_key`), or `original` (full quality, can be several MB). An unrecognised name posts the full-size original rather than reverting to the default, and the API upgrade for it fails — so check the spelling here. |

`language_tool:` — grammar and spell check in the composer. Off by default; when
on, your draft is checked as you type and mistakes are underlined in place, with
`alt+g` opening the suggestions for the one under the cursor.

| Key | Default | What it does |
|---|---|---|
| `language_tool.enabled` | `false` | Turn it on. Everything else has a working default, so `enabled: true` is enough. |
| `language_tool.server_url` | `http://localhost:8010/v2` | The API `/v2` root; the check endpoint is this + `/check`. |
| `language_tool.language` | `auto` | Language code (`en-US`, `en-GB`, `nl`, …) or `auto` to let the server detect it per message. |
| `language_tool.picky` | `false` | LanguageTool's "picky" level — stricter style, typography and grammar rules. |

### The `listen` daemon

`matterbox listen` holds a WebSocket open, keeps the local cache warm, and
bridges mentions and DMs to Telegram. `telegram:` is the delivery channel:

| Key | Default | What it does |
|---|---|---|
| `telegram.bot_token` | *(empty)* | Token from [@BotFather](https://t.me/botfather). Empty disables delivery entirely — the daemon still warms the cache. |
| `telegram.chat_id` | *(empty)* | Destination: a numeric chat id (message the bot, then read it from `https://api.telegram.org/bot<token>/getUpdates`) or an `@channelusername`. Also the only sender the bot obeys for two-way mode. |

`listen:` is the behaviour:

| Key | Default | What it does |
|---|---|---|
| `listen.notify_on_mention` | `true` | Forward direct @mentions (and DMs, if `notify_dms`). `false` runs the daemon as a pure cache-warmer. |
| `listen.summarize` | `true` | Send an LLM summary of the surrounding conversation instead of the raw message, using `summary.endpoint`/`model`. Falls back to raw text automatically when the chat server is down. |
| `listen.notify_prompt` | *(a one-or-two-sentence prompt)* | System prompt for that summary. Your `@username` and the message source are appended per request. |
| `listen.respect_mutes` | `true` | Skip channels you muted in Mattermost. |
| `listen.respect_dnd` | `true` | Skip notifications while your Mattermost status is Do Not Disturb. `urgent` notify actions bypass this either way. |
| `listen.quiet_hours` | *(empty)* | Suppress pushes during a daily window, `"HH:MM-HH:MM"` local, may wrap midnight (`"22:00-08:00"`). Messages are still cached — catch up with the bot's `/unread`. |
| `listen.two_way` | `true` | Accept input from Telegram: reply to a notification to post back, tap the 👍 / ✓ buttons, run `/search`, `/unread`, `/digest`, `/ask`. Needs `telegram.chat_id`. |
| `listen.notify_dms` | `false` | Also forward direct messages. Off by default so a DM you are actively reading doesn't ping your phone. |
| `listen.notify_delay_seconds` | `60` | Wait this long before sending, then re-check the server's read state: if any client (TUI, mobile, web, on any machine) marked the channel read during the window, the notification is dropped. `0` delivers immediately with no read-check. |

These options are not only about the built-in behaviour: **every** `notify`
action passes the same gate, including ones you write in `rules:`. So
`notify_dms: false` silences a rule's notify on a direct message too;
`summarize` is the default a rule's own `summarize:` overrides; and
`respect_mutes` / `respect_dnd` / `quiet_hours` are skipped only for an action
marked `urgent: true`. `notify_on_mention` is the exception — it decides whether
the built-in rule exists at all, and has no effect once you write your own
rules.

Independent of all of the above, the daemon stays quiet about what you are
looking at right now: before notifying it asks the TUI on this machine what is
on screen and skips the push if that channel is open in a focused window. Rules
gate on the same signal with
[`viewing: false`](rules.md#not-while-youre-reading-it).

### `rules`

With no `rules:` block the daemon behaves exactly as the `listen:` options
describe — that default *is* a rule. Add a `rules:` list to take over: match on
team, channel, author, message text, mention, bot, channel type, time of day or
thread status, and run actions (`notify`, `exec`, `webhook`, `send`, `react`,
`mark_read`, `log`, and the persistent-ledger `state_*` actions). Rules can fire
on new messages, edits, deletions, reactions, or on the clock
(`cron: "0 9 * * 1-5"`).

It is the largest thing in the config by far and has its own reference:
**[the rules engine](rules.md)**. `matterbox rules test` says which rules a message
would fire and why the rest wouldn't, and `matterbox rules list` / `stats` /
`state` show what loaded, what has fired, and what the ledger remembers.

Rules are compiled when they load, so a bad glob, regexp or action type is a
startup error rather than a rule that silently never fires — and a `SIGHUP`
reload that fails to compile changes nothing: the daemon logs the error and
keeps the ruleset it already had, so a half-written edit can't disarm a working
daemon.

### Integrations: Jira and GitLab

Both are opt-in and both hang off one key: `v` on a message that names a Jira
issue or links a GitLab merge request opens it in a side panel — read-only, with
inline editing when the token allows it.

```yaml
jira:
  base_url: https://your-instance.atlassian.net
  email: you@example.com
  api_token: …
  projects: [ABC, PROJ]
gitlab:
  base_url: https://git.example.com
  token: glpat-…
```

| Key | Default | What it does |
|---|---|---|
| `jira.base_url` | *(empty)* | Instance root. Also how matterbox recognises `/browse/KEY` links pointing at *your* instance. |
| `jira.email` | *(empty)* | Atlassian account email — the username half of the Cloud Basic-auth pair. |
| `jira.api_token` | *(empty)* | API token from id.atlassian.com → Security → API tokens. `JIRA_API_TOKEN` overrides it, which keeps the secret out of the file. |
| `jira.projects` | *(empty)* | Project keys whose **bare** ids (`ABC-123`) open the panel. Empty means only full `/browse/KEY` links are detected — so look-alikes like `UTF-8` never trigger. |
| `jira.story_points_field` | *(empty)* | Pin the story-points custom field (`customfield_10016`). Empty auto-detects it from the instance's field metadata; set this only if auto-detection picks the wrong field. |
| `gitlab.base_url` | *(empty)* | Instance root, also used to recognise `/-/merge_requests/N` links for this host. |
| `gitlab.token` | *(empty)* | Personal or project access token. Empty falls back to `GITLAB_TOKEN`, then to the token `glab auth login` stored for this host in `~/.config/glab-cli/config.yml` — so a working `glab` setup needs no secret here. |

Jira targets **Cloud** (`/rest/api/3`); Server/Data Center instances won't work
as-is. What a token needs:

- **GitLab** — `read_api` for everything read-only (the panel, `!iid` badges,
  pipeline and approval state); `api` for the approve and merge actions, since
  GitLab has no narrower scope for MR writes.
- **Jira, classic token** — unscoped, so access follows your project
  permissions: *Browse Projects* to view, plus *Transition / Assign / Edit
  Issues* for the inline edits.
- **Jira, scoped token** — `read:jira-work` + `read:jira-user` to view, plus
  `write:jira-work` for the edits.

### `telemetry`

Anonymous usage telemetry and error reports. **Off unless you turned it on.**
`matterbox welcome` asks; using the client never does, so upgrading into a build
that has telemetry changes nothing until you run the wizard. What is collected,
and how to stop it, is documented in [telemetry.md](telemetry.md) — the complete
event catalogue, generated from the code so it cannot fall out of step — and at
<https://matterbox.work/docs/telemetry>.

```yaml
telemetry:
  enabled: true
  anonymous_id: 4f0c…
```

| Key | Default | What it does |
|---|---|---|
| `telemetry.enabled` | *(absent — off)* | Send anonymous usage data and error reports. Absent means nobody has answered the question, which behaves exactly like `false`; only an explicit `true` sends anything. |
| `telemetry.anonymous_id` | *(empty)* | The random id reports are grouped by, minted when you opt in. Nothing about your account, server or machine goes into it — delete it to become a new, unrelated user. |

Turning it off after the fact is one line (`enabled: false`); matterbox also
drops the id when you decline in the wizard, so opting out leaves nothing behind
to tag you with. Re-running `matterbox welcome` is how you change your answer:
the question opens on whatever the config currently says. The config key is the
only gate: the PostHog key is compiled into every build, including one you built
yourself, and does nothing without an explicit `enabled: true`.

### `update_check`

Whether matterbox looks for a newer release. **On unless you turn it off** —
which is the opposite of `telemetry`, deliberately, because it is a different
kind of thing. Once a day it fetches
<https://matterbox.work/latest.json>: a small file that answers everyone the
same way. The request carries no version, no platform and no identifier, and
the comparison happens on your machine, so there is nothing in it to count
installs with. It reveals no more about you than opening the website does,
which is why it is not behind the telemetry question.

Nothing is ever installed on its own. When a newer release exists matterbox
mentions it twice and then stops: a small box in the top-right corner, which goes
away by itself or when you click it, and a line when you quit, where the command
can actually be typed.

```yaml
update_check:
  enabled: false
```

| Key | Default | What it does |
|---|---|---|
| `update_check.enabled` | `true` | Check once a day whether a newer release exists, and say so if it does. |

Turning it off stops the check, not the upgrade path: `matterbox upgrade` still
works, and still asks the same URL, because you asked it to. A build with no
release name — a plain `go build`, or an install from a branch — never checks
at all, having nothing to compare itself against.

Two things the check will not do. It will not tell you to go *back*: a build a
few commits past v1.1.0 is stamped `v1.1.0-3-gabc1234`, and only the numeric
`1.1.0` is compared, so being ahead of a release never reads as being behind
one. And it will not nag: a failed check waits an hour, a successful one waits
a day, the notice is said once per session, and it waits for a frame with no
popup on it rather than being stamped over whatever you had open.

### `matterbox upgrade`

Not a config key, but the other half of the same story. It installs the current
release over this one by running the same installer the website hands out, and
works out *how* rather than guessing:

```
matterbox upgrade                    # the latest release
matterbox upgrade --check            # say what is current, change nothing
matterbox upgrade --version v1.0.0   # a specific release, including an older one
```

The release binaries carry inline video, so a build with that is simply replaced
by one. A build with the `--demo` soundtrack is rebuilt from source instead,
because no release has it. `matterbox --version` prints which you have. It installs alongside the binary it replaces,
so an upgrade lands wherever the original `--dir` put it, and stays on your
PATH.

## Environment variables

| Variable | Effect |
|---|---|
| `MATTERBOX_CONFIG_DIR` | Use this directory for config, token, cache and stats. Named verbatim. |
| `JIRA_API_TOKEN` | Overrides `jira.api_token`. |
| `GITLAB_TOKEN` | Used when `gitlab.token` is empty (before the `glab` fallback). |
| `GIPHY_API_KEY` | Overrides `giphy.api_key`. |
| `NO_COLOR` | Disables code-block colour regardless of `code_theme`. |
| `MATTERBOX_POSTHOG_KEY` | Project key telemetry reports to, overriding the compiled-in one. For working on telemetry against your own PostHog project; consent is still required. |
| `MATTERBOX_POSTHOG_HOST` | Ingest host for telemetry (default `https://eu.i.posthog.com`). |

## Recipes

**A calm terminal.** No motion, no mouse capture, every message in full:

```yaml
mouse: false
collapse_long_messages: 0
animations:
  custom_emoji: false
  image_preview: false
  inline_images: false
```

**macOS-friendly navigation**, avoiding the Mission Control collision and
`alt`-key trouble:

```yaml
keybindings:
  nav_modifier: shift
  vim_nav: reading
  bindings:
    goto_team: [super+1, super+2, super+3, super+4, super+5]
    goto_dm: super+d
    goto_feed: super+u
```

**A quiet daemon** — keep the cache warm and search fresh, notify nothing:

```yaml
listen:
  notify_on_mention: false
```

**Local AI, all of it** — chat model on `:8321`, embeddings on `:8322`,
background indexing on:

```yaml
summary:
  endpoint: http://127.0.0.1:8321
  model: your-chat-model.gguf
embeddings:
  endpoint: http://127.0.0.1:8322
  model: embeddinggemma-300m-qat-Q8_0.gguf
  auto_index: true
```

Then `matterbox embed` to backfill, and `matterbox search --semantic` or the
Search tab to use it.

## Troubleshooting

**"parse config …: yaml: line N"** — a syntax error; matterbox refuses to start
rather than guess. Indentation is the usual culprit. Your editor flags most of
these before you save if the schema modeline is in place.

**A setting does nothing.** Check the spelling: unknown keys are ignored at load
time. `matterbox keys` shows the effective keybindings, and the schema in your
editor marks unknown keys as errors — an unrecognised value for an enum-ish key
(`nav_modifier`, `code_theme`, …) silently reverts to the default.

**It won't start after a keybinding change.** That is by design: unknown action
ids, bad chords, and collisions fail loudly, and the error lists the valid
action ids. `matterbox keys` prints the same list.

**My comments vanished.** matterbox rewrote the file — see [First run, and when
matterbox rewrites the file](#first-run-and-when-matterbox-rewrites-the-file).

**Start over.** Move the file aside and let matterbox write a fresh one:

```sh
mv ~/.config/matterbox/config.yaml{,.bak}
matterbox welcome
```

The message cache, token and stats are separate files, so a fresh config costs
you nothing but your settings.
