# The message database

matterbox keeps every message it has ever shown you in a local SQLite database.
The SQL tab is a read-only editor over it.

The database lives at `~/.config/matterbox/messages.db`, and the **SQL tab**
points arbitrary `SELECT`s at your own chat history — from "show me the last 50
posts" to aggregates, full-text search, and digging into the raw Mattermost
JSON. What follows is the schema and a pile of copy-pasteable queries.

## Opening the SQL tab

The tab is **hidden by default**. Turn it on by setting `sql_tab: true` in
`~/.config/matterbox/config.yaml`, then restart matterbox.

```yaml
sql_tab: true
```

Once enabled it is the yellow **SQL** tab in the tab strip, next to **Feed** and
**Search**. Jump to it the same way you switch teams — the nav modifier plus
arrows, the team jump keys, or just click it. Then:

- Type a query and press **Enter** to run it. (`alt+↵` / `shift+↵` inserts a
  newline, so multi-line SQL works.)
- Results render as chat messages — see
  [How results are rendered](#how-results-are-rendered).
- When a query returns rows, focus drops into the result list. Use the arrow
  keys to move the selection; the read-only message actions work on the selected
  row (open, preview or download attachments, copy markdown, copy code blocks)
  exactly as they do in a channel.
- **Esc** clears the results, and a second Esc leaves the editor; **i** or Esc
  hops back up to the editor to refine the query.

### It is strictly read-only

The tab runs every query on a separate SQLite handle opened with
`PRAGMA query_only`, so **nothing you type can modify the cache** — writes fail
with *"attempt to write a readonly database"* rather than touching your data. As
a friendly shortcut, queries that *start* with a write verb (`INSERT`, `UPDATE`,
`DELETE`, `DROP`, `CREATE`, `ALTER`, `REPLACE`, `TRUNCATE`, `VACUUM`, …) are
rejected up front with an explanation. You get `SELECT`, `EXPLAIN`, `PRAGMA`,
and `WITH … SELECT`.

Two other guardrails:

- **Row cap:** at most **1000 rows** are pulled into the view. If a query
  matches more, the tab tells you the result was clipped — add a tighter `WHERE`
  or an aggregate.
- **Timeout:** a single query is cancelled after **20 seconds**, so a runaway
  scan cannot wedge the tab.

### How results are rendered

Each result row is drawn as a chat message. If the row carries the usual post
columns (`message`, `user_id`, `channel_id`, `create_at`, …) you get a
normal-looking message: the markdown body, attachments and reactions, with the
author prefixed by its `Team › #channel` (or `DMs › @user`) breadcrumb and a
timestamp.

Any **other** selected column — an aggregate count, a computed value, a
`json_extract` — is appended as a dim `name=value` line under the row. So:

- `SELECT * FROM posts …` looks like a chat transcript.
- `SELECT channel_id, COUNT(*) AS n FROM posts GROUP BY channel_id` shows each
  channel breadcrumb with `n=…` beside it.

> **Names are resolved for display only.** The breadcrumb (`Team › #channel`,
> `@user`) is filled in by the TUI from the teams and channels it currently has
> loaded. The database itself stores only opaque 26-character IDs — there is
> **no users table and no channels table** (see [What's not
> here](#whats-not-here)). So a row for a channel you have never opened falls
> back to a short id, and you cannot `JOIN` on a human name in SQL.

## Schema overview

| Table | What's in it |
|---|---|
| `posts` | Every cached message. This is the table you want 99% of the time. |
| `posts_fts` | FTS5 full-text index over `posts.message`, kept in sync by triggers. |
| `post_revisions` | Prior versions of edited posts that matterbox happened to observe. |
| `post_vectors` | Semantic-search embeddings, one per post. Binary; not useful in raw SQL. |
| `rule_state` | The [rules ledger](rules.md#persistent-state-the-ledger) — the key/value store rules read and write. |
| `meta` | Tiny key/value store for the `listen` daemon, e.g. its catch-up cursor. |
| `notif_targets` | Maps a sent Telegram notification back to its Mattermost post. |

### The `posts` table

```sql
id          TEXT PRIMARY KEY     -- 26-char Mattermost post id
channel_id  TEXT                 -- 26-char channel id (no names table; see below)
user_id     TEXT                 -- 26-char author id ('' for some system posts)
root_id     TEXT                 -- '' for a root post; the parent's id for a thread reply
create_at   INTEGER              -- creation time, UNIX MILLISECONDS (not seconds!)
update_at   INTEGER              -- last server-side update, unix-ms
edit_at     INTEGER              -- last edit, unix-ms (0 if never edited)
delete_at   INTEGER              -- 0 = live; non-zero = soft-deleted (unix-ms of deletion)
message     TEXT                 -- the markdown body (also the FTS source)
raw_json    BLOB                 -- the FULL Mattermost post object as JSON (see below)
```

Indexes: `(channel_id, create_at)` and `(root_id)`. Queries that filter or sort
on those stay fast; everything else is a table scan over a few hundred thousand
rows — still sub-second, but mind the 20 s cap on heavy `json_extract` scans.

Four conventions matter for almost every query:

1. **Timestamps are UNIX MILLISECONDS.** Divide by 1000 before handing them to
   SQLite's date functions:
   `datetime(create_at/1000, 'unixepoch', 'localtime')`. To go the other way — a
   cutoff for a `WHERE` — multiply: `strftime('%s','now','-7 days') * 1000`.
2. **`delete_at = 0` means "not deleted."** Add `WHERE delete_at = 0` unless you
   specifically want deleted posts.
3. **`root_id = ''` is a top-level post**; a non-empty `root_id` is a thread
   reply pointing at its parent post's id.
4. **`message` vs `raw_json`.** `message` is the plain markdown text, and what
   FTS indexes. `raw_json` is the entire serialized post — use `json_extract` to
   reach anything the column set does not expose.

### Digging into `raw_json`

`raw_json` is the complete Mattermost post object, stored as JSON. SQLite's JSON
functions work on it directly. Useful paths, all verified against a real cache:

| Path | Meaning |
|---|---|
| `$.type` | `''` for a normal message; otherwise a system type (`system_join_channel`, `system_header_change`, `me`, …). |
| `$.is_pinned` | `1` if the post is pinned to its channel. |
| `$.has_reactions` | `1` if anyone reacted. |
| `$.reply_count` | Number of replies in this post's thread (on the root post). |
| `$.file_ids` | JSON array of attached file ids. |
| `$.metadata.reactions` | JSON array of reaction objects (`emoji_name`, `user_id`, …). |
| `$.metadata.files` | JSON array of file metadata (name, size, mime type, …). |
| `$.props` | Webhook, bot and system properties — e.g. bot attachments, override username. |
| `$.hashtags` | Space-separated hashtags found in the message. |

`json_extract(raw_json, '$.type')`, `json_each(raw_json, '$.file_ids')` and
`json_array_length(…)` are the workhorses — there are examples below.

### The other tables

- **`posts_fts`** — an FTS5 index over `posts.message`, using a *Porter*-stemmed
  `unicode61` tokenizer. Stemming means `MATCH 'deploy'` also matches `deployed`
  and `deployment`, in both directions. Query it by joining back to `posts` on
  the shared `rowid`, and rank with `bm25(posts_fts)` — smaller is more
  relevant. You never write to it directly; triggers keep it in sync.
- **`post_revisions`** — when matterbox sees a post change (a WebSocket edit
  event, or an `edit_at` that advanced between fetches) it archives the *old*
  version here: `post_id`, `channel_id`, `user_id`, `edit_at`, `update_at`,
  `captured_at`, `message`, `raw_json`. This only contains edits matterbox
  actually witnessed — Mattermost's API does not expose full edit history.
- **`post_vectors`** — `post_id`, `vec` (an int8-quantized embedding BLOB),
  `dim`, `model`, `created_at`. The vectors are binary and meant for the in-app
  semantic search; about the only useful SQL here is coverage counting: how many
  posts are embedded, under which model.
- **`rule_state`** — where the
  [rules ledger](rules.md#persistent-state-the-ledger) lives. Readable here, but
  `matterbox rules state` is the tool for it.
- **`meta`** and **`notif_targets`** — internal bookkeeping for the `listen`
  daemon; rarely interesting from the SQL tab.

### What's *not* here

This database stores **posts only**. There is no users table, no channels table,
no teams table, no membership or read-state table. So:

- You **cannot** translate a `user_id` or `channel_id` into a name *in SQL* —
  only the running TUI can, and it does so when it renders a result row.
- Read/unread state, channel membership and team metadata live elsewhere — in
  memory and in `channel_stats.json` — not in this database.

## Example queries

Paste any of these into the SQL tab. They all assume the read-only, 1000-row,
20-second environment described above.

### Browsing

```sql
-- The 50 most recent messages, everywhere
SELECT * FROM posts
WHERE delete_at = 0
ORDER BY create_at DESC
LIMIT 50;
```

```sql
-- Everything in one channel (grab the channel_id from any rendered row first,
-- or from a COUNT-by-channel query below)
SELECT * FROM posts
WHERE channel_id = 'PASTE_CHANNEL_ID' AND delete_at = 0
ORDER BY create_at DESC
LIMIT 100;
```

```sql
-- A specific person's recent posts (channel_id/user_id are visible as the
-- breadcrumb; copy the user_id from a `SELECT user_id …` aggregate)
SELECT * FROM posts
WHERE user_id = 'PASTE_USER_ID' AND delete_at = 0
ORDER BY create_at DESC
LIMIT 50;
```

### Searching text

```sql
-- Quick substring match (case-insensitive, no stemming). Fine for ad-hoc lookups.
SELECT * FROM posts
WHERE message LIKE '%deploy%' AND delete_at = 0
ORDER BY create_at DESC
LIMIT 50;
```

```sql
-- Full-text search, ranked by relevance. Stemmed, so 'deploy' also finds
-- 'deployed'/'deployment'. bm25 is smaller-is-better.
SELECT bm25(posts_fts) AS rank, p.*
FROM posts_fts
JOIN posts p ON p.rowid = posts_fts.rowid
WHERE posts_fts MATCH 'deploy'
  AND p.delete_at = 0
ORDER BY rank
LIMIT 50;
```

```sql
-- FTS phrase + boolean operators (AND/OR/NOT, prefix with *)
SELECT p.* FROM posts_fts
JOIN posts p ON p.rowid = posts_fts.rowid
WHERE posts_fts MATCH '"merge request" AND gitlab NOT draft'
  AND p.delete_at = 0
ORDER BY p.create_at DESC
LIMIT 50;
```

### Time ranges

```sql
-- Posts from the last 7 days (note the *1000 to reach unix-ms)
SELECT * FROM posts
WHERE delete_at = 0
  AND create_at > (strftime('%s','now','-7 days') * 1000)
ORDER BY create_at DESC;
```

```sql
-- How many messages per day over the last fortnight
SELECT date(create_at/1000, 'unixepoch', 'localtime') AS day,
       COUNT(*) AS messages
FROM posts
WHERE delete_at = 0
  AND create_at > (strftime('%s','now','-14 days') * 1000)
GROUP BY day
ORDER BY day DESC;
```

```sql
-- Busiest hour of the day (your local time)
SELECT strftime('%H', create_at/1000, 'unixepoch', 'localtime') AS hour,
       COUNT(*) AS messages
FROM posts
WHERE delete_at = 0
GROUP BY hour
ORDER BY messages DESC;
```

### Aggregates & stats

```sql
-- Which channels have the most messages cached
SELECT channel_id, COUNT(*) AS n
FROM posts
WHERE delete_at = 0
GROUP BY channel_id
ORDER BY n DESC
LIMIT 25;
```

```sql
-- Most prolific authors
SELECT user_id, COUNT(*) AS n
FROM posts
WHERE delete_at = 0 AND user_id <> ''
GROUP BY user_id
ORDER BY n DESC
LIMIT 25;
```

```sql
-- Cache overview: total posts, oldest and newest message
SELECT COUNT(*) AS posts,
       datetime(MIN(create_at)/1000, 'unixepoch', 'localtime') AS oldest,
       datetime(MAX(create_at)/1000, 'unixepoch', 'localtime') AS newest
FROM posts
WHERE delete_at = 0;
```

### Threads

```sql
-- The most-replied threads (reply_count lives on the root post)
SELECT json_extract(raw_json, '$.reply_count') AS replies, *
FROM posts
WHERE root_id = '' AND delete_at = 0
ORDER BY replies DESC
LIMIT 25;
```

```sql
-- Every post in one thread, oldest first (root id = the thread's root post id)
SELECT * FROM posts
WHERE (id = 'PASTE_ROOT_ID' OR root_id = 'PASTE_ROOT_ID')
  AND delete_at = 0
ORDER BY create_at ASC;
```

### Reactions, pins, attachments

```sql
-- Most-used emoji reactions across the cache
SELECT json_extract(r.value, '$.emoji_name') AS emoji, COUNT(*) AS n
FROM posts, json_each(posts.raw_json, '$.metadata.reactions') AS r
WHERE delete_at = 0
GROUP BY emoji
ORDER BY n DESC
LIMIT 20;
```

```sql
-- Your most-reacted-to messages
SELECT json_array_length(json_extract(raw_json, '$.metadata.reactions')) AS reactions, *
FROM posts
WHERE json_extract(raw_json, '$.has_reactions') = 1 AND delete_at = 0
ORDER BY reactions DESC
LIMIT 25;
```

```sql
-- Pinned posts
SELECT * FROM posts
WHERE json_extract(raw_json, '$.is_pinned') = 1 AND delete_at = 0
ORDER BY create_at DESC;
```

```sql
-- Posts that carry file attachments (select the row, then use the download /
-- preview action keys on it)
SELECT json_array_length(json_extract(raw_json, '$.file_ids')) AS files, *
FROM posts
WHERE json_array_length(json_extract(raw_json, '$.file_ids')) > 0
  AND delete_at = 0
ORDER BY create_at DESC
LIMIT 50;
```

### System messages & edits

```sql
-- Filter OUT the join/leave/header-change noise (keep only real messages)
SELECT * FROM posts
WHERE json_extract(raw_json, '$.type') = ''   -- '' = a normal user message
  AND delete_at = 0
ORDER BY create_at DESC
LIMIT 50;
```

```sql
-- Breakdown of message types in the cache
SELECT COALESCE(NULLIF(json_extract(raw_json, '$.type'), ''), '(normal)') AS type,
       COUNT(*) AS n
FROM posts
GROUP BY type
ORDER BY n DESC;
```

```sql
-- Posts that have been edited, newest edit first
SELECT edit_at, * FROM posts
WHERE edit_at > 0 AND delete_at = 0
ORDER BY edit_at DESC
LIMIT 50;
```

```sql
-- The recorded edit history of one post (only versions matterbox observed)
SELECT datetime(edit_at/1000, 'unixepoch', 'localtime') AS edited_at, message
FROM post_revisions
WHERE post_id = 'PASTE_POST_ID'
ORDER BY edit_at ASC;
```

### Semantic-search coverage

```sql
-- How many posts are embedded, and under which model
SELECT model, COUNT(*) AS vectors, MAX(dim) AS dim
FROM post_vectors
GROUP BY model;
```

```sql
-- Messages still missing an embedding (candidates for `matterbox embed`)
SELECT COUNT(*) AS missing
FROM posts p
LEFT JOIN post_vectors v ON v.post_id = p.id
WHERE v.post_id IS NULL AND p.delete_at = 0 AND p.message <> '';
```

## Tips

- **See the query plan** without running the whole thing:
  `EXPLAIN QUERY PLAN SELECT … ;` — a `SEARCH … USING INDEX` is good; a `SCAN`
  over `posts` plus a per-row `json_extract` is the slow shape to watch for
  under the 20 s cap.
- **Lean on the indexes.** Filtering by `channel_id` (and sorting by
  `create_at`) or by `root_id` uses an index; filtering by a `json_extract`
  value does not.
- **Copy IDs out of rendered rows.** There is no name lookup in SQL, so the
  usual flow is: run a broad query, read the breadcrumbs the TUI rendered, then
  paste the `channel_id` / `user_id` / post `id` you care about into a follow-up
  query.
- **Mind the 1000-row clip.** If the tab says the result was truncated, you are
  seeing the first 1000 rows in whatever order you asked for — add `LIMIT`,
  tighten the `WHERE`, or aggregate.
