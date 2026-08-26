# matterbox

A from-scratch **Go TUI Mattermost client** (terminal, bubbletea). Module `matterbox`, Go 1.26. The TUI is the root command; other front-ends/verbs hang off the same binary.

## Layout
- `internal/ui` — the bubbletea TUI (the bulk of the app)
- `internal/mm` — Mattermost API/WS client · `internal/store` — SQLite message cache
- `internal/cli` — cobra subcommands · `internal/listen` — `matterbox listen` daemon
- `main.go` — entry point

## Build & test
- `make` build · `make test` · `make run` · `make install` (per-user, no root)

## Gotchas (always relevant)
- `ui.Model` is ~133KB. `View()` runs on every keystroke — it's a hot path. Use **pointer receivers** on render helpers; don't add uncached work to the View/resize paths.
- Message cache: SQLite + FTS5 at `~/.config/matterbox/messages.db` (serves warm-reopen render + local search).
- Open conversation = `m.openChannelID`; sidebar cursor = `m.channelIdx`. Routing/title/actions use `openChannelID`, not the cursor.
- Don't auto-start `llama-server` (semantic/AI search backend) — the user launches it manually; hand them the command.
