# apiary-slack

Slack conversations for [Apiary](https://github.com/orlandoburli/apiary), as an
out-of-process source plugin.

Mention the bot in a channel, write in a channel it owns, or DM it: the message
becomes an Apiary work item, a workflow runs, and whatever the workflow
publishes is posted back as a reply in the same thread. Reply in that thread
and the conversation continues.

**There are no changes to Apiary itself**, and nothing listens for inbound
traffic. The plugin reads Slack with plain outbound Web API calls on Apiary's
poll interval — no Socket Mode, no Events API, no public URL.

```
Slack message ──► poll ──► work item ──► trigger ──► workflow ──► agent
      ▲                                                             │
      └──────────────── write_result: reply in thread ◄─────────────┘
```

## How a conversation works

Plugin sources cannot resume a parked task, so a conversation is not one
long-lived task. **Every human turn is its own work item**; continuity travels
in the item:

- The message that opens a conversation is an item labelled `turn:first`.
- A reply in the thread is a new item labelled `turn:reply`, whose body carries
  the thread so far as a transcript — including the bot's own earlier answers —
  followed by the latest message.
- A DM is one running conversation: each message carries the DM's recent
  history, and the answer is posted in line rather than in a thread.

More in [How it works](docs/how-it-works.md).

## Slack app

Create an app at <https://api.slack.com/apps> (from scratch, in your
workspace), add these **bot token scopes**, install it, and copy the
`xoxb-…` bot token:

| Scope | For |
|---|---|
| `channels:history`, `groups:history` | reading public / private channels |
| `im:history`, `im:read` | direct messages (`direct_messages: true`) |
| `chat:write` | posting answers |
| `reactions:write` | `ack_reaction` (optional) |

For DMs, also enable *App Home → Messages Tab → Allow users to send messages*.
Invite the bot to each channel you list (`/invite @your-bot`).

Put the token in the daemon's environment — never in `apiary.yaml`:

```bash
echo 'SLACK_BOT_TOKEN=xoxb-…' >> .apiary/.env
```

## Install

```bash
make install DIR=/path/to/your/project/.apiary/plugins
```

That places the executable and its manifest in `<DIR>/dev.apiary.slack/`.
Optionally pin the binary's checksum so Apiary detects a swap-out:

```bash
make checksum DIR=/path/to/your/project/.apiary/plugins
```

Then verify Apiary sees it:

```bash
apiary plugins list
```

```bash
apiary validate
```

## Configure

```yaml
plugins:
  - id: dev.apiary.slack
    timeout: 30s
    config:
      state_file: /abs/path/to/.apiary/slack-state.json
      channels:
        - id: C0123456789
          mode: mentions      # or: all
      direct_messages: true
      allowed_users: [U01ABCDEF]

sources:
  - id: slack
    type: plugin
    poll_interval: 30s
    config: {plugin: dev.apiary.slack}

workflows:
  - id: slack-chat
    trigger:
      once: true
      match: {source: slack, labels: ["slack"]}
    result_comment: on_complete
    steps:
      - id: answer
        agent: assistant
        prompt: Answer the latest Slack message in the task body, briefly.
```

Every option is in [Configuration](docs/configuration.md); a complete hive is in
[examples/apiary.yaml](https://github.com/orlandoburli/apiary-slack/blob/main/examples/apiary.yaml).

> **Anyone who can message the bot can drive an agent.** Set `allowed_users`,
> and give Slack workflows a runner with only the permissions a chat needs.
> Message text is untrusted input to the agent.

## Limits

Read-only source rules apply — no `set_state`, labels, `wait_for`, or
approvals with approvers — and answers arrive on the poll interval, not
instantly. The full list is in [Limits](docs/limits.md).

## License

[BSD 3-Clause](LICENSE). See [COMMERCIAL.md](COMMERCIAL.md).
