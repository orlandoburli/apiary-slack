# How it works

## One process per call

Apiary runs a plugin as a single-shot process: one JSON request on stdin, one
JSON response on stdout, exit. Nothing survives in memory, which decides the
whole design:

- The plugin cannot hold a socket open, so it does not use Socket Mode or the
  Events API. It reads with `conversations.history` and
  `conversations.replies` — outbound HTTPS, on Apiary's poll interval. This is
  also Apiary's own rule: it is polling-only and never listens for inbound
  traffic.
- What has been read lives in `state_file`: one cursor per conversation and
  the set of watched threads. The host's poll `since` cannot stand in — the
  daemon keeps it in memory and resets it on restart.

## A poll, step by step

1. **Identify the bot** (`auth.test`), once; cached in the state file.
2. **Resolve what to listen to**: the configured channels, plus the bot's open
   DMs when `direct_messages` is on.
3. **Channel pass.** For each conversation, read top-level messages newer than
   its cursor. A conversation seen for the first time gets its cursor set to
   *now* and emits nothing — installing the plugin never answers old messages.
   A message qualifies when a person wrote it (no bots, no joins/edits, not the
   bot itself), the author is in `allowed_users`, and — in a `mentions`
   channel — it mentions the bot. A qualifying message becomes a `turn:first`
   item and its thread starts being watched.
4. **Thread pass.** For each watched thread, read the thread and emit each
   qualifying reply not yet seen as a `turn:reply` item carrying everything
   before it as a transcript.
5. **Save the state file**, atomically.

Cursors advance at emission, not on acknowledge. Item ids are stable, so
Apiary's dedup already covers a re-read; waiting for an ack would re-emit the
same message on every poll until its run was dispatched.

`max_per_poll` bounds a poll. When the budget runs out the cursor stays just
before the first unread message, so the next poll resumes in order.

## Why each turn is its own item

Plugin sources are read-only to Apiary's workflow engine: they cannot feed
`resume_on`, event triggers, or `wait_for`, so a reply cannot wake a task that
is parked waiting for one. Instead of one long-lived task per conversation,
each human turn is a fresh item and a fresh run, and the conversation's memory
is the transcript in the item body.

That has a useful property: a run never blocks a conversation. Two people can
be mid-thread with the bot at once, and a daemon restart loses nothing — the
thread itself is the state.

## Answers

`write_result` receives the item back, reads the channel and thread out of its
id (`slack:<channel>:<thread_ts>:<ts>`), and posts there. The id is
self-sufficient on purpose, so the reply never depends on metadata surviving
the round trip. A top-level DM is answered in line; everything else in the
message's thread.

`acknowledge` adds `ack_reaction` to the message, so the person sees it was
picked up before the answer is ready. A failed reaction is logged, never
surfaced — a lost answer is.

## Failure behaviour

- One unreadable channel (bot not invited) is logged and skipped; the others
  are still read. The error is only returned when nothing at all was emitted.
- A rate limit (HTTP 429) or an expired deadline ends the poll at once; what
  was already read is saved and returned.
- A corrupt state file is an error, not a reset: silently starting over would
  drop every watched thread and hide the corruption behind conversations that
  quietly stop getting answers.
