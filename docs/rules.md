# Rules — event automation for `matterbox listen`

The `listen` daemon holds a persistent WebSocket connection to your Mattermost
server and reacts to what happens on it. Rules make that reaction programmable.

When something matches a set of conditions, the daemon runs one or more actions
— forward it to Telegram, run a local command, POST a webhook, post a message
back, add a reaction, mark the channel read, or just log it.

A rule reacts to a new message by default, but [`on:`](#triggers-on) widens that
to edits, deletions and reactions — and to the clock, so "post the standup
prompt at 09:00 on weekdays" is a rule rather than a separate cron job with its
own copy of your config.

Rules also have *memory*: a [`frequency`](#rate-limiting-with-frequency) window
fires only on a burst ("three sev-1s in ten minutes"), and a
[persistent ledger](#persistent-state-the-ledger) lets rules count and remember
across messages.

This is the kind of automation a server-side Mattermost plugin cannot give you:
it is per-user, it runs on *your* machine, and it can run *your* commands. Think
procmail or Sieve, but for chat.

## Creating a rule, step by step

Rules live under a top-level `rules:` key in
`~/.config/matterbox/config.yaml`. On first run matterbox writes that file with
documented defaults; if a `rules:` key is not there yet, just add one.

### The shape of a rule

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

`name` is optional — it only labels log lines. `match` and `actions` are the
substance. An empty `match:` matches *every* message: handy with a narrow
action, dangerous with `notify`.

### Your first rule

Start with a `log` action — it is free, synchronous, and cannot spam anyone, so
it is the safest way to confirm a rule matches what you think it does. This one
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

### Loading it, and checking what loaded

Rules are compiled and validated when they load, so tell the running daemon to
re-read them after every edit. A reload swaps the ruleset in place, without
dropping the connection or re-running catch-up:

```sh
# Linux (systemd --user)
systemctl --user reload matterbox-listen.service
journalctl --user -u matterbox-listen -f

# anywhere, by signal
pkill -HUP -f "matterbox listen"

# macOS (launchd)
launchctl kill -HUP gui/$(id -u)/com.matterbox.listen
tail -f ~/Library/Logs/matterbox-listen.log

# or just run it in the foreground while you iterate
matterbox listen
```

A reload that fails to compile changes nothing: the daemon logs the error and
keeps running the rules it already had, so a typo cannot disarm a working
daemon. To see what is configured without touching the daemon at all, run
`matterbox rules list`.

The startup — and reload — line reports how many rules loaded:

```
matterbox listen: cache=… two_way=true … rules=1 configured
```

If a glob, regexp or action type is wrong, the daemon **refuses to start** and
tells you which rule and why, so a typo fails loud rather than silently never
firing:

```
rules: rule "watch-ops": bad message regexp "(": error parsing regexp: …
```

### Watch it fire, then swap in a real action

You do not have to wait for someone to say the magic word — ask which rules a
message would fire:

```sh
matterbox rules test -m "sev-1 in prod" --channel "Ops Alerts" --author bob
```

Nothing runs: it reports, per rule, whether it matches and — when it does not —
which condition stopped it. See
[Testing a rule](#testing-a-rule).

With the daemon log open, post a test message that should match. You will see
your `log` line appear. Once the `match` is doing what you want, replace `log`
with the action you actually need — `notify`, `exec`, `webhook`, `react` or
`mark_read` (see [Actions](#actions)):

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

> **Heads up.** the moment you add *any* `rules:`, the built-in mention and DM
> Telegram bridge is no longer applied — your list is the whole policy. If you
> still want mention notifications, include a `notify` rule yourself; see [The
> notification bridge is just a rule](#the-notification-bridge-is-just-a-rule).

#### Testing exec and webhook safely

`exec` and `webhook` receive the message as a JSON envelope (and `exec` also as
`MATTERBOX_*` environment variables). To see exactly what your script receives,
point it at a throwaway command first:

```yaml
    actions:
      - type: exec
        command: ["sh", "-c", "cat >> /tmp/matterbox-rule.log"]
```

Post a matching message, then inspect `/tmp/matterbox-rule.log` to see the
envelope; [the exec / webhook payload](#the-exec--webhook-payload) documents
every field. You can also exercise a rule against your own messages without
involving anyone else: run the daemon with `--notify-self` and post in your
self-DM.

### Authoring tips

- **Order matters.** Rules fire top to bottom; put specific rules first and use
  `stop: true` on a rule that should be the last word for a message.
- **Conditions are ANDed.** Add fields to narrow, remove fields to widen.
  Different *fields* are ANDed; within `channel` or `author` a **list** is ORed
  (`channel: [ops, "eng-*"]` matches either). For "everything except…", nest a
  `not:` block.
- **`channel` matches the display name**, not `team/channel` — use the name as
  it appears in the sidebar (globbing with `*` and `?`), or paste an exact
  channel id. It accepts a single value or a list.
- **`team` matches the team's URL slug** (the `core` in `/core/channels/…`, the
  same slug `matterbox send` takes), not the team's display name. Use it alone
  to match every channel in a team, or alongside `channel` to disambiguate a
  channel name shared across teams.
- **Use `(?i)`** at the start of a `message` regexp for case-insensitive
  matching.
- **Iterate with `log`**, promote to real actions once the match is right.
- **`matterbox rules test` beats guessing.** It runs the same matcher the daemon
  runs and names the first condition that failed.
- **A rule only reacts to what it lists.** With no `on:`, that is new messages —
  so a rule about a reaction or an edit needs [`on:`](#triggers-on).

## Working on rules: `matterbox rules`

Four verbs, all reading the same config the daemon reads and running the same
matcher — so an answer here is the answer the daemon would give.

### Testing a rule

`matterbox rules test` reports, for every rule, whether it matches — and when it
does not, the first condition that stopped it. Nothing runs: no action is
executed, nothing is posted, and the ledger and the rate gates are only read.

```sh
$ matterbox rules test -m "sev-1 in prod" --channel "Ops Alerts" --author bob
probe: message at Tue 18 Aug 21:32 in "Ops Alerts" from @bob — "sev-1 in prod"

  ✓  pager                    → exec → notify  [stop: later rules are skipped]
  ✗  desktop                  mention doesn't match
  –  nightly-digest           reacts to schedule
```

`✓` fires, `✗` does not (with the condition that failed), `–` is not listening
for this kind of event, and `⏸` matches but is held back by a
[cooldown](#periodic-firing-with-cooldown) or
[frequency](#rate-limiting-with-frequency) gate.

Describe a hypothetical message with flags, or pass a real post's id — or the
permalink the UI copies — to test against a message that actually exists,
attachments and props included:

```sh
matterbox rules test -m "help" --dm --author alice
matterbox rules test 8x4k9y…                              # a real post
matterbox rules test 8x4k9y… --on reaction --emoji eyes --reactor bob
matterbox rules test -m "deploy" --at 03:00               # test a time window
matterbox rules test --on schedule                        # the timer rules
```

Every field of the probe has a flag, so a rule can be tested against a message
nobody has sent:

| Flag | What it sets |
|---|---|
| `-m`, `--message` | The body. It is also what a `mention:` condition reads — put `@you` in the text to test a mention rule. |
| `--channel` | The channel's display name (what `channel:` globs match). |
| `--team` | The team's URL name (what `team:` globs match). |
| `--author` | The sender's username, without the `@`. |
| `--type` | The conversation kind: `public` (the default), `private`, `dm`, or `group`. |
| `--dm` | Shorthand for `--type dm`. |
| `--from-me` | The post is your own — what `from_me: true` matches. |
| `--bot` | The post comes from a bot or incoming webhook — `from_bot: true`. |
| `--file` | The post carries an attachment — `has_file: true`. |
| `--thread` | The post is a thread reply — `is_thread: true`. |
| `--on` | The trigger kind: `message` (the default), `edit`, `delete`, `reaction`, `reaction_removed`, `schedule`. |
| `--emoji` | The reaction's shortcode. Needs `--on reaction` (or `reaction_removed`). |
| `--reactor` | Who reacted. Needs a reaction trigger. |
| `--at` | The moment the trigger fired: `15:04`, `2006-01-02 15:04`, or RFC3339. |

`--at` moves the clock, which is how a `time:` window or a weekday condition is
checked without waiting for Tuesday.

With a **real post** the post supplies the body, the attachments and the thread
position, so `-m`, `--file`, `--thread` and `--bot` have nothing left to add —
but `--on`, `--emoji`, `--reactor` and `--at` still shape the trigger, which is
how you ask what an old post would do if someone reacted to it right now.

A probe that could not mean what it says is rejected rather than quietly testing
something else: an unknown `--on` or `--type`, or an `--emoji`/`--reactor`
without a reaction trigger.

### Reloading

`systemctl --user reload matterbox-listen.service` — or
`pkill -HUP -f "matterbox listen"` — makes the daemon re-read the config and
swap its rules in place: no dropped connection, no re-run catch-up. A config
that fails to compile is reported and **ignored**; the daemon keeps the rules it
already had.

### Is it firing at all?

```sh
$ matterbox rules stats
RULE                            FIRES  LAST
pager                              12  2026-08-18 09:12
desktop                             –  never
```

Counters persist across restarts and count *firings*, not matches — a rule held
back by its cooldown or frequency window does not count. A dash next to a rule
you expect to be busy is the fastest sign its match is wrong; `rules test` then
says which condition.

### The ledger

```sh
matterbox rules state                    # every key
matterbox rules state get zork:active
matterbox rules state set greeted today
matterbox rules state del zork:active    # unwedge a stuck rule
```

The [ledger](#persistent-state-the-ledger) is the one piece of rule state with
no other window onto it.

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
`respect_mutes`, `quiet_hours` and `notify_dms` — and the per-channel read check
(`notify_delay_seconds`), exactly as the built-in bridge does. A rule that must
page through quiet hours and mutes can opt out with `urgent: true`; the self and
DM gates still apply. See [Actions](#actions).

### Catch-up after a reconnect

When the daemon (re)connects it sends a single "📥 While you were away" digest of
the mentions and DMs that arrived while it was offline. That digest is filtered
through your rules: a message is only included if a rule with a `notify` action
matches it, so narrowing or routing your notifications narrows the catch-up too.

Two deliberate bounds. The catch-up only considers unread **mentions and DMs** —
the set the server lets the daemon query cheaply on reconnect, not every unread
post. And it only drives `notify`: `exec`, `webhook`, `react` and the `state_*`
ledger actions are **live-only** and never re-fire for historical messages, so a
reconnect cannot replay side effects or double-count a counter. The
[`frequency`](#rate-limiting-with-frequency) window is likewise live-only. The
digest always goes to the default `telegram.chat_id`; a per-rule `chat_id`
applies to live messages.

## How rules are evaluated

For each event — system messages are always skipped — the daemon walks the
`rules:` list top to bottom, considering only the rules that react to *that kind
of event*. Every rule whose `match` passes runs its `actions`. A matching rule
with `stop: true` ends evaluation: no later rule runs for that event.

## Triggers (`on`)

A rule reacts to whatever its `on:` field lists. With no `on:`, that is
`message` — a new post — which is what every rule did before this field existed.

| `on:` | Fires when |
|---|---|
| `message` | A new post arrives. The default. |
| `edit` | An existing post is edited. |
| `delete` | A post is deleted. The post is the tombstone, so `message` still matches the text it had. |
| `reaction` | Someone adds an emoji reaction to a post. |
| `reaction_removed` | Someone removes one. |
| `schedule` | The rule's own timer comes due. Needs a `schedule:` block; cannot be combined with the other kinds. |

A rule can list several: `on: [message, edit]` reacts to a post and to later
edits of it.

```yaml
rules:
  - name: someone-noticed
    on: reaction              # a single value or a list
    match:
      from_me: true           # …reacted to *my* post
      emoji: [eyes, white_check_mark]
    actions:
      - type: mark_read
```

On a reaction trigger the post-centric conditions keep their usual meaning:
`author`, `from_me`, `channel` and `message` describe **the post**, and who
reacted is [`reactor`](#conditions-match). That way "somebody acknowledged my
message" is `from_me: true` — which is what you actually want to match.

### Scheduled rules

`on: schedule` is the trigger nothing in Mattermost causes. Set exactly one of
`cron:` or `every:`:

```yaml
rules:
  - name: standup-prompt
    on: schedule
    schedule:
      cron: "0 9 * * 1-5"     # 09:00, Monday to Friday, local time
    actions:
      - type: send
        channel: eng/standup  # required: a timer has no channel of its own
        text: "Standup in 15 minutes ⏰"

  - name: hourly-sweep
    on: schedule
    schedule:
      every: 1h               # first fires an hour after the daemon starts
    actions:
      - type: exec
        command: ["/home/me/bin/sweep.sh"]
```

`cron:` is the ordinary five-field crontab grammar — minute, hour, day of month,
month, day of week — with `*`, lists (`1,15`), ranges (`1-5`), steps (`*/10`,
`9-17/2`) and names (`mon`, `jan`). As in crontab, when day-of-month *and*
day-of-week are both restricted the rule fires when **either** matches. Times
are local.

`every:` measures from the last firing, which is remembered across restarts — so
a daemon that restarts twice an hour still runs a `24h` rule once a day. The
minimum is `1m`.

Three things to know:

- **A restart catches up nothing.** A firing missed because the daemon was down
  is skipped, never replayed at startup — a standup prompt four hours late is
  worse than one that never came. A *running* daemon does cover a late tick,
  though: if the machine suspends and the clock jumps (Go's timers freeze with
  it), the tick after the resume still fires the minute it slept through, as
  long as that was within the last five minutes. Only the most recent match
  fires — waking from an hour's sleep with a `*/10` rule owes you one recap, not
  six.
- **There is no post**, so `notify`, `react` and `mark_read` have nothing to act
  on and are rejected at compile time, and `send` must name a `channel:`.
- **`match` still applies.** [`state` conditions](#matching-on-the-ledger) in
  particular let a scheduled digest hold its tongue until there is something to
  report.

`matterbox rules list` shows when each scheduled rule fires next.

## Conditions (`match`)

Different fields are ANDed; an empty `match` matches every message.

| Field | Meaning |
|---|---|
| `channel` | Case-insensitive glob (`*`, `?`) over the channel's **display name**, or an exact channel id. A single value or a list (matches **any**). |
| `team` | Case-insensitive glob over the team's **URL name** (the slug in the channel URL, e.g. `core`), or an exact team id. A single value or a list. A DM carries no team, so a `team` condition never matches a direct message. |
| `author` | Username (no leading `@`), matched case-insensitively. A single value or a list. |
| `message` | [RE2](https://github.com/google/re2/wiki/Syntax) regexp over the body **plus any attachment text** (see below). Prefix `(?i)` for case-insensitive. Its capture groups are available to templates — see [Captures](#captures-from-message). |
| `mention` | `true` requires you were directly @named (the same test the bridge uses). |
| `dm` | `true` = only direct messages; `false` = only channels; unset = either. |
| `from_me` | `true` = only your own posts; `false` = only others'; unset = either. `from_me: false` is how you keep a rule from firing on the messages **you** send — `exec`, `webhook` and `react` have no built-in self-skip the way `notify` and `send` do. |
| `has_file` | `true` requires at least one attachment. |
| `from_bot` | `true` = only posts from a bot or incoming webhook; `false` = only posts from people; unset = either. |
| `channel_type` | `public`, `private`, `dm`, or `group` (a multi-person DM). A single value or a list. |
| `emoji` | Reaction shortcode (no colons), as a case-insensitive glob or exact name. A single value or a list. Needs `on: reaction` (or `reaction_removed`). |
| `reactor` | Username (no `@`) of whoever reacted. A single value or a list. Needs a reaction trigger. |
| `time` | A window of the local clock and/or certain weekdays: `after`, `before`, `days`. See [Office hours](#office-hours-with-time). |
| `is_thread` | `true` = only thread replies; `false` = only root posts; unset = either. |
| `viewing` | `true` = only when you are **looking at** the post's channel; `false` = only when you are not; unset = either. See [Not while you're reading it](#not-while-youre-reading-it). |
| `not` | A nested `match` block that **inverts**: the rule fires only when the post does **not** satisfy it. Recursive. |
| `frequency` | A rolling-window threshold: even when the fields match, fire only on a **burst**. See [below](#rate-limiting-with-frequency). |
| `cooldown` | A minimum interval: even when the fields match, fire at most **once per period** (`every: 48h`), then stay quiet. Persisted across restarts. The general form of "once a day/week". See [below](#periodic-firing-with-cooldown). |
| `state` | Match on the persistent [ledger](#matching-on-the-ledger) — e.g. `failure_count` is at least 3. |

`channel` and `author` as a list is an OR; combine with `not` to subtract. For
example, "anything in the ops channels except from the bots":

```yaml
match:
  channel: ["Ops*", "Incidents"]
  not:
    author: [deploybot, alertmanager]
```

### Messages that hide in attachments

Integrations — Jira, GitLab, alertmanager, most incoming webhooks — routinely
post with an **empty body** and put everything in a Slack-style attachment. A
`message` condition therefore matches the body *and* the flattened attachment
text (pretext, title, text, fields, footer), and such a post reaches the rules
even though its body is empty:

```yaml
rules:
  - name: failed-pipelines
    match:
      from_bot: true
      message: "(?i)pipeline failed"   # matches attachment text too
    actions:
      - type: notify
        urgent: true
```

The raw attachment text is also in the
[exec/webhook payload](#the-exec--webhook-payload) as `attachment_text`,
separate from `message`.

### Captures from `message`

The capture groups of a `message` regexp are available to every
[template](#templating-keys-and-values) in the rule, so a command-style message
can carry its own arguments:

```yaml
rules:
  - name: deploy-command
    match:
      message: '^!deploy (?P<env>\w+)$'
    actions:
      - type: exec
        command: ["/home/me/bin/deploy.sh", "{{ .match.env }}"]
```

- A **named** group `(?P<env>…)` reads as `{{ .match.env }}`.
- A **numbered** group reads as `{{ index .match "1" }}` — Go templates have no
  syntax for a numeric field name. `"0"` is the whole match.
- `exec` also gets them as `MATTERBOX_MATCH_ENV`, `MATTERBOX_MATCH_1`, …
- Each rule sees only its own captures; a rule with no `message` condition sees
  an empty map.

### Office hours with `time`

`frequency` and `cooldown` say how *often* a rule may fire; `time` says *when*
it may fire at all. Every field set is ANDed:

```yaml
match:
  time:
    after: "09:00"          # inclusive, local time
    before: "17:30"         # exclusive
    days: [mon, tue, wed, thu, fri]
```

A `before` earlier than `after` wraps midnight, so
`after: "22:00", before: "06:00"` is the night. Either bound can stand alone
(`after: "18:00"` runs to the end of the day). `days` accepts `mon`..`sun`, full
names, or `0`-`7`.

The window is tested against the moment the **trigger** fired, not the daemon's
uptime — so a reaction to an old post is judged by when the reaction happened.

### Not while you're reading it

The daemon has its own WebSocket connection and knows nothing about your TUI, so
without help it will happily pop a desktop notification for the reply to the
message you are, at that moment, watching someone type. `viewing` is the fix:

```yaml
rules:
  - name: desktop-dms
    match:
      dm: true
      from_me: false
      viewing: false        # … but not for the chat that's on my screen
    actions:
      - type: exec
        command: [notify-send, "{{ .author }}", "{{ .message }}"]
```

`viewing: true` holds when a matterbox TUI **on this machine** has the post's
channel open *and* its terminal has focus. Both halves matter: a conversation
sitting in a buried window is not being read, and it still notifies.

The daemon asks the TUI over its control socket (`~/.config/matterbox/tui.sock`,
the same one `matterbox open` writes to) and caches the answer for a second.
Anything that is not a clear "yes" — no TUI running, the socket gone stale, a
daemon on another host, a channel hidden behind the Feed/Search/SQL tab — counts
as **not viewing**, so a `viewing: false` rule keeps firing exactly as it did
before. A missed notification is the expensive failure; a redundant one is not.

`notify` actions get this for free: the Telegram bridge never pushes a message
for the channel you are focused on, in the same breath as it skips muted
channels and quiet hours. You only need `viewing` for `exec` and `webhook` rules
— the ones that drive your own desktop notifications.

Terminals report focus via DECSET 1004 (Ghostty, kitty, WezTerm, Alacritty,
xterm; inside tmux, `set -g focus-events on`). On a terminal that never reports
it, the TUI falls back to "was there input in the last minute" — coarser, but it
still keeps quiet while you are typing.

### Rate limiting with `frequency`

The fields above answer *"does this one message match?"* — they are stateless.
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

**Semantics.** Every matching message records a hit in a sliding window; hits
older than `within` fall out. The rule fires on the message that fills the
window to `count`, and the window then **resets** — so it re-arms only after
another full burst, rather than firing again on the 4th, 5th, … message. With
`by: author`, each author has an independent window (one noisy author cannot
trip a threshold meant for the room as a whole); `by: channel` does the same per
channel.

The window is **in-memory and live-only**: it starts empty on every (re)start,
and the [reconnect catch-up](#catch-up-after-a-reconnect) never feeds historical
messages through it. A frequency gate is therefore best for "happening right
now" bursts, not for thresholds that must survive a restart — for that, count
into the [persistent ledger](#persistent-state-the-ledger) instead.

### Periodic firing with `cooldown`

`frequency` answers *"has this happened enough lately?"*; `cooldown` is the
inverse — *"has it been long enough since the last time?"*. With a cooldown the
rule fires on the next matching message, then goes quiet for `every`, then fires
on the next match after that. It is how you say **"do this at most once a day /
week / every two days"** without pinning it to a wall-clock time: the trigger is
still a message, but the rule throttles itself to one firing per interval.

```yaml
rules:
  - name: weekly-standup-nudge
    match:
      channel: "Engineering"
      cooldown:
        every: 168h     # 7 days — fire at most once a week
        by: channel     # optional: a separate interval per author / channel / team
    actions:
      - type: send
        text: "📅 Reminder: standup notes due!"
```

| Field | Meaning |
|---|---|
| `every` | The minimum time between firings, as a Go duration: `24h` (daily), `48h` (every two days), `168h` (weekly), `30m`. |
| `by` | Keep a separate interval per `author`, `channel`, or `team`, or one for the whole rule (`global`, the default). |

**Semantics.** The rule fires on a matching message only if it has not fired
within the last `every` (for that `by` group); firing records the time and
re-arms the interval. Unlike `frequency`, the last-fire time is **persisted**,
so the interval is honoured across a daemon restart — a
`cooldown: { every: 168h }` that fired yesterday will not fire again after a
restart today. It is still **live-only** for *firing* (the
[catch-up](#catch-up-after-a-reconnect) never trips it), and it composes with
the field conditions and `frequency` — all must pass.

The interval is **rolling**: measured from the last firing, not aligned to
midnight. For a reset-at-local-midnight "once per calendar day" instead, build a
per-day ledger key from [`{{ today }}`](#templating-keys-and-values).

## Actions

| `type` | Fields | What it does |
|---|---|---|
| `notify` | `summarize`, `urgent`, `chat_id` (all optional) | Summarise and deliver to Telegram. `summarize` overrides `listen.summarize`; `urgent: true` delivers even during quiet hours or for muted channels; `chat_id` routes to a different Telegram chat. |
| `exec` | `command` (argv list) | Run a local command. Each argv element is a [template](#templating-keys-and-values) over the post, so an argument can carry `{{ .author }}`, `{{ .message }}`, … (e.g. `["notify-send", "{{ .author }} sent a message"]`). The message is also piped to its **stdin as JSON** and exported as `MATTERBOX_*` environment variables. 30 s timeout. |
| `webhook` | `url`, `headers` (optional map) | HTTP `POST` the message envelope as a JSON body. `headers` adds request headers; values are expanded from the daemon's environment (`$TOKEN` / `${TOKEN}`). 15 s timeout. |
| `react` | `emoji` (shortcode) | Add an emoji reaction to the message. |
| `mark_read` | — | Mark the message's channel read. |
| `send` | `text`, `channel` (optional), `thread` (optional) | Post a message. `text` is a [template](#templating-keys-and-values), so it can carry `{{ .author }}`, `{{ today }}`, … With no `channel` it posts into the channel the trigger arrived in; `channel` (`team/channel` or `@user`) routes it elsewhere. `thread: true` replies in the trigger's thread (ignored when `channel` is set). Skips your own posts (unless `--notify-self`) so an ungated rule cannot loop on its own output. |
| `log` | `text` (optional prefix) | Write a line to the daemon log. |
| `state_set` | `key`, `value` | Write `value` into the persistent [ledger](#persistent-state-the-ledger) under `key`. Both are templates. |
| `state_incr` | `key`, `by` (optional, default 1) | Add `by` to the integer stored at `key` (a missing or non-numeric value counts as 0). Negative decrements. |
| `state_del` | `key` | Remove `key` from the ledger. |

`urgent` bypasses only the do-not-disturb suppression (`quiet_hours`,
`respect_mutes`); the self and `notify_dms` gates still apply. A rule that
routes to a non-default `chat_id` is delivery-only — the two-way reply buttons
work just for the configured `telegram.chat_id`.

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
  "event": "message",
  "emoji": "eyes",
  "reactor": "alice",
  "attachment_text": "Pipeline failed on main",
  "rule": "",
  "match": { "0": "deploying now", "1": "now" },
  "state": { "failure_count": "3", "last_failure_time": "1700000000000" }
}
```

`team`/`team_id`, `root_id` and `permalink` are omitted when empty; `mentioned`
is whether *you* were @named; `files` lists attachment names (present only when
the post carries file metadata); `state` is a snapshot of the
[persistent ledger](#persistent-state-the-ledger) (omitted when empty).

`event` is the [trigger kind](#triggers-on), so one script can serve several
rules. `emoji` and `reactor` are set only for a reaction trigger, `rule` only
for a scheduled one, `attachment_text` only when the post carries
[attachment text](#messages-that-hide-in-attachments), and `match` holds the
`message` regexp's [captures](#captures-from-message).

`exec` additionally exports each scalar field as an environment variable so a
quick script need not parse JSON:

```
MATTERBOX_POST_ID        MATTERBOX_CHANNEL_ID     MATTERBOX_CHANNEL
MATTERBOX_TEAM_ID        MATTERBOX_TEAM           MATTERBOX_AUTHOR
MATTERBOX_MESSAGE        MATTERBOX_IS_DM          MATTERBOX_IS_THREAD
MATTERBOX_ROOT_ID        MATTERBOX_MENTIONED      MATTERBOX_FILES
MATTERBOX_PERMALINK      MATTERBOX_CREATE_AT      MATTERBOX_EVENT
MATTERBOX_EMOJI          MATTERBOX_REACTOR        MATTERBOX_ATTACHMENT_TEXT
MATTERBOX_RULE
```

`MATTERBOX_FILES` is comma-separated. Regexp captures are exported as
`MATTERBOX_MATCH_<NAME>` / `MATTERBOX_MATCH_1`. The ledger is exported as
`MATTERBOX_STATE` (the whole map as JSON) plus one `MATTERBOX_STATE_<KEY>` per
entry — the key upper-cased, non-alphanumerics collapsed to `_` — e.g.
`MATTERBOX_STATE_FAILURE_COUNT=3`.

## Persistent state (the ledger)

The `match` conditions and most actions are stateless — each message is judged
on its own. The **ledger** breaks that: a small key/value store, persisted in
the [message database](database.md) (the `rule_state` table) and therefore
surviving restarts, that rules read and write. It is how a rule carries context
from one message to the next — a running failure count, the timestamp of the
last deploy, a flag that a later rule checks.

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

The `key` of `state_set`/`state_incr`/`state_del`, the `value` of `state_set`,
the `send` `text`, and each element of an `exec` `command`, are
[Go `text/template`](https://pkg.go.dev/text/template) strings expanded against
the message. The data is exactly the
[exec/webhook envelope](#the-exec--webhook-payload) above — so `{{ .author }}`,
`{{ .channel }}`, `{{ .create_at }}`, `{{ .message }}`, `{{ .is_dm }}`, … all
work — **plus** the current ledger under `.state`, so a template can read a
value another action just wrote:

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
`failures:bob`) use the `index` function: `{{ index .state "failures:bob" }}`. A
field or `.state` key that does not exist renders as empty, not an error.

Two date helpers are available on top of the post fields, in every template —
state keys and values, state-match keys, the `send` body, and `exec` command
arguments:

| Function | Renders |
|---|---|
| `{{ today }}` | The current **local** date as `2006-01-02` (e.g. `2026-06-22`). |
| `{{ now }}` | The current local time as a Go `time.Time`; format it yourself, e.g. `{{ now.Format "15:04" }}`. |

`today` is the building block for **once-per-day** behaviour: put it in a ledger
key and gate on the key's absence. `key: "greeted:{{ today }}"` with
`exists: false` matches only the first time that key is seen each calendar day;
setting it afterwards closes the gate until tomorrow, when the date — and so the
key — changes. The per-day keys are tiny and simply accumulate; the date is the
daemon host's local timezone.

#### How state flows to other actions

- **Order is guaranteed.** Within one rule, `state_*` actions run synchronously
  in the order written, *before* the `exec`/`webhook` actions that follow them
  are dispatched — so a script invoked later in the same rule sees the values
  this rule just wrote.
- **`exec` and `webhook`** receive the whole ledger: in the JSON envelope under
  `state`, and `exec` also as `MATTERBOX_STATE` / `MATTERBOX_STATE_<KEY>`
  environment variables.
- **`state_incr` is atomic**, so two messages updating the same counter at once
  cannot lose a write.

### Matching on the ledger

Writing the ledger is only half of it — a `match.state` condition reads it, so
one rule can react to what another counted. Each condition names a `key` and one
or more operators, all ANDed:

| Operator | Meaning |
|---|---|
| `exists` | `true` = key is present; `false` = key is absent. |
| `eq` / `ne` | Value equals / does not equal this (string compare). |
| `gt` / `gte` / `lt` / `lte` | Value, **as a number**, compared to this (a range is `gte` + `lt`). |

`state` takes a single condition or a list (all ANDed). The `key` is a
**template** over the post, just like the action keys — so a condition can read
a per-channel or per-author key, e.g. `key: "hot:{{ .channel_id }}"`. The
canonical pairing: count failures in one rule, page when the count crosses a
threshold in another.

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
(there is nothing to compare); a non-numeric value never satisfies a numeric
operator.

This overlaps with [`frequency`](#rate-limiting-with-frequency) but trades off
differently: `frequency` is in-memory and self-pruning (great for "N in the last
T minutes"), while a ledger counter is exact, persistent across restarts, and
yours to reset — better when the threshold must survive a restart, or the count
is cleared by an explicit event (a green deploy) rather than by time.

### Correlating across messages

Because a state condition's `key` is templated per post, the ledger can remember
context about *one conversation* and react to it several messages later — a
correlation no single-message `match` (term **and** mention in the same message)
or `frequency` window (the same rule's own hits) can express. The pattern is a
per-channel **countdown**: a trigger term arms a window, every message ticks it
down, and a mention while the window is open escalates. "Was a sev-1 term posted
in the last ~5 messages, and then I got @-mentioned?"

```yaml
rules:
  # 1) ARM: a trigger term opens a ~5-message window for this channel.
  - name: arm-hot
    match:
      message: &terms "(?i)urgent|sev-?1|p1|outage|prod(uction)? down"
    actions:
      - type: state_set
        key: "hot:{{ .channel_id }}"
        value: "5"

  # 2) ESCALATE: I'm mentioned while this channel is still "hot".
  - name: hot-mention
    match:
      mention: true
      state: { key: "hot:{{ .channel_id }}", gte: 1 }
    actions:
      - type: notify
        urgent: true

  # 3) TICK: each NON-term message in a hot channel counts the window down.
  - name: tick-hot
    match:
      state: { key: "hot:{{ .channel_id }}", gte: 1 }
      not: { message: *terms }
    actions:
      - type: state_incr
        key: "hot:{{ .channel_id }}"
        by: -1

  # 4) NORMAL: an ordinary mention (channel not hot) → regular notification.
  - name: mention
    match:
      mention: true
      not: { state: { key: "hot:{{ .channel_id }}", gte: 1 } }
    actions:
      - type: notify
```

The `&terms` / `*terms` is a YAML anchor, so the term regexp is written once.
Why this order:

- **ARM first** so a message that is *both* a term and an @-mention ("sev-1,
  @corne!") arms the window and then escalates in the same pass — putting it
  later would let that high-signal message fall through both notify rules.
- **TICK skips term messages** (`not: { message: *terms }`) and runs *after* the
  escalate check, so the arming message does not consume its own window and a
  mention on the last message of the window still escalates.
- Rules 2 and 4 are **mutually exclusive** (the `not:` in rule 4), so a mention
  fires *either* the urgent path *or* the normal one, never both — and an
  ordinary mention in a channel that never saw a term still notifies (an absent
  `hot:` key fails `gte: 1`, so `not:` lets rule 4 through).
- The window is **per channel** — a term in `#ops` cannot escalate a mention in
  `#random`.

> **Live-only, like side effects.** State actions only run for **live**
> messages. The [reconnect catch-up](#catch-up-after-a-reconnect) drives
> `notify` only — it never replays `state_set`/`state_incr`/`state_del` (nor
> `exec`, `webhook`, `react`), so a reconnect cannot double-count a failure or
> re-fire a side effect for history. `state` *match* conditions are evaluated in
> catch-up against the current ledger, but since the historical messages do not
> mutate it, that just reflects "does this rule's threshold currently hold".

## Examples

Page yourself for sev-1 alerts and stop there:

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

A desktop notification for direct mentions, on Linux:

```yaml
rules:
  - name: desktop
    match: { mention: true }
    actions:
      - type: exec
        command: ["notify-send", "{{ .author }} mentioned you", "{{ .message }}"]
```

Mark a DM read as soon as you acknowledge it from your phone with 👀:

```yaml
rules:
  - name: ack-clears-unread
    on: reaction
    match:
      dm: true
      reactor: me-on-mobile      # your own username
      emoji: eyes
    actions:
      - type: mark_read
```

Post the standup prompt on weekday mornings:

```yaml
rules:
  - name: standup-prompt
    on: schedule
    schedule:
      cron: "0 9 * * 1-5"
    actions:
      - type: send
        channel: eng/standup
        text: "Standup in 15 minutes ⏰"
```

Page for a failed pipeline — from an integration that posts an empty body and
puts everything in an attachment — but only during office hours:

```yaml
rules:
  - name: pipeline-failures
    match:
      from_bot: true
      channel_type: [public, private]
      message: "(?i)pipeline failed"
      time:
        after: "08:00"
        before: "18:00"
        days: [mon, tue, wed, thu, fri]
    actions:
      - type: notify
        summarize: false
```

Run a deploy script with the environment the message named:

```yaml
rules:
  - name: deploy-command
    match:
      from_me: false
      message: '^!deploy (?P<env>staging|prod)$'
    actions:
      - type: exec
        command: ["/home/me/bin/deploy.sh", "{{ .match.env }}"]
      - type: react
        emoji: rocket
    stop: true
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

Mirror everything in a channel to an authenticated external system — the token
comes from the daemon's environment, not the config file:

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

Say good morning the first time anyone posts in your team, and only once a day.
The `send` action posts the greeting and a
[`cooldown`](#periodic-firing-with-cooldown) throttles it to one firing per
interval:

```yaml
rules:
  - name: good-morning
    match:
      team: core            # any channel in the "core" team …
      cooldown:
        every: 24h          # … but at most once a day (48h for every two days, 168h weekly)
    actions:
      - type: send
        text: "Good morning! ☀️"   # posts into the channel that triggered it
    stop: true
```

The rule fires on the first matching message, then the cooldown suppresses it
until `every` has elapsed, when the next message re-arms it. Change `every` to
`48h`, `168h`, or any [duration](https://pkg.go.dev/time#ParseDuration) for a
different cadence; add `by: channel` (or `team`) to greet each room
independently. Drop the `team` line to greet on the first message **anywhere**,
or add a `channel:` condition to pin the trigger to one room. To greet in a
*fixed* channel instead of wherever the trigger landed, add
`channel: core/general` to the `send` action itself.

The cooldown interval is **rolling** — measured from the last greeting. If you
would rather it reset at local **midnight**, so the first message at 08:00
always greets even if yesterday's was at 09:00, gate on a per-day ledger key
built from `{{ today }}` instead of a cooldown:

```yaml
  - name: good-morning-calendar
    match:
      team: core
      state: { key: "greeted:{{ today }}", exists: false }   # only if we haven't greeted today
    actions:
      - type: state_set
        key: "greeted:{{ today }}"                           # close the gate until tomorrow
        value: "1"
      - type: send
        text: "Good morning! ☀️"
    stop: true
```

### Reminders: `!remind me in 2 hours …`

Two rules and a small helper make a `!remind me …` command: one reacts to the
message that asks for the reminder, the other is a schedule rule that delivers
the ones that have come due.

The first hands every `!remind` message you post to
[`scripts/matterbox-remind`](../scripts/matterbox-remind), which parses the
delay, stores the reminder in `~/.config/matterbox/reminders.db`, and replies
with a confirmation. The second runs the same helper with `--tick` once a
minute; the tick delivers *every* reminder that is due, so a missed minute is
late, never lost. Everything posts back with `matterbox reply`, so the
confirmation and the delivered reminder thread under your original message.

```yaml
rules:
  - name: reminders
    match:
      from_me: true            # only your own commands schedule reminders
      message: "(?i)^\\s*!remind"
    actions:
      - type: exec
        command: ["/home/me/.config/matterbox/matterbox-remind"]

  - name: reminder-tick
    on: schedule
    schedule:
      every: 1m
    actions:
      - type: exec
        command: ["/home/me/.config/matterbox/matterbox-remind", "--tick"]
```

What you can type — the helper dispatches on the message:

| Command | Effect |
|---|---|
| `!remind me in <when> <text>` | Schedule for a relative delay and confirm. |
| `!remind me at <date> [time] <text>` | Schedule for a clock date/time and confirm. |
| `!reminders` (or `!remind list`) | List this channel's pending reminders. |
| `!remind cancel <id>` | Cancel a pending reminder. |

`in <when>` is flexible: `2 hours`, `2 days`, `30m`, `1h30m`, `1 day 6 hours`,
`90 minutes`, `2 weeks` — weeks, days, hours, minutes and seconds, spelled out
or abbreviated and combinable. A leading `in`/`after` and a connector ("… to
call dad", "… that the build is done") are ignored.

`at <date> [time]` schedules for an absolute moment. The date can be
`2026-07-01`, `1 July`/`jul 1`, `03/07` (**day-first** — 3 July), a weekday
(`friday`), or `today`/`tomorrow`; the optional time can be `14:30`, `2:30pm`,
`9am`, `14h30`, or `noon`. A date with no time defaults to **09:00** (override
with `MATTERBOX_REMIND_HOUR`), and a bare time (`!remind me at 18:00 …`) means
today. A spec that has already passed but omits the year or date — a weekday, a
bare time, a no-year date — rolls forward to its next occurrence; a
fully-specified past moment is rejected. (`on` and `@` work as synonyms for
`at`, and a bare absolute date with no keyword is accepted too.)

Install the helper:

```sh
cp scripts/matterbox-remind ~/.config/matterbox/
chmod +x ~/.config/matterbox/matterbox-remind
systemctl --user reload matterbox-listen      # pick up the two rules
```

The reminders persist in SQLite, so they survive a daemon — or host — restart.
Delivery only runs while the daemon does, which is also the only time a reminder
can be *created*; if you want them to go out even with the daemon stopped, the
tick rule can be a systemd timer instead —
[`scripts/matterbox-remind.timer`](../scripts/matterbox-remind.timer) and its
service unit still do exactly that (`Persistent=true` catches up a tick missed
while the box was off).

## Safety

A rule that reacts to a reaction and then *adds* one is the obvious way to build
a loop: the server echoes every reaction back over the WebSocket. The daemon
breaks that specific circle — the echo of a reaction its own `react` action just
added never triggers a rule — while a reaction **you** make, from your phone
say, still does. Nothing stops a rule from looping through a slower path, though
(a `send` whose text matches the rule's own condition, for instance), so give
any rule that writes back a condition that its own output cannot satisfy —
`from_me: false` is usually it.

`exec` runs commands from your own config, as you, on your own machine — the
same trust level as a shell alias. Each run is bounded by a timeout and runs off
the ingest path, so a slow or hung command cannot block message caching or take
the daemon down.

A bad glob, regexp, **template, `frequency`/`cooldown` duration, `cron`
expression, `time` window, or `state` condition** (missing key, no operator), an
unknown action `type` or `on:` kind, and any combination that could never work —
an `emoji` condition without a reaction trigger, a schedule rule whose `send`
has no channel — are all reported when the rules load, so a typo fails loud
rather than silently never firing. A failed **reload** leaves the running rules
in place. `state_*` writes touch only matterbox's own database (the `rule_state`
table), never your Mattermost server.
