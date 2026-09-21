# Configuration

The plugin is configured in two places in `apiary.yaml`: a `plugins[]` entry
holding its options, and a `sources[]` entry of `type: plugin` that points at
it. The bot token comes from the daemon's environment.

## Token

`SLACK_BOT_TOKEN` must be set in the environment the daemon runs in (its
`.env`). The name is fixed: Apiary passes a plugin only the variables its
manifest declares in `security.secret_env`, so a differently named variable
would never reach the process.

## Plugin options

| Option | Default | |
|---|---|---|
| `state_file` | — (required) | Absolute path to the cursor file. Absolute because a plugin's working directory is its own install directory. |
| `channels` | `[]` | Channels to listen in. See below. |
| `direct_messages` | `false` | Turn every DM to the bot into an item. |
| `allowed_users` | everyone | Slack user ids (`U…`) allowed to start or continue a conversation. |
| `max_per_poll` | `10` | Cap on items emitted per poll. The rest are picked up next poll, in order. |
| `thread_ttl` | `24h` | How long after its last activity a thread is still watched for replies. |
| `max_threads` | `50` | Cap on watched threads; the least recently active drop first. |
| `transcript_limit` | `50` | How many earlier messages a reply item carries. |
| `ack_reaction` | none | Emoji name added to a message once Apiary dispatches it, e.g. `eyes`. |
| `post_failures` | `true` | Post a short notice in the thread when a run fails. |
| `api_url` | `https://slack.com/api` | For testing only. |

At least one of `channels` / `direct_messages` is required.

### `channels[]`

| Field | Default | |
|---|---|---|
| `id` | — (required) | The channel **id** (`C…`, or `G…` for older private channels), not its `#name`. It is at the bottom of the channel's *About* tab. |
| `mode` | `mentions` | `mentions`: only messages that @mention the bot. `all`: every human message. |
| `labels` | `[]` | Extra labels stamped on every item from the channel. |

A thread follows its channel's mode. In a `mentions` channel a thread reply
must mention the bot again — so people can talk to each other in a thread the
bot once answered in without each line starting a run.

The bot must be a member of every listed channel.

## Items

Each human turn becomes one item:

| Field | Value |
|---|---|
| `id` | `slack:<channel>:<thread_ts>:<ts>` |
| `title` | first line of the message, bot mention removed |
| `description` | who wrote it, the conversation so far (replies and DMs), the latest message |
| `labels` | `slack`, `channel:<id>`, `kind:mention\|message\|dm`, `turn:first\|reply`, plus the channel's `labels` |
| `type` | `conversation` |
| `url` | permalink to the message |
| `metadata` | `channel`, `thread_ts`, `ts`, `user`, `kind`, `turn` |

Route on the labels. For example, a cheap model for follow-ups and a stronger
one for openers:

```yaml
workflows:
  - id: slack-open
    trigger: {once: true, match: {source: slack, labels: ["turn:first"]}}
    # …
  - id: slack-follow-up
    trigger: {once: true, match: {source: slack, labels: ["turn:reply"]}}
    # …
```

## What gets posted back

Everything a workflow writes to its source lands in the thread: the
`result_comment`, `post_comment` steps, and any `APIARY_PUBLISH` block an agent
emits. One item can therefore produce several replies.

Agent Markdown is converted to Slack's mrkdwn — links, bold, headings, bullets,
blockquotes; fenced code is left alone — and anything over ~3500 characters is
split into consecutive messages. An empty output posts nothing.

## Source options

```yaml
sources:
  - id: slack
    type: plugin
    poll_interval: 30s
    config:
      plugin: dev.apiary.slack
```

`poll_interval` is the latency floor for every answer. See
[Limits](limits.md) before lowering it.

Raise `plugins[].timeout` from its 10s default (`30s` is comfortable): a poll
makes one Slack call per channel and one per watched thread.
