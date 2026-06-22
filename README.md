# matterbox

A fast terminal client for [Mattermost](https://mattermost.com) — a full TUI plus a
scriptable CLI, with a local message cache and optional AI features powered by a
local LLM.

## Features

- **Terminal UI** — teams, channels, DMs, threads, reactions, attachments, and
  live updates over WebSocket, built on [Bubble Tea](https://github.com/charmbracelet/bubbletea).
- **Scriptable CLI** — read, send, and search Mattermost from the shell or scripts,
  with `--json` output on most commands and shell completion for zsh/bash/fish.
- **Local message cache** — every message you see is stored in a local SQLite
  database (`~/.config/matterbox/messages.db`) so reopening a channel is instant
  and history is searchable offline. An optional read-only **SQL tab**
  (`sql_tab: true`) lets you query it directly — see [docs/database.md](docs/database.md).
- **Full-text search** — keyword search over the local cache via SQLite FTS5,
  available both in the TUI and as `matterbox search`.
- **Semantic search** — optional vector search over your message history using a
  local embeddings model (no data leaves your machine). Blended with keyword
  results via hybrid ranking.
- **Feed tab** — aggregated view of all unread messages across channels and DMs,
  excluding muted channels.
- **Jira integration** — press `v` on a Jira issue link to open a side panel; edit
  Status, Priority, Story points, and Assignee inline without leaving the TUI.
- **GitLab integration** — press `v` on a GitLab MR link to open a merge-request
  side panel with pipeline, approvals, and status; approve or merge inline.
- **AI summaries** — summarize a channel or thread with a local LLM (Ctrl+K in the TUI).
- **Agentic search** — end a query with `?` on the Search tab and a local model
  uses tools to dig through your channels to answer it.
- **Clipboard paste** — paste images and files from the clipboard directly into
  the composer (macOS and Linux with wl-clipboard/xclip).
- **Per-channel drafts** — unsent composer text is saved per channel via
  Mattermost's server-side drafts API, so each channel keeps its own draft,
  switching channels never loses what you typed, and a draft started here shows
  up (and stays in sync) in the webapp, mobile, and across restarts.
- **Rules engine** — make the `listen` daemon react to messages: match on
  team/channel/author/text/mention and run actions (notify, run a local command,
  POST a webhook, post a message back, react, mark read). The Telegram bridge is
  itself just the default rule. See [docs/rules.md](docs/rules.md).

The AI features (summaries, semantic/agentic search) are entirely optional and talk
to any OpenAI-compatible endpoint — point them at a local
[llama.cpp](https://github.com/ggml-org/llama.cpp) server, or leave them off.

## Requirements

- Go 1.26+
- A Mattermost server and an account on it
- *(optional, for AI features)* a local OpenAI-compatible LLM server, e.g. llama.cpp

## Build & install

```sh
make            # build ./matterbox
make install    # build + install to ~/.local/bin and set up shell completion
make run        # build and launch the TUI
```

Or with plain Go:

```sh
go build -o matterbox .
```

## Configure & log in

1. **Point matterbox at your server.** On first run a config file is created at
   `~/.config/matterbox/config.yaml`. Set your instance URL:

   ```yaml
   server_url: https://mattermost.example.com
   ```

2. **Authenticate.** `matterbox login` opens your browser for GitLab SSO and saves
   the session token to `~/.config/matterbox/mm_token.json`:

   ```sh
   matterbox login
   ```

   It uses Mattermost's native-login endpoint, which hands the token back via an
   `mmauth://` link once you authorize. On **Linux** matterbox registers itself as
   the `mmauth://` handler, so your browser offers to "open Matterbox Login Handler"
   and the token is captured automatically. On other platforms (or if you decline),
   right-click the **link** on the success page, choose **Copy Link Address**, and
   paste it at the prompt.

   `matterbox login --show` prints where the token is stored; `--clear` removes it.

   (Alternatively, drop any valid Mattermost session/access token into that file as
   `{"token": "..."}` — or paste a raw token at the `login` prompt.)

3. **Run it.**

   ```sh
   matterbox
   ```

## CLI

Running `matterbox` with no arguments launches the TUI. The subcommands are for
scripting:

| Command | What it does |
|---|---|
| `matterbox send <channel> [message]` | Post a message (`--file` to attach, repeatable) |
| `matterbox read [channel]` | Print recent messages (`--since`, `--from`, `--thread`, `--wait`, `--json`) |
| `matterbox unread` | List unread messages grouped by channel |
| `matterbox mark-read <channel>...` | Mark one or more channels/DMs as read (clear unread) |
| `matterbox search <query>` | Search the local cache (`--semantic`, `--channel`, `--context`, `--json`) |
| `matterbox channels` | List all teams and channels with their addresses |
| `matterbox digest` | Show your own recent messages in a time range |
| `matterbox whoami` | Print the authenticated user |
| `matterbox embed` | Backfill semantic-search embeddings for cached messages |
| `matterbox listen` | Background daemon: keeps cache warm and bridges @mentions/DMs to Telegram |
| `matterbox keys` | List all keyboard actions, default keys, and your config overrides |

Channels are addressed as `team/channel` (e.g. `eng/general`) or `@username` for a DM.

## Keybindings

Press `?` in the TUI for the full, grouped list; the footer shows the primary
keys for the focused pane. Three knobs under `keybindings:` in `config.yaml`
tune them:

```yaml
keybindings:
  nav_modifier: ctrl     # modifier for arrow-key team/channel nav:
                         # ctrl (default), alt, shift, super (⌘ / Windows;
                         # also "cmd"), meta, hyper, or none. On macOS ctrl+arrows
                         # clash with Mission Control — try shift, or super on a
                         # Kitty-protocol terminal (Ghostty/kitty/WezTerm).
  vim_nav: global        # when ctrl+h/j/k/l switch team/channel:
                         #   global  — from any focus, even while typing (default)
                         #   reading — only outside text inputs, so ctrl+h / ctrl+k
                         #             stay as the composer's emacs editing keys
                         #   off     — never (arrow nav still works in every mode)
  bindings:              # rebind individual actions (optional)
    compose: [i, a]      # a single key or a list
    delete_post: shift+d
    quit: []             # empty list (or "none") unbinds — ctrl+c always quits
    channel_next: ctrl+j # rebinding a nav action drops its modifier-arrow alias too
```

Each `bindings:` value names an **action** (`compose`, `channel_next`,
`delete_post`, …) and the key or keys that trigger it. Modifiers are
`ctrl`/`alt`/`shift`/`super`/`meta`/`hyper`. An unknown action id, an
unparseable chord, or a binding that would collide with another action is
reported as a startup error (with the full list of valid action ids), so a
typo fails loud rather than silently shadowing a key.

> Some chords only arrive on terminals that speak the Kitty keyboard protocol
> (Ghostty, kitty, WezTerm). `shift+enter` for example *sends* the message on a
> legacy terminal instead of inserting a newline — use `alt+enter` there.

## Jira & GitLab integration (optional)

Press `v` on a message that names a Jira issue or links a GitLab merge request to
open it in a side panel — read-only by default, with inline editing/actions when
the token allows it. Both are opt-in and configured in `config.yaml`.

### GitLab

```yaml
gitlab:
  base_url: https://git.example.com
  token: glpat-…            # optional — see fallbacks below
```

`token` may be a **personal access token** or a **project access token**, with
these scopes:

| Scope | Covers |
|---|---|
| `read_api` | Everything read-only: the MR panel, inline `!iid` badges, pipeline/stage status, approval state. |
| `api` | The above **plus** the approve and merge actions. GitLab has no narrower scope for MR writes (`write_repository` is git-over-HTTPS only and does **not** cover the MR API). |

Use `read_api` unless you actually approve/merge from the TUI. If `token` is left
empty, matterbox falls back to the `GITLAB_TOKEN` env var, then to an existing
`glab` login (the token `glab auth login` stored for this host in
`~/.config/glab-cli/config.yml`) — so a working `glab` setup needs no secret in
this file.

### Jira (Cloud only)

```yaml
jira:
  base_url: https://your-instance.atlassian.net
  email: you@example.com
  api_token: …             # or the JIRA_API_TOKEN env var
  projects: [ABC, PROJ]    # optional: enable bare-id detection (ABC-123)
```

Authentication is HTTP Basic with your Atlassian account email + an API token
(id.atlassian.com → Security → API tokens). What you need depends on the **kind**
of token:

- **Classic API token** (the default) — unscoped; it acts as your user, so access
  is gated by your Jira **project permissions**, not token scopes. *Browse
  Projects* is enough to view; *Transition Issues*, *Assign Issues*, and *Edit
  Issues* enable the inline status / assignee / priority / story-points edits.
- **Scoped API token** (the newer option) — select `read:jira-work` and
  `read:jira-user` for viewing, and add `write:jira-work` for the inline edits.

This integration targets Jira **Cloud** — it uses the `/rest/api/3` endpoints, so
Server/Data Center instances won't work as-is.

## AI features (optional)

Summaries and search use OpenAI-compatible endpoints configured under `summary`,
`ai_search`, and `embeddings` in `config.yaml` (defaults assume a local llama.cpp on
`127.0.0.1:8321` for chat and `:8322` for embeddings). A helper script for the
embeddings server is in [`scripts/llama-embeddings.sh`](scripts/llama-embeddings.sh).

Once an embeddings server is running, build the index with `matterbox embed` (or let
the TUI index in the background), then search with `matterbox search --semantic`.

## listen daemon (optional)

`matterbox listen` holds a persistent WebSocket connection to your Mattermost
server and writes every incoming message into the local cache — so the TUI
reopens warm and `search`/`digest` stay fresh without launching the UI.

When configured, it also bridges your @mentions and DMs to Telegram, optionally
summarising the surrounding conversation via the chat model first. Two-way mode
lets you reply from Telegram back into Mattermost. The daemon also accepts `/ask`
commands from Telegram to run agentic search against your message history.

Run it under a process supervisor. `make install` drops a disabled service for
your platform:

```sh
# Linux (systemd --user)
systemctl --user enable --now matterbox-listen.service
journalctl --user -u matterbox-listen -f

# macOS (launchd)
launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/com.matterbox.listen.plist
tail -f ~/Library/Logs/matterbox-listen.log
```

Configure under the `telegram` and `listen` sections in `config.yaml`
(set `telegram.bot_token` from @BotFather and `telegram.chat_id`). The daemon is
safe to run alongside the TUI — both write idempotent rows into the WAL-mode store.

### Rules

What the daemon does with each incoming message is driven by rules. With no
`rules:` block it behaves exactly as before — forwarding your @mentions and DMs
to Telegram (that default *is* a rule). Add a `rules:` block to take over: match
on team, channel, author, message text, mention, attachments, or thread status,
and run actions — `notify`, `exec` (run a local command with the message piped in
as JSON), `webhook`, `send` (post a message back), `react`, `mark_read`, or
`log`. See [docs/rules.md](docs/rules.md) for the full reference and examples.
