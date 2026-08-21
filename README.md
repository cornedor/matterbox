# matterbox

![Matterbox](./docs/banner.jpg)

A fast terminal client for [Mattermost](https://mattermost.com) — a full TUI plus a
scriptable CLI, with a local message cache and optional AI features powered by a
local LLM.

Screenshots, the feature tour, and the docs live at **[matterbox.work](https://matterbox.work)**.

## What it does

- **Terminal UI** — teams, channels, DMs, threads, reactions, attachments, live over
  WebSocket, built on [Bubble Tea](https://github.com/charmbracelet/bubbletea).
- **Search that finds things** — every message you see lands in a local SQLite database
  (`~/.config/matterbox/messages.db`), and FTS5 searches all of it in the TUI or as
  `matterbox search` — offline, instantly, however far back it goes. The same cache is
  why reopening a channel is instant. An optional read-only SQL tab queries it directly
  ([docs/database.md](docs/database.md)).
- **Animated GIFs, in a terminal** — on one that speaks the Kitty graphics protocol
  (Ghostty, kitty, WezTerm), images and GIFs are drawn inline in the transcript and
  actually play, custom emoji included. Short clips too, on a build with video support.
- **Scriptable CLI** — read, send, and search from the shell, `--json` on most
  commands, completion for zsh/bash/fish.
- **Feed** — every unread message across channels and DMs in one list.
- **Nested replies** — press `r` on a reply and the thread pane draws a tree instead
  of Mattermost's flat list. Other clients still see a normal reply.
- **Paste and drop** — paste an image from the clipboard, or drag a file onto the
  terminal; both become attachments.
- **Jira and GitLab** — press `v` on an issue or MR link for a side panel: edit status,
  assignee, or story points, approve or merge, without leaving the TUI.
- **AI, if you want it** — channel and thread summaries, semantic search over your
  history, and an agentic search that digs through channels to answer a question. All
  optional, all against any OpenAI-compatible endpoint, so nothing needs to leave your
  machine.
- **Rules engine** — the `listen` daemon reacts to what happens on your server: match
  on channel, author, text, or mention and notify, run a command, POST a webhook, post
  back, react, or mark read — on a schedule too. [docs/rules.md](docs/rules.md).

## Install

Prebuilt Linux and macOS binaries (amd64 + arm64) are on the
[releases page](https://github.com/cornedor/matterbox/releases) — pure Go, so they need
no toolchain, though they carry no optional features. Drop one in `~/.local/bin`.

From source, with Go 1.26+:

```sh
make install    # build, install to ~/.local/bin, set up shell completion
```

Some optional features need C libraries; `make` detects what your machine has and
compiles in whatever it can, so a plain `make install` is all anyone needs. `make tags`
shows what was picked and how to unlock the rest. Handing a self-built binary to
someone else has licensing consequences — see [docs/building.md](docs/building.md).

`matterbox --version` names the build, its optional features, and the platform.

## Configure & log in

### Setup tool (recommended)

Run the interactive wizard to set the server URL, log in, and pick basic preferences:

```sh
matterbox welcome
```

Running `matterbox` with no saved login starts it for you.

The wizard asks whether anonymous usage telemetry and error reports may be collected.
It is off unless you say yes, and it is only ever asked here — using the client never
prompts you. Re-run the wizard to change your answer, or set
`telemetry.enabled: false`. Every event and property it can send is listed in
[docs/telemetry.md](docs/telemetry.md) — generated from the catalogue in
`internal/telemetry`, so it cannot drift — and published at
<https://matterbox.work/docs/telemetry>.

### Manual configuration

Edit `~/.config/matterbox/config.yaml` directly for full control. At minimum:

```yaml
server_url: https://mattermost.example.com
```

Then `matterbox login` for GitLab SSO. It saves the token to
`~/.config/matterbox/mm_token.json` — on Linux automatically, elsewhere by pasting the
link from the success page at the prompt. Any valid session token works if you'd rather
write that file yourself: `{"token": "..."}`.

The config file is written with every key at its default and a comment explaining it,
plus a JSON Schema for editor autocomplete; [docs/config.md](docs/config.md) is the full
reference. `MATTERBOX_CONFIG_DIR` moves config, token, and cache somewhere else — handy
for a second profile.

## CLI

Running `matterbox` with no arguments launches the TUI. The subcommands are for setup
and scripting:

| Command | What it does |
|---|---|
| `matterbox welcome` | First-run setup: server URL, sign-in, a few preferences |
| `matterbox login` | Sign in and save the session token |
| `matterbox send <channel> [message...]` | Post a message, with attachments |
| `matterbox reply <message-id> [message...]` | Reply in a message's thread |
| `matterbox react <message-id> <emoji> [emoji...]` | React to a message |
| `matterbox read [channel]` | Print recent messages |
| `matterbox unread` | List unread messages, grouped by channel |
| `matterbox mark-read <channel>...` | Clear unread on one or more channels |
| `matterbox open <channel>` | Jump the running TUI to a channel |
| `matterbox search <query>` | Search the local cache, keyword or `--semantic` |
| `matterbox channels` | List teams and channels with their addresses |
| `matterbox digest` | Your own recent messages in a time range |
| `matterbox embed` | Backfill semantic-search embeddings |
| `matterbox listen` | Background daemon: warm cache, notifications, rules |
| `matterbox rules` | Inspect, list, and test the `listen` rules |
| `matterbox keys` | Print every keyboard action and your overrides |

Channels are `team/channel` (e.g. `eng/general`), or `@username` for a DM. Every flag is
in `--help` and at [matterbox.work/docs/cli](https://matterbox.work/docs/cli/).

## Keys

The footer shows the keys for whatever has the keyboard. `?` expands that into
everything that works right now; `f1 › Keys` — or `matterbox keys` — is the full
cheatsheet of your effective bindings. Every action can be rebound under `keybindings:`
in `config.yaml`; [docs/config.md](docs/config.md) lists them.

Two things that bite on day one:

- On macOS, `ctrl`+arrows clash with Mission Control. Set `keybindings.nav_modifier: shift`.
- Some chords need a terminal that speaks the Kitty keyboard protocol. Elsewhere
  `shift+enter` sends the message instead of inserting a newline — use `alt+enter`.

## Jira, GitLab & GitHub (optional)

Press `v` on a message naming a Jira issue or linking a GitLab MR / GitHub issue
or pull request. Read-only by default, with inline editing and actions when the
token allows it. GitLab and GitHub share one forge panel; GitHub issues are
read-only (approve/merge are pull requests only). Public GitHub repos work
without a token (60 requests/hour anonymous).

```yaml
gitlab:
  base_url: https://git.example.com
  token: glpat-…          # or $GITLAB_TOKEN, or an existing `glab auth login`

github:
  base_url: https://github.com   # optional default
  token: ghp_…                   # or $GITHUB_TOKEN/$GH_TOKEN, or `gh auth login`
  # client_id: …               # only for optional `matterbox github login`

jira:                     # Cloud only — it uses /rest/api/3
  base_url: https://your-instance.atlassian.net
  email: you@example.com
  api_token: …            # or $JIRA_API_TOKEN
  projects: [ABC, PROJ]   # optional: detect bare ids like ABC-123
```

GitLab needs `read_api` to view and `api` to approve or merge — there is no narrower
scope for MR writes. Jira runs on your own project permissions with a classic API
token, or `read:jira-work` + `read:jira-user` (+ `write:jira-work` to edit) with a
scoped one.

## AI (optional)

Summaries, semantic search, and agentic search talk to OpenAI-compatible endpoints
configured under `summary`, `ai_search`, and `embeddings`. The defaults assume a local
llama.cpp on `127.0.0.1:8321` for chat and `:8322` for embeddings, and
[`scripts/llama-embeddings.sh`](scripts/llama-embeddings.sh) starts the latter. Build
the index once with `matterbox embed`, then `matterbox search --semantic`.

On the Search tab, a query ending in `?` runs the agentic version.

## listen daemon (optional)

`matterbox listen` holds a WebSocket open and writes every incoming message into the
cache, so the TUI reopens warm and `search`/`digest` stay fresh with the UI closed.
Configured, it also forwards your mentions and DMs to Telegram — optionally summarised
first — and takes replies back the other way.

It stays quiet about what you're already reading: before notifying, it asks the TUI on
this machine what's on screen, and skips the push if that channel is open in a focused
window.

`make install` drops a disabled unit for your platform:

```sh
systemctl --user enable --now matterbox-listen.service   # Linux
launchctl bootstrap gui/$(id -u) \                       # macOS
  ~/Library/LaunchAgents/com.matterbox.listen.plist
```

Safe to run alongside the TUI — both write idempotent rows into the WAL-mode store.

### Rules

What the daemon does is driven by rules — the Telegram bridge is just the default one.
Match on team, channel, author, text, mention, bot, channel type, time of day, or
thread status, and run `notify`, `exec`, `webhook`, `send`, `react`, `mark_read`, or
`log`. Rules fire on new messages, edits, deletions, reactions, or a cron schedule.

`matterbox rules test` says which rules a message would fire, and which condition
stopped the rest. [docs/rules.md](docs/rules.md) is the full reference.

## License

[Apache License 2.0](LICENSE). Much of the code was written with AI assistance under
human review.

Two pieces of art in here belong to other people and keep their own terms, both
recorded in [NOTICE](NOTICE): the `--demo` soundtrack is "Paradox #3" by **dubmood /
Razor1911**, and the empty-feed sailing ship is by **Sebastian Stöcker (SSt)** — whose
terms are why the signature stays on the art. Release binaries also ship a
`THIRD_PARTY_LICENSES` file covering every Go module linked into them.
