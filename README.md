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

**A thread is one Apiary task.** The item the plugin emits is the conversation
— a thread, or a DM channel — with a stable id (`slack:<channel>:<thread_ts>`),
so every turn binds to the same task and the dashboard shows one task per
thread with one workflow instance per turn. Plugin sources cannot resume a
parked task, so continuity travels in the item instead:

- The item's **state** is `pending` while a human turn awaits an answer and
  `answered` once Apiary has picked it up (the plugin records the dispatch when
  the host acknowledges the item). The workflow trigger matches
  `states: [pending]`, so each turn runs the workflow exactly once.
- The item's **description** is the thread so far — including the bot's own
  earlier answers — followed by the message(s) waiting for an answer.
- A DM is one running conversation: the whole DM is one task, answered in
  line rather than in a thread.

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
      # NOT once: the same item is dispatched again for every turn. `states`
      # is what stops it re-running once the turn has been picked up.
      match: {source: slack, states: [pending]}
    result_comment: on_fail
    steps:
      - id: answer
        agent: assistant
        prompt: |
          Answer the latest Slack message in the task body, briefly. Put the
          answer, and nothing else, between these markers:

          APIARY_PUBLISH_BEGIN
          <your answer>
          APIARY_PUBLISH_END
```

Only the published block is posted. `result_comment: on_complete` also works,
but it wraps the output in a `Workflow: … — ✓ Done` header plus the workflow
memory — right for an issue tracker, noise in a chat.

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
