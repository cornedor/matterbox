# matterbox-server — design & build plan

A Mattermost-wire-compatible server, written from scratch in Go on top of
PostgreSQL, kept MVP-first. The compatibility target is the `matterbox` client
in this repo; the official Mattermost web/desktop client is a stretch goal the
architecture keeps reachable.

The acceptance spec is concrete and finite: every call `matterbox` makes lives
in `internal/mm/client.go` and the WebSocket events it consumes live in
`internal/ui/update.go`. This document turns that surface into a server.

## Stack

- **Language:** Go 1.26 (same toolchain as the client).
- **Wire types:** import `github.com/mattermost/mattermost/server/public/model`
  — the exact package the client uses. The `model.*` structs are the
  serialization contract, so request/response JSON matches by construction and
  there is no shape guesswork.
- **Storage:** PostgreSQL. One schema, accessed through `pgx`.
- **Deployment:** single static binary + a Postgres connection string.
- **HTTP:** stdlib `net/http` + a lightweight router (chi or stdlib 1.22
  `http.ServeMux` patterns). No framework.
- **WebSocket:** `github.com/coder/websocket` (or `gorilla/websocket`) behind a
  hand-written hub.

> Routing note: the canonical path for each handler is whatever
> `model.Client4` calls in the SDK. Derive every route directly from the SDK's
> `client4.go` rather than from memory; the paths below are the expected ones
> and must be confirmed against the pinned SDK version (`server/public v0.4.0`).

## Repository layout (new top-level `server/`)

```
server/
├── cmd/matterbox-server/main.go      flags, config, wire up, listen
├── internal/
│   ├── config/                       env/flags: addr, db dsn, file store, version string
│   ├── store/                        Postgres layer (pgx), one file per aggregate
│   │   ├── migrate/                  embedded SQL migrations
│   │   ├── users.go teams.go channels.go members.go
│   │   ├── posts.go reactions.go drafts.go files.go emoji.go status.go
│   │   └── search.go                 server-side search queries
│   ├── id/                           26-char base32 Mattermost-style IDs
│   ├── auth/                         login, sessions, token middleware
│   ├── api/                          REST handlers, grouped by resource
│   │   ├── router.go                 /api/v4 mux + middleware chain
│   │   ├── users.go teams.go channels.go members.go
│   │   ├── posts.go reactions.go drafts.go files.go emoji.go status.go
│   │   ├── search.go actions.go      search + interactive dialog/action stubs
│   │   └── system.go                 /config/client, /ping, version headers
│   ├── ws/                           WebSocket hub
│   │   ├── hub.go                    connection registry, fan-out
│   │   ├── conn.go                   per-connection read/write pumps, auth
│   │   └── events.go                 typed broadcast helpers
│   └── bus/                          internal event bus REST→WS
└── testdata/                         golden fixtures for client round-trips
```

## Conventions the client depends on

- **IDs:** 26-char Crockford base32, exactly like `model.NewId()`. The client
  treats IDs as opaque but the SDK validates length in places — match it.
- **Timestamps:** unix milliseconds (`int64`), `CreateAt`/`UpdateAt`/`DeleteAt`.
  Soft-delete = non-zero `DeleteAt`, row retained.
- **Login token:** `POST /users/login` must return the session token in the
  `Token` response header (the SDK reads it from there), and also set the
  `MMAUTHTOKEN` cookie.
- **Etags / status codes:** handlers may return `""` etags; match Mattermost's
  status codes (e.g. `201` on create, `200` with `{"status":"OK"}` on the
  view/ack endpoints) since the SDK branches on them.
- **Version reporting:** send `X-Version-ID` and include `server_version` in the
  WS `hello` event with a plausible Mattermost version so neither the SDK nor
  the official client refuses the connection.

## Data model (Postgres)

Core tables, all with `create_at`/`update_at`/`delete_at bigint`:

- `users` — id, username, email, nickname, first/last name, roles, props,
  notify_props (jsonb), last_picture_update.
