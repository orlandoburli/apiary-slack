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
| `thread_replies` | `participants` | Who may continue a thread in a `mentions` channel without mentioning the bot again. See below. |
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

### `thread_replies`

In an `all` channel every reply in a watched thread is a turn. In a `mentions`
channel, `thread_replies` decides which replies are, beyond those that mention
the bot:

| Value | |
|---|---|
| `participants` (default) | Replies from anyone who already addressed the bot in that thread. You mention it once, then just talk; a colleague joining in must mention it once too. |
| `mentions` | None — every turn must mention the bot. |
| `all` | Every human reply in the thread. |

The bot must be a member of every listed channel.

## Items

Each conversation — a thread, or a DM channel — is one item, re-emitted on every
poll while it awaits an answer:

| Field | Value |
|---|---|
| `id` | `slack:<channel>:<thread_ts>`, or `slack:<dm channel>:dm` |
| `state` | `pending` while a human turn awaits a bot reply; `answered` otherwise |
| `title` | first line of the thread's root message, bot mention removed |
| `description` | the conversation so far, then the message(s) awaiting an answer |
| `labels` | `slack`, `channel:<id>`, `kind:mention\|message\|dm`, `turn:first\|reply`, plus the channel's `labels` |
| `type` | `conversation` |
| `url` | permalink to the thread |
| `metadata` | `channel`, `thread_ts`, `ts` (the latest turn), `kind`, `turns` |

The trigger **must** match `states: [pending]` and must **not** be `once: true`:
the same item is dispatched once per turn, and the state is what stops it
re-running in between. Without the `states` filter an answered thread would be
dispatched again on every poll; with `once` the second turn would never run.

Route on the labels. For example, a cheap model for follow-ups and a stronger
one for openers:

```yaml
workflows:
  - id: slack-open
    trigger: {match: {source: slack, states: [pending], labels: ["turn:first"]}}
    # …
  - id: slack-follow-up
    trigger: {match: {source: slack, states: [pending], labels: ["turn:reply"]}}
    # …
```

## What gets posted back

Everything a workflow writes to its source lands in the thread: the
`result_comment`, `post_comment` steps, and any `APIARY_PUBLISH` block an agent
emits. One item can therefore produce several replies.

**For a chat, have the agent publish.** Tell it in the prompt to wrap its answer
in `APIARY_PUBLISH_BEGIN` / `APIARY_PUBLISH_END` and set `result_comment:
on_fail`: only the answer is posted. `result_comment: on_complete` posts
Apiary's whole result comment — a `Workflow: … — ✓ Done` header, the workflow
memory, then the output — which reads well on an issue and badly in a thread.

Replies are posted as Block Kit. Prose becomes mrkdwn sections — links, bold,
headings, bullets, blockquotes converted; fenced code left alone. A Markdown
table (`| a | b |` rows with a `|---|---|` separator) becomes a real
[table block](https://docs.slack.dev/reference/block-kit/blocks/table-block/):
first row as header, cells with links, `code` or **bold** kept as rich text.
Slack's ceilings are respected by splitting: 50 blocks and 10,000 table
characters per message, 100 rows and 20 columns per table (the header row is
repeated). An empty output posts nothing.

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
