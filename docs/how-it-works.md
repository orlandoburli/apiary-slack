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
   channel — it mentions the bot. A qualifying message opens a conversation:
   its thread starts being watched. In a DM the channel itself is the
   conversation, and each qualifying message is a new turn in it.
4. **Thread pass.** For each watched thread, read the thread and record the
   qualifying replies not yet seen as new turns.
5. **Emit** one item per conversation whose latest turn is newer than the one
   Apiary last acknowledged: state `pending`, description = the conversation
   so far plus the unanswered message(s).
6. **Save the state file**, atomically.

Whether a turn has been answered is read from the conversation itself. When a
conversation becomes pending the plugin notes the turn it is handing over; the
next bot message newer than that turn means it is done, and the conversation
stops being pending until someone writes again. A turn that arrives while a run
is in progress stays pending — Apiary's live-instance guard holds it until the
run ends, and the next dispatch carries it. Apiary's `acknowledge` (sent only
when `settings.state_lock` is on) marks the same thing earlier when it arrives.

A run that never replies would otherwise re-run on every poll; after 120
emissions of the same turn (about half an hour at 15s polls) the plugin gives
up on it and logs that.

`max_per_poll` bounds how many conversations are emitted per poll; the rest are
emitted next time, in a stable order.

## Why the thread is the task

Plugin sources are read-only to Apiary's workflow engine: they cannot feed
`resume_on`, event triggers, or `wait_for`, so a reply cannot wake a task that
is parked waiting for one. Instead, the conversation is one item that comes
back as `pending` on every turn, and Apiary — which refreshes a bound task's
description and state from the item on every poll, and re-dispatches an item
whose earlier instance completed — runs the workflow once per turn on the same
task. One task per thread, one instance per turn.

That has a useful property: a run never blocks a conversation, and a daemon
restart loses nothing — the thread itself is the state.

## Answers

`write_result` receives the item back, reads the channel and thread out of its
id (`slack:<channel>:<thread_ts>`), and posts there. The id is
self-sufficient on purpose, so the reply never depends on metadata surviving
the round trip. A top-level DM is answered in line; everything else in the
message's thread.

`acknowledge` also adds `ack_reaction` to the latest turn, so the person sees
it was picked up before the answer is ready. A failed reaction is logged, never
surfaced — a lost answer is.

## Failure behaviour

- One unreadable channel (bot not invited) is logged and skipped; the others
  are still read. The error is only returned when nothing at all was emitted.
- A rate limit (HTTP 429) or an expired deadline ends the poll at once; what
  was already read is saved and returned.
- A corrupt state file is an error, not a reset: silently starting over would
  drop every watched thread and hide the corruption behind conversations that
  quietly stop getting answers.