- `teams` — id, name (slug), display_name, type (`O`/`I`), description.
- `team_members` — (team_id, user_id), roles.
- `channels` — id, team_id (empty for DM/GM), type (`O`/`P`/`D`/`G`), name
  (slug), display_name, header, purpose, creator_id, last_post_at,
  total_msg_count.
- `channel_members` — (channel_id, user_id), roles, last_viewed_at, msg_count,
  mention_count, notify_props (jsonb; holds `mark_unread` for mute).
- `posts` — id, channel_id, user_id, root_id, message, type, props (jsonb),
  file_ids (jsonb), has_reactions, edit_at, original_id, pending_post_id.
- `reactions` — (user_id, post_id, emoji_name), create_at.
- `drafts` — (user_id, channel_id, root_id), message, file_ids, props.
- `file_info` — id, creator_id, post_id, channel_id, name, extension, size,
  mime_type, width, height, has_preview_image, mini_preview.
- `emoji` — id, creator_id, name; image bytes in the file store.
- `status` — user_id, status, manual, last_activity_at, plus custom-status in
  user props (`customStatus`).
- `sessions` — token, user_id, create_at, expires_at, props.
- `preferences` — (user_id, category, name) → value (needed mainly for the
  official client; minimal for matterbox).

Indexes that matter for the hot paths:

- `posts (channel_id, create_at DESC)` — channel pagination
  (before/after/since all key off `create_at`).
- `posts (root_id)` — thread fetch.
- `channel_members (user_id)` — bootstrap + unread badges.
- A full-text index for search (see Search).

## Auth & sessions (SSO skipped)

1. `POST /api/v4/users/login` — accept `{login_id, password}` or a
   `{token}` device login; verify against `users` (bcrypt), create a
   `sessions` row, return the token in the `Token` header + cookie and the
   `User` in the body.
2. **Token middleware** — read `Authorization: Bearer <token>` (and the
   `MMAUTHTOKEN` cookie); resolve to a session + user; attach to request
   context; `401` with a `model.AppError`-shaped body on failure.
3. `POST /api/v4/users/logout`, basic session expiry. SSO/OAuth endpoints are
   out of scope for v1 (the `mmauth://` flow in the client is replaced by plain
   login for this server).

The error body shape matters: the SDK decodes failures into `model.AppError`
(`{"id","message","status_code","request_id"}`). A small helper enforces it.

## REST surface (maps 1:1 to `internal/mm/client.go`)

Grouped by file; each line is `SDK method → METHOD path` and the client method
it backs. Confirm exact paths against the SDK.

**users.go / status.go**
- `GetMe` → `GET /users/me`
- `GetUsersByIds` → `POST /users/ids`
- `GetUserByUsername` → `GET /users/username/{username}`
- `GetUsersInChannel` → `GET /users?in_channel={id}&page&per_page`
- `AutocompleteUsersInChannel` → `GET /users/autocomplete?in_channel&in_team&name`
- `GetUsersStatusesByIds` → `POST /users/status/ids`
- `UpdateUserStatus` → `PUT /users/{id}/status`
- `UpdateUserCustomStatus` → `PUT /users/{id}/status/custom`
- `RemoveUserCustomStatus` → `DELETE /users/{id}/status/custom`

**teams.go / channels.go / members.go**
- `GetTeamsForUser` → `GET /users/{id}/teams`
- `GetChannelsForUserWithLastDeleteAt` → `GET /users/{id}/channels?last_delete_at`
- `GetChannelByNameForTeamName` → `GET /teams/name/{team}/channels/name/{channel}`
- `GetChannelMembersWithTeamData` → `GET /users/{id}/channel_members?page&per_page`
- `GetChannelMember` → `GET /channels/{id}/members/{user_id}`
- `GetUsersInChannel` (see users)
- `ViewChannel` → `POST /channels/members/{user_id}/view`
- `UpdateChannelNotifyProps` → `PUT /channels/{id}/members/{user_id}/notify_props`
- `CreateDirectChannel` → `POST /channels/direct`
- `CreateGroupChannel` → `POST /channels/group`
- `GetPinnedPosts` → `GET /channels/{id}/pinned`

