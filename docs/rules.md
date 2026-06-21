# Rules — per-message automation for `matterbox listen`

The `matterbox listen` daemon holds a persistent WebSocket connection to your
Mattermost server and reacts to every incoming message. **Rules** make that
reaction programmable: when a message matches a set of conditions, the daemon
runs one or more actions — forward it to Telegram, run a local command, POST a
webhook, add a reaction, mark the channel read, or just log it.

This is the kind of automation a server-side Mattermost plugin can't give you:
it's per-user, runs on *your* machine, and can run *your* commands. Think
procmail/Sieve, but for chat.

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
read-check (`notify_delay_seconds`), exactly as the built-in bridge does.

## How rules are evaluated

For each incoming message (system messages, deletions, and empty bodies are
skipped) the daemon walks the `rules:` list top to bottom. Every rule whose
`match` passes runs its `actions`. A matching rule with `stop: true` ends
evaluation — no later rule runs for that message.

## Conditions (`match`)

All set conditions are ANDed; an empty `match` matches every message.

| Field | Meaning |
|---|---|
| `channel` | Case-insensitive glob (`*`, `?`) over the channel's **display name**, or an exact channel id. |
| `author` | Username (no leading `@`), matched case-insensitively. |
| `message` | [RE2](https://github.com/google/re2/wiki/Syntax) regexp over the body. Prefix `(?i)` for case-insensitive. |
| `mention` | `true` requires you were directly @named (the same test the bridge uses). |
| `dm` | `true` = only direct messages; `false` = only channels; unset = either. |
| `has_file` | `true` requires at least one attachment. |
| `is_thread` | `true` = only thread replies; `false` = only root posts; unset = either. |

## Actions

| `type` | Fields | What it does |
|---|---|---|
| `notify` | `summarize` (optional bool) | Summarise + deliver to Telegram. `summarize` overrides `listen.summarize` for this rule. |
| `exec` | `command` (argv list) | Run a local command. The message is piped to its **stdin as JSON** and exported as `MATTERBOX_*` env vars. 30s timeout. |
| `webhook` | `url` | HTTP `POST` the message envelope as a JSON body. 15s timeout. |
| `react` | `emoji` (shortcode) | Add an emoji reaction to the message. |
| `mark_read` | — | Mark the message's channel read. |
| `log` | `text` (optional prefix) | Write a line to the daemon log. |

### The exec / webhook payload

Both `exec` (on stdin) and `webhook` (as the POST body) receive the same flat,
stable JSON envelope:

```json
{
  "post_id": "abc123",
  "channel_id": "xyz789",
  "channel": "Engineering",
  "author": "bob",
  "message": "deploying now",
  "is_dm": false,
  "create_at": 1700000000000,
  "permalink": "https://mm.example.com/eng/pl/abc123"
}
```

`exec` additionally exports each field as an environment variable so a quick
script needn't parse JSON: `MATTERBOX_POST_ID`, `MATTERBOX_CHANNEL_ID`,
`MATTERBOX_CHANNEL`, `MATTERBOX_AUTHOR`, `MATTERBOX_MESSAGE`,
`MATTERBOX_IS_DM`, `MATTERBOX_PERMALINK`.

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

Mirror everything in a channel to an external system:

```yaml
rules:
  - name: archive-incidents
    match: { channel: "Incidents" }
    actions:
      - type: webhook
        url: https://hooks.example.com/incidents
```

## Safety

`exec` runs commands from your own config, as you, on your own machine — same
trust level as a shell alias. Each run is bounded by a timeout and runs off the
ingest path, so a slow or hung command can't block message caching or take the
daemon down. A bad glob, regexp, or unknown action `type` is reported at
startup so a typo fails loud rather than silently never firing.
