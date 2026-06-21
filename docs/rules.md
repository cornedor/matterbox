# Rules — per-message automation for `matterbox listen`

The `matterbox listen` daemon holds a persistent WebSocket connection to your
Mattermost server and reacts to every incoming message. **Rules** make that
reaction programmable: when a message matches a set of conditions, the daemon
runs one or more actions — forward it to Telegram, run a local command, POST a
webhook, add a reaction, mark the channel read, or just log it.

Rules also have *memory*: a [`frequency`](#rate-limiting-with-frequency) window
fires only on a burst ("three sev-1s in ten minutes"), and a [persistent
ledger](#persistent-state-the-ledger) lets rules count and remember across
messages.

This is the kind of automation a server-side Mattermost plugin can't give you:
it's per-user, runs on *your* machine, and can run *your* commands. Think
procmail/Sieve, but for chat.

## Creating a rule, step by step

### 1. Open your config

Rules live under a top-level `rules:` key in your matterbox config file:

```
~/.config/matterbox/config.yaml
```

(On first run matterbox writes this file with documented defaults. If a
`rules:` key isn't there yet, just add one — see below.)

### 2. Understand the shape

A rule is a `match` (when it fires) plus an ordered list of `actions` (what it
does). The list under `rules:` is evaluated top to bottom for every incoming
message:

```yaml
rules:
  - name: my-first-rule     # optional label, shown in the daemon log
    match:                  # all conditions here must hold (AND)
      channel: "Engineering"
      message: "(?i)deploy"
    actions:                # run in order when the match passes
      - type: log
        text: "deploy mentioned"
```

`name` is optional (it just labels log lines). `match` and `actions` are the
substance. An empty `match:` matches *every* message — handy with a narrow
action, dangerous with `notify`.

### 3. Write your first rule

Start with a `log` action — it's free, synchronous, and can't spam anyone, so
it's the safest way to confirm a rule matches what you think it does. This one
logs whenever a teammate posts in a channel whose name starts with "Ops":

```yaml
rules:
  - name: watch-ops
    match:
      channel: "Ops*"
    actions:
      - type: log
        text: "ops activity"
```

### 4. Restart the daemon and check it loaded

Rules are compiled and validated when the daemon starts, so restart it after
every edit:

```sh
# Linux (systemd --user)
systemctl --user restart matterbox-listen.service
journalctl --user -u matterbox-listen -f

# macOS (launchd)
launchctl kickstart -k gui/$(id -u)/com.matterbox.listen
tail -f ~/Library/Logs/matterbox-listen.log

# or just run it in the foreground while you iterate
matterbox listen
```

The startup line reports how many rules loaded:

```
matterbox listen: cache=… two_way=true … rules=1 configured
```

If a glob, regexp, or action type is wrong, the daemon **refuses to start** and
tells you which rule and why — a typo fails loud rather than silently never
firing:

```
rules: rule "watch-ops": bad message regexp "(": error parsing regexp: …
```

Fix the config and restart.

### 5. Watch it fire, then swap in a real action

With the daemon log open, post a test message that should match. You'll see your
`log` line appear. Once the `match` is doing what you want, replace `log` with
the action you actually need — `notify`, `exec`, `webhook`, `react`, or
`mark_read` (see the [Actions](#actions) reference below):

```yaml
rules:
  - name: watch-ops
    match:
      channel: "Ops*"
      message: "(?i)sev-1|pagerduty"
    actions:
      - type: exec
        command: ["/home/me/bin/page-me.sh"]
      - type: notify
        summarize: false
    stop: true
```

> **Heads up:** the moment you add *any* `rules:`, the built-in mention/DM
> Telegram bridge is no longer applied — your list is the whole policy. If you
> still want mention notifications, include a `notify` rule yourself (see
> [The notification bridge is just a rule](#the-notification-bridge-is-just-a-rule)).

### 6. Test exec / webhook safely

`exec` and `webhook` receive the message as a JSON envelope (and `exec` also as
`MATTERBOX_*` env vars). To see exactly what your script receives, point it at a
throwaway command first:

```yaml
    actions:
      - type: exec
        command: ["sh", "-c", "cat >> /tmp/matterbox-rule.log"]
```

Post a matching message, then inspect `/tmp/matterbox-rule.log` to see the
envelope. The [exec / webhook payload](#the-exec--webhook-payload) section
documents every field.

You can also exercise a rule against your own messages without involving anyone
else: run the daemon with `--notify-self` and post in your self-DM.

### Authoring tips

- **Order matters.** Rules fire top to bottom; put specific rules first and use
  `stop: true` on a rule that should be the last word for a message.
- **Conditions are ANDed.** Add fields to narrow; remove fields to widen.
  Different *fields* are ANDed; within `channel` or `author` a **list** is ORed
  (`channel: [ops, "eng-*"]` matches either). For "everything except…", nest a
  `not:` block.
- **`channel` matches the display name**, not `team/channel` — use the name as
  it appears in the sidebar (globbing with `*`/`?`), or paste an exact channel
  id. It accepts a single value or a list.
- **Use `(?i)`** at the start of a `message` regexp for case-insensitive
  matching.
- **Iterate with `log`**, promote to real actions once the match is right.

## The notification bridge is just a rule

The daemon's original behaviour — *"on a direct @mention or DM, summarise the
surrounding conversation and forward it to Telegram"* — is no longer special.
When you have **no** `rules:` block configured, the daemon synthesises one
default rule from the `listen.*` options that reproduces it exactly:

```yaml
# The built-in default, written out as a rule. You don't need this in your
# config — it's what you get when `rules:` is empty — but it shows the shape.
rules:
  - name: notify-mentions-and-dms
    match:
      mention: true       # you were directly @named in a channel …
    actions:
      - type: notify      # … so summarise + push to Telegram
  - name: notify-dms
    match:
      dm: true            # any direct message
    actions:
      - type: notify
```

The moment you add your own `rules:`, the default is **not** applied — your
rules are the whole policy. So if you write custom rules and still want mention
notifications, include a `notify` rule yourself (copy the block above).

The `notify` action always honours the daemon's do-not-disturb settings —
`respect_mutes`, `quiet_hours`, and `notify_dms` — and the per-channel
read-check (`notify_delay_seconds`), exactly as the built-in bridge does. A rule
that must page through quiet hours and mutes can opt out with `urgent: true`
(the self/DM gates still apply); see [Actions](#actions).

### Catch-up after a reconnect

When the daemon (re)connects it sends a single "📥 While you were away" digest
of the mentions and DMs that arrived while it was offline. That digest is now
filtered through your rules: a message is only included if a rule with a
`notify` action matches it, so narrowing or routing your notifications narrows
the catch-up too. Two deliberate bounds: the catch-up only considers unread
**mentions and DMs** (the set the server lets the daemon query cheaply on
reconnect, not every unread post), and it only drives `notify` — `exec`,
`webhook`, `react`, and the `state_*` ledger actions are **live-only** and never
re-fire for historical messages, so a reconnect can't replay side effects or
double-count a counter. The `frequency` window is likewise live-only. The digest always goes to
the default `telegram.chat_id` (per-rule `chat_id` applies to live messages).

## How rules are evaluated

For each incoming message (system messages, deletions, and empty bodies are
skipped) the daemon walks the `rules:` list top to bottom. Every rule whose
`match` passes runs its `actions`. A matching rule with `stop: true` ends
evaluation — no later rule runs for that message.

## Conditions (`match`)

Different fields are ANDed; an empty `match` matches every message.

| Field | Meaning |
|---|---|
| `channel` | Case-insensitive glob (`*`, `?`) over the channel's **display name**, or an exact channel id. A single value or a list (matches **any**). |
| `author` | Username (no leading `@`), matched case-insensitively. A single value or a list (matches **any**). |
| `message` | [RE2](https://github.com/google/re2/wiki/Syntax) regexp over the body. Prefix `(?i)` for case-insensitive. |
| `mention` | `true` requires you were directly @named (the same test the bridge uses). |
| `dm` | `true` = only direct messages; `false` = only channels; unset = either. |
| `has_file` | `true` requires at least one attachment. |
| `is_thread` | `true` = only thread replies; `false` = only root posts; unset = either. |
| `not` | A nested `match` block that **inverts**: the rule fires only when the post does **not** satisfy it. Recursive. |
| `frequency` | A rolling-window threshold: even when the fields match, fire only on a **burst**. See below. |
| `state` | Match on the persistent [ledger](#matching-on-the-ledger) — e.g. `failure_count` is at least 3. |

`channel`/`author` as a list is an OR; combine with `not` to subtract. For
example, "anything in the ops channels except from the bots":

```yaml
match:
  channel: ["Ops*", "Incidents"]
  not:
    author: [deploybot, alertmanager]
```

### Rate limiting with `frequency`

The fields above answer *"does this one message match?"* — they're stateless.
`frequency` adds memory: it counts how often the rest of the `match` has held
recently, and fires the rule's actions only once that count crosses a threshold
within a rolling window. The classic use is *"don't page me for one sev-1, but
do if there are three in ten minutes"*:

```yaml
rules:
  - name: sev1-storm
    match:
      channel: "Ops Alerts"
      message: "(?i)sev-1"
      frequency:
        count: 3        # fire once this many matches …
        within: 10m     # … land inside this rolling window
        by: author      # optional: count separately per author (or channel)
    actions:
      - type: notify
        urgent: true
```

| Field | Meaning |
|---|---|
| `count` | How many matches within the window trigger the rule (≥ 1). |
| `within` | The window length, as a Go duration: `10m`, `1h30m`, `45s`. |
| `by` | Count separately per `author`, per `channel`, or together (`global`, the default). |

**Semantics.** Every matching message records a hit in a sliding window;
hits older than `within` fall out. The rule fires on the message that fills the
window to `count`, and the window then **resets** — so it re-arms only after
another full burst, rather than firing again on the 4th, 5th, … message. With
`by: author`, each author has an independent window (one noisy author can't trip
a threshold meant for the room as a whole); `by: channel` does the same per
channel.

The window is **in-memory and live-only**: it starts empty on every (re)start,
and the [reconnect catch-up](#catch-up-after-a-reconnect) never feeds historical
messages through it. A frequency gate is therefore best for "happening right
now" bursts, not for thresholds that must survive a restart — for that, count
into the [persistent ledger](#persistent-state-the-ledger) instead.

## Actions

| `type` | Fields | What it does |
|---|---|---|
| `notify` | `summarize`, `urgent`, `chat_id` (all optional) | Summarise + deliver to Telegram. `summarize` overrides `listen.summarize`; `urgent: true` delivers even during quiet hours / for muted channels; `chat_id` routes to a different Telegram chat. |
| `exec` | `command` (argv list) | Run a local command. The message is piped to its **stdin as JSON** and exported as `MATTERBOX_*` env vars. 30s timeout. |
| `webhook` | `url`, `headers` (optional map) | HTTP `POST` the message envelope as a JSON body. `headers` adds request headers; values are expanded from the daemon's environment (`$TOKEN` / `${TOKEN}`). 15s timeout. |
| `react` | `emoji` (shortcode) | Add an emoji reaction to the message. |
| `mark_read` | — | Mark the message's channel read. |
| `log` | `text` (optional prefix) | Write a line to the daemon log. |
| `state_set` | `key`, `value` | Write `value` into the persistent ledger under `key`. Both are templates (see below). |
| `state_incr` | `key`, `by` (optional, default 1) | Add `by` to the integer stored at `key` (a missing/non-numeric value counts as 0). Negative decrements. |
| `state_del` | `key` | Remove `key` from the ledger. |

`urgent` bypasses only the do-not-disturb suppression (`quiet_hours`,
`respect_mutes`); the self and `notify_dms` gates still apply. A rule that routes
to a non-default `chat_id` is delivery-only — the two-way reply buttons work just
for the configured `telegram.chat_id`.

### The exec / webhook payload

Both `exec` (on stdin) and `webhook` (as the POST body) receive the same flat
JSON envelope. Fields are only ever **added**, never renamed or removed, so a
script can depend on them:

```json
{
  "post_id": "abc123",
  "channel_id": "xyz789",
  "channel": "Engineering",
  "team_id": "t1",
  "team": "core",
  "author": "bob",
  "message": "deploying now",
  "is_dm": false,
  "is_thread": false,
  "root_id": "",
  "mentioned": true,
  "files": ["diagram.png"],
  "create_at": 1700000000000,
  "permalink": "https://mm.example.com/core/pl/abc123",
  "state": { "failure_count": "3", "last_failure_time": "1700000000000" }
}
```

`team`/`team_id`, `root_id`, and `permalink` are omitted when empty; `mentioned`
is whether *you* were @named; `files` lists attachment names (present only when
the post carries file metadata); `state` is a snapshot of the [persistent
ledger](#persistent-state-the-ledger) (omitted when empty).

`exec` additionally exports each scalar field as an environment variable so a
quick script needn't parse JSON: `MATTERBOX_POST_ID`, `MATTERBOX_CHANNEL_ID`,
`MATTERBOX_CHANNEL`, `MATTERBOX_TEAM_ID`, `MATTERBOX_TEAM`, `MATTERBOX_AUTHOR`,
`MATTERBOX_MESSAGE`, `MATTERBOX_IS_DM`, `MATTERBOX_IS_THREAD`,
`MATTERBOX_ROOT_ID`, `MATTERBOX_MENTIONED`, `MATTERBOX_FILES` (comma-separated),
and `MATTERBOX_PERMALINK`. The ledger is exported as `MATTERBOX_STATE` (the whole
map as JSON) plus one `MATTERBOX_STATE_<KEY>` per entry (the key upper-cased,
non-alphanumerics collapsed to `_`), e.g. `MATTERBOX_STATE_FAILURE_COUNT=3`.

## Persistent state (the ledger)

The `match` conditions and most actions are stateless — each message is judged
on its own. The **ledger** breaks that: a small key/value store, persisted in
the message database (`rule_state` table) and therefore surviving restarts, that
rules read and write. It's how a rule carries context from one message to the
next — a running failure count, the timestamp of the last deploy, a flag that a
later rule checks.

Three actions write it:

```yaml
rules:
  - name: track-deploy-failures
    match:
      author: deploybot
      message: "(?i)failed"
    actions:
      - type: state_set
        key: last_failure_time
        value: "{{ .create_at }}"
      - type: state_incr
        key: failure_count          # 0 → 1 → 2 … (created on first use)
  - name: clear-on-success
    match:
      author: deploybot
      message: "(?i)succeeded"
    actions:
      - type: state_del
        key: failure_count          # reset the counter on a green deploy
```

### Templating keys and values

`state_set`/`state_incr`/`state_del` `key`, and `state_set` `value`, are
[Go `text/template`](https://pkg.go.dev/text/template) strings expanded against
the message. The data is exactly the [exec/webhook envelope](#the-exec--webhook-payload)
above — so `{{ .author }}`, `{{ .channel }}`, `{{ .create_at }}`, `{{ .message }}`,
`{{ .is_dm }}`, … all work — **plus** the current ledger under `.state`, so a
template can read a value another action just wrote:

```yaml
actions:
  - type: state_incr
    key: deploy_failures
  # state_* actions run in order, so this sees the value the incr just stored.
  - type: state_set
    key: last_failure_count
    value: "deploy #{{ .state.deploy_failures }} failed"
```

A per-message **key** can vary too — `key: "failures:{{ .author }}"` keeps an
independent counter per author. Note that `.state.NAME` only works for keys that
are valid template identifiers; to read a key built from a template (e.g.
`failures:bob`) use the `index` function: `{{ index .state "failures:bob" }}`.
A field or `.state` key that doesn't exist renders as empty, not an error.

### How state flows to other actions

- **Order is guaranteed.** Within one rule, `state_*` actions run synchronously
  in the order written, *before* the `exec`/`webhook` actions that follow them
  are dispatched — so a script invoked later in the same rule sees the values
  this rule just wrote.
- **`exec` and `webhook`** receive the whole ledger: in the JSON envelope under
  `state`, and `exec` also as `MATTERBOX_STATE` / `MATTERBOX_STATE_<KEY>` env
  vars (see above).
- **`state_incr` is atomic**, so two messages updating the same counter at once
  can't lose a write.

### Matching on the ledger

Writing the ledger is only half of it — a `match.state` condition reads it, so
one rule can react to what another counted. Each condition names a `key` and one
or more operators, all ANDed:

| Operator | Meaning |
|---|---|
| `exists` | `true` = key is present; `false` = key is absent. |
| `eq` / `ne` | Value equals / doesn't equal this (string compare). |
| `gt` / `gte` / `lt` / `lte` | Value, **as a number**, compared to this (a range is `gte` + `lt`). |

`state` takes a single condition or a list (all ANDed). The `key` is a
**template** over the post, just like the action keys — so a condition can read a
per-channel or per-author key, e.g. `key: "hot:{{ .channel_id }}"`. The canonical
pairing — count failures in one rule, page when the count crosses a threshold in
another:

```yaml
rules:
  - name: count-failures
    match: { author: deploybot, message: "(?i)failed" }
    actions:
      - type: state_incr
        key: failures
  - name: page-on-streak
    match:
      state:
        key: failures
        gte: 3            # ← reads what count-failures just wrote
    actions:
      - type: notify
        urgent: true
      - type: state_del
        key: failures      # reset so the next streak starts clean
  - name: clear-on-success
    match: { author: deploybot, message: "(?i)succeeded" }
    actions:
      - type: state_del
        key: failures
```

**Same-message visibility.** Within one incoming message the ledger is re-read
after any rule that wrote it, so `page-on-streak` sees the `failures` value
`count-failures` just incremented — the page fires on the *third* failure, not
the fourth. An absent key satisfies `exists: false` but no value comparison
(there's nothing to compare); a non-numeric value never satisfies a numeric
operator.

This overlaps with [`frequency`](#rate-limiting-with-frequency) but trades off
differently: `frequency` is in-memory and self-pruning (great for "N in the last
T minutes"), while a ledger counter is exact, persistent across restarts, and
yours to reset — better when the threshold must survive a restart or the count
is cleared by an explicit event (a green deploy) rather than by time.

### Correlating across messages

Because a state condition's `key` is templated per post, the ledger can remember
context about *one conversation* and react to it several messages later — a
correlation no single-message `match` (term **and** mention in the same message)
or `frequency` window (same rule's own hits) can express. The pattern is a
per-channel **countdown**: a trigger term arms a window, every message ticks it
down, and a mention while the window is open escalates. "Was a sev-1 term posted
in the last ~5 messages, and then I got @-mentioned?":

```yaml
rules:
  # 1) ESCALATE: I'm mentioned while this channel is still "hot".
  - name: hot-mention
    match:
      mention: true
      state: { key: "hot:{{ .channel_id }}", gte: 1 }
    actions:
      - type: notify
        urgent: true

  # 2) TICK: every message in a hot channel counts the window down by one.
  - name: tick-hot
    match:
      state: { key: "hot:{{ .channel_id }}", gte: 1 }
    actions:
      - type: state_incr
        key: "hot:{{ .channel_id }}"
        by: -1

  # 3) ARM: a trigger term opens a ~5-message window for this channel.
  - name: arm-hot
    match:
      message: "(?i)urgent|sev-?1|p1|outage|prod(uction)? down"
    actions:
      - type: state_set
        key: "hot:{{ .channel_id }}"
        value: "5"

  # 4) NORMAL: an ordinary mention (channel not hot) → regular notification.
  - name: mention
    match:
      mention: true
      not: { state: { key: "hot:{{ .channel_id }}", gte: 1 } }
    actions:
      - type: notify
```

Rules 1 and 4 are mutually exclusive (the `not:` in rule 4), so a mention fires
*either* the urgent path *or* the normal one, never both — and an ordinary
mention in a channel that never saw a term still notifies (an absent `hot:` key
fails `gte: 1`, so `not:` lets rule 4 through). The window is **per channel** —
a term in `#ops` can't escalate a mention in `#random`. Rule order matters: the
tick runs before the arm so the arming message itself doesn't consume the window.

### Live-only, like side effects

State actions only run for **live** messages. The [reconnect
catch-up](#catch-up-after-a-reconnect) drives `notify` only — it never replays
`state_set`/`state_incr`/`state_del` (nor `exec`/`webhook`/`react`), so a
reconnect can't double-count a failure or re-fire a side effect for history.
(`state` *match* conditions are evaluated in catch-up against the current ledger,
but since the historical messages don't mutate it, this just reflects "does this
rule's threshold currently hold".)

## Examples

Page yourself for Sev-1 alerts and stop there:

```yaml
rules:
  - name: pager
    match:
      channel: "Ops Alerts"
      message: "(?i)sev-1|pagerduty"
    actions:
      - type: exec
        command: ["/home/me/bin/page-me.sh"]
      - type: notify
        summarize: false
    stop: true
```

Desktop notification for direct mentions (Linux):

```yaml
rules:
  - name: desktop
    match: { mention: true }
    actions:
      - type: exec
        command: ["notify-send", "Mattermost mention"]
```

Auto-react with 👀 when a bot drops a deploy notice, and mark it read:

```yaml
rules:
  - name: ack-deploys
    match:
      author: deploybot
      message: "(?i)deployed"
    actions:
      - type: react
        emoji: eyes
      - type: mark_read
```

Page through quiet hours when on-call is summoned, and route it to a separate
chat:

```yaml
rules:
  - name: oncall
    match:
      channel: ["Ops*", "Incidents"]
      message: "(?i)@oncall|sev-1"
      not:
        author: [statusbot]
    actions:
      - type: notify
        urgent: true          # ignore quiet_hours + mutes
        chat_id: "-1001234567" # a dedicated on-call chat
        summarize: false
    stop: true
```

Mirror everything in a channel to an authenticated external system (the token
comes from the daemon's environment, not the config file):

```yaml
rules:
  - name: archive-incidents
    match: { channel: "Incidents" }
    actions:
      - type: webhook
        url: https://hooks.example.com/incidents
        headers:
          Authorization: "Bearer ${INCIDENTS_TOKEN}"
```

Escalate only on a deploy-failure streak — count failures in the ledger, and
when the count reaches three, run a script with the count in its environment and
reset it:

```yaml
rules:
  - name: count-failures
    match: { author: deploybot, message: "(?i)failed" }
    actions:
      - type: state_incr
        key: deploy_failures
  - name: clear-failures
    match: { author: deploybot, message: "(?i)succeeded" }
    actions:
      - type: state_del
        key: deploy_failures
  - name: escalate
    match:
      author: deploybot
      message: "(?i)failed"
      frequency: { count: 3, within: 30m }   # three failures in half an hour
    actions:
      - type: exec
        command: ["/home/me/bin/escalate.sh"]   # reads $MATTERBOX_STATE_DEPLOY_FAILURES
      - type: state_del
        key: deploy_failures
```

## Safety

`exec` runs commands from your own config, as you, on your own machine — same
trust level as a shell alias. Each run is bounded by a timeout and runs off the
ingest path, so a slow or hung command can't block message caching or take the
daemon down. A bad glob, regexp, **template, `frequency` duration, or `state` condition**
(missing key / no operator), or an unknown action `type`, is reported at startup
so a typo fails loud rather than silently never firing. `state_*` writes touch only matterbox's own database
(the `rule_state` table), never your Mattermost server.