**posts.go**
- `GetPostsForChannel` → `GET /channels/{id}/posts?page&per_page`
- `GetPostsAfter` / `GetPostsBefore` → same with `?after=` / `?before=`
- `GetPostsSince` → `GET /channels/{id}/posts?since=`
- `GetPostThread` → `GET /posts/{id}/thread`
- `CreatePost` → `POST /posts`
- `PatchPost` → `PUT /posts/{id}/patch`
- `DeletePost` → `DELETE /posts/{id}`

All list endpoints return `model.PostList` (`{order, posts, next_post_id,
prev_post_id}`).

**reactions.go**
- `SaveReaction` → `POST /reactions`
- `DeleteReaction` → `DELETE /users/{user_id}/posts/{post_id}/reactions/{emoji}`
- `GetReactions` → `GET /posts/{id}/reactions`

**drafts.go**
- `GetDrafts` → `GET /users/{id}/teams/{team_id}/drafts`
- `UpsertDraft` → `POST /drafts`
- `DeleteDraft` → `DELETE /users/{id}/channels/{channel_id}/drafts?root_id`

**files.go**
- `UploadFile` → `POST /files` (multipart) → `model.FileUploadResponse`
- `DownloadFile` → `GET /files/{id}`
- `DownloadFilePreview` → `GET /files/{id}/preview`
- `GetFileInfosForPost` → `GET /posts/{id}/files/info`

**emoji.go**
- `GetEmojiList` → `GET /emoji?page&per_page`
- `GetEmojisByNames` → `POST /emoji/names`
- `GetEmojiImage` → `GET /emoji/{id}/image`

**actions.go (plugin interactivity — stub to no-op for v1)**
- `DoPostAction` → `POST /posts/{id}/actions/{action_id}`
- `SubmitInteractiveDialog` → `POST /actions/dialogs/submit`

**system.go (needed for connect / official client)**
- `GET /api/v4/system/ping`
- `GET /api/v4/config/client?format=old` — minimal client config blob.

## WebSocket hub

Endpoint: `GET /api/v4/websocket` (upgrade). Protocol contract:

1. On upgrade, authenticate from the `Authorization` header / cookie. If the
   client sends an `authentication_challenge` action instead, accept the token
   from its data.
2. Immediately push the `hello` event with `connection_id` and
   `server_version`.
3. Maintain a sequence number per connection; echo `seq_reply` for actions like
   `user_typing`.

**Hub design (the performance core):**
- A central `Hub` owns a registry: `userID → set<*conn>` and per-connection
  metadata (subscribed channels derivable from membership).
- REST handlers publish typed events onto an internal `bus`; the hub fans them
  out. Broadcasts carry a `model.WebSocketEvent` with a `broadcast` scope
  (`{channel_id}` / `{user_id}` / `{team_id}` / omit-users set).
- Fan-out resolves recipients from channel membership (cached in-memory,
  invalidated on member changes) — never a DB hit per event.
- Per-connection buffered send channel with a slow-consumer drop policy; writes
  serialized by a single writer goroutine per connection.

**Events to emit** (consumed in `internal/ui/update.go:930`):
`hello`, `posted`, `post_edited`, `post_deleted`, `reaction_added`,
`reaction_removed`, `status_change`, `user_updated`, `open_dialog`, `typing`,
`multiple_channels_viewed`, `draft_created`, `draft_updated`, `draft_deleted`.

Producer mapping:
- `CreatePost` → `posted` to channel members; bump `last_post_at`, counts.
- `PatchPost`/edit → `post_edited`; `DeletePost` → `post_deleted`.
- `SaveReaction`/`DeleteReaction` → `reaction_added`/`reaction_removed`.
- `ViewChannel` → `multiple_channels_viewed` back to the acting user.
- `UpdateUserStatus`/custom status → `status_change` / `user_updated`.
- `user_typing` action in → `typing` to channel members.
- Draft endpoints → `draft_*` to the acting user's connections only.

## Search (MVP)

