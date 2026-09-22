# Limits

## Latency is the poll interval

An answer cannot start before the next poll, so a message waits up to
`poll_interval`, then for a worker, then for the agent. This is a colleague who
checks Slack every half minute, not a chat bot that types back.

## Slack rate limits

One poll costs **1 call per listened conversation + 1 per watched thread**
(plus 1 to list DMs, and 1 per emitted DM for its history). Reads are Tier 3,
roughly 50 calls a minute per method, so at `poll_interval: 30s` keep
conversations + watched threads under ~25. `thread_ttl` and `max_threads` are
the levers.

Slack rate-limits `conversations.history` / `conversations.replies` far harder
(1 call a minute) for *commercially distributed apps that are not in the
Marketplace*. An app you create for your own workspace is an internal custom
app and is not affected — create it in the workspace you use it in.

On a 429 the poll stops immediately and resumes from the same cursors next
interval; nothing is lost.

## Conversations

- **In a `mentions` channel, only people who already addressed the bot in a
  thread continue it without a mention** (`thread_replies: participants`).
  Anyone else's reply is ignored until they mention the bot.
- **Only threads the bot opened a conversation on are watched.** Mentioning
  the bot in the middle of an existing human thread is not seen:
  `conversations.history` returns top-level messages only.
- **Threads stop being watched** `thread_ttl` after their last qualifying
  reply, or when `max_threads` is exceeded. A reply after that is ignored;
  mention the bot in a new message.
- **Replies inside a DM thread are not read.** The bot answers DMs in line, so
  DM threads do not arise on their own.
- **Edits and deletions are ignored.** A message is read once.
- **Files, images and Block Kit content are not passed on** — only message
  text. User mentions stay as `<@U…>` ids.
- **Each turn is a fresh run on the same task.** The agent knows the
  conversation from the transcript only, capped at `transcript_limit` messages
  of 4000 characters.
- **The trigger must be `states: [pending]` and not `once`.** Get either wrong
  and the thread is dispatched every poll, or only its first turn ever runs.

## Read-only source

Like every plugin source, items cannot host `set_state`, label steps,
`wait_for`, `materialize: sub_issue`, or approvals with approvers — `apiary
validate` rejects them when the trigger pins `match.source`. **Always pin it**:
in a hive that also has a GitHub or Jira source, an unpinned workflow using
those steps validates clean and then silently does nothing for Slack items.

An operator-gate approval (a `message`, no approvers) is allowed, answered with
`apiary approvals` or the dashboard — not from Slack.

## Security

- Anyone who can message the bot can drive an agent. Set `allowed_users`.
- Message text is untrusted input. Give Slack workflows a runner with only the
  permissions a chat needs (for Claude, `permission_mode: plan` is read-only).
- What a workflow publishes is posted to the channel the message came from —
  do not route Slack items to agents that may quote secrets.
- The token is read from `SLACK_BOT_TOKEN` only and is never written to the
  state file or to logs.
