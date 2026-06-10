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
  and history is searchable offline.
- **Full-text search** — keyword search over the local cache via SQLite FTS5,
  available both in the TUI and as `matterbox search`.
- **Semantic search** — optional vector search over your message history using a
  local embeddings model (no data leaves your machine). Blended with keyword
  results via hybrid ranking.
- **AI summaries** — summarize a channel or thread with a local LLM (Ctrl+K in the TUI).
- **Agentic search** — end a query with `?` on the Search tab and a local model
  uses tools to dig through your channels to answer it.

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

2. **Authenticate.** `mm_login.py` opens a browser for GitLab SSO login and saves
   the session token to `~/.config/matterbox/mm_token.json`:

   ```sh
   python mm_login.py --url https://mattermost.example.com
   ```

   (Alternatively, drop any valid Mattermost session/access token into that file as
   `{"token": "..."}`.)

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
| `matterbox search <query>` | Search the local cache (`--semantic`, `--channel`, `--context`, `--json`) |
| `matterbox channels` | List all teams and channels with their addresses |
| `matterbox digest` | Show your own recent messages in a time range |
| `matterbox whoami` | Print the authenticated user |
| `matterbox embed` | Backfill semantic-search embeddings for cached messages |

Channels are addressed as `team/channel` (e.g. `eng/general`) or `@username` for a DM.

## AI features (optional)

Summaries and search use OpenAI-compatible endpoints configured under `summary`,
`ai_search`, and `embeddings` in `config.yaml` (defaults assume a local llama.cpp on
`127.0.0.1:8321` for chat and `:8322` for embeddings). A helper script for the
embeddings server is in [`scripts/llama-embeddings.sh`](scripts/llama-embeddings.sh).

Once an embeddings server is running, build the index with `matterbox embed` (or let
the TUI index in the background), then search with `matterbox search --semantic`.