Keep search minimal for the initial build — enough to answer a query on the
standard routes, not a reimplementation of the client's local search:

- `POST /api/v4/teams/{team_id}/posts/search` and `POST /api/v4/posts/search`
  → `model.PostSearchResults`.

MVP implementation in Postgres:
- A `tsvector` column on `posts` with a GIN index; match with
  `plainto_tsquery`, order by recency, paginate.
- Permission-scope results to channels the requesting user is a member of.

Deliberately deferred (revisit only if needed): the full Mattermost modifier
grammar (`in:`/`from:`/`on:`/`"phrase"`/`-exclude`/wildcards), `pg_trgm` fuzzy
matching, and rank tuning. The schema leaves room to add these later without a
rewrite.

## Files & previews

- File store behind an interface: `Local` (filesystem dir) for v1; S3-compatible
  later. Path keyed by file id.
- On upload: sniff MIME, for images decode dimensions, generate a downscaled
  preview (≤~1MP JPEG/PNG) and a tiny `mini_preview` thumbnail, persist
  `file_info`.
- `/files/{id}/preview` serves the rendition; `404` when `has_preview_image` is
  false (the client relies on this).

## Performance notes

- In-memory caches for the fan-out hot path: channel-membership sets and
  user→connection registry; invalidate on membership/role changes only.
- Prepared statements via `pgx`; batch the bootstrap reads (teams + channels +
  members + statuses) the client issues on startup.
- `posts (channel_id, create_at DESC)` covers every pagination mode; keyset
  pagination on `create_at` rather than OFFSET.
- Single writer goroutine per WS connection; bounded send buffers; drop + close
  slow consumers rather than blocking the hub.

## Milestones

### M1 — vertical slice (login → read → live)
- [ ] Migrations + `store` for users, teams, channels, members, posts.
- [ ] `id` package; `AppError` helper; token middleware.
- [ ] `POST /users/login`, `GET /users/me`, teams + channels + channel_members
      bootstrap reads.
- [ ] `GET /channels/{id}/posts` (+ after/before/since), `POST /posts`.
- [ ] WS hub: hello + auth + `posted` fan-out.
- [ ] Acceptance: `matterbox` logs in, lists teams/channels, reads a channel,
      sends a message, sees it (and others') live.

### M2 — full matterbox parity
- [ ] Threads, edit, delete, pinned posts.
- [ ] Reactions (REST + WS).
- [ ] Read state / unread counts / mute (notify_props) + `multiple_channels_viewed`.
- [ ] Drafts (REST + WS).
- [ ] Files: upload/download/preview/info + image preview generation.
- [ ] Presence + custom status; `user_updated` / `status_change`.
- [ ] Custom emoji list/names/image.
- [ ] DM + group-DM creation; user autocomplete.
- [ ] Typing events.
- [ ] `actions`/`dialogs` stubbed to no-op (matterpoll-shaped requests succeed
      without effect).
- [ ] Acceptance: every method in `internal/mm/client.go` exercised against the
      server with the real client.

### M3 — search (MVP) + hardening
- [ ] Postgres `tsvector` + GIN; `plainto_tsquery` match, recency order.
- [ ] `/posts/search` + `/teams/{id}/posts/search`.
- [ ] Load/perf pass on WS fan-out and bootstrap reads; benchmarks.
- [ ] Session expiry, basic rate limiting, structured logging.

### M4 — official client reach (stretch)
- [ ] `/config/client`, preferences, sidebar categories, `/users/me/teams/unread`.
- [ ] Fill endpoints the webapp bootstrap requires; iterate against a real
      webapp build until it loads, reads, and posts.

## Testing

- **Golden round-trips:** marshal `model.*` structs to JSON, assert handler
  output matches — guards the wire contract.
- **Client-in-the-loop:** a test harness that points the repo's own `mm.Client`
  at an in-process server and drives each method (this is the real acceptance
  suite; the client is the spec).
- **WS conformance:** assert each producer emits the right event to the right
  recipients and not to excluded users.
- **Search:** fixture corpus with expected ranked ordering per modifier.
