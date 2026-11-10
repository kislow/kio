---
name: ki-recap
model: haiku
description: Draft the daily standup (done since the last working day, today, blockers) for one GitHub identity from its activity, project board (when it has one) and project notes. Prints text to paste; never posts.
argument-hint: "[identity] [--since YYYY-MM-DD]"
allowed-tools: mcp__ki-kb__search mcp__ki-kb__get_context Bash(ki recap *) Bash(ki status *) Read
ki-role: writing
ki-tools:
  - ki recap
  - ki status
---

Draft a standup from: $ARGUMENTS

## 1. Collect, for exactly one identity

- Run `ki status --json`; note `identity.name` (the session's identity, may be null) and `vault_dir`.
- If the arguments name an identity (for example `work` or `me`), pass it as `--identity <name>`; pass `--since <date>` through. Then run `ki recap --json [--identity <name>] [--since <date>]`.
- If it fails asking for `--identity`, ask the user which of the listed identities, once. If it refuses an identity, say so and stop: do not try another way to read that account.
- If the user wants several identities, run once per identity and write a separate draft for each. Never merge two reports into one draft, and never carry an item, a note or KB context from one into the other.

The report holds `done` (items with what was done to them inside the window), `open` (open PRs and assigned issues updated since two weeks before the window starts; older ones are left out), `board` (cards assigned to the user in the board's current iteration, with `status`, `status_changed_at`, `changed_in_window`, `labels` and `fields` such as a blocker reason), `vault_notes` (project notes changed in the window), `dropped` and `warnings`. `ki recap` already reads GitHub as the identity's own account and drops repos that belong to other identities; work only from what it returns.

## 2. Understand what the work was about

- From `vault_notes` (paths relative to `vault_dir`), read at most five: the ones whose names match items in the report. Read no other vault files. A note created in the window can be work in itself (a runbook or design written); count it under the first section only when nothing in GitHub covers it.
- Only when the `ki-kb` tools are offered and `identity.name` from `ki status` equals the report's `identity`: call `search` (or `get_context`) with the titles of the two or three biggest items to learn why they mattered. In a session of another identity, skip the knowledge base entirely, even if tools are available.
- Notes and the knowledge base explain items that are in the report. They never add an item.

## 3. Draft

Three sections, in this order. The window is the report's `since`: call the first section "Yesterday", or "Since Friday" when `since` is a Friday and today is Monday.

1. **Yesterday / Since Friday:** what got done. Merged PRs and closed issues first, then reviews, then opened PRs and issues. Group items that serve one goal into one bullet that names the outcome, not the steps (five deploy bumps are one line). Skip comment-only items unless the comment was the work (a design review, an incident thread). Cards whose status moved in the window (`changed_in_window`) count here when they moved to a done-like status.
2. **Today:** board cards with an in-progress status (for example "In development", "Testing", "Reviewing"), then open PRs that need a push to land (draft PRs, PRs with requested changes), then the next "Todo" cards of the current iteration if there is room. Without a board, use `open`. At most five bullets.
3. **Blockers:** cards with a blocked status, a `blocked` label or a blocker reason field, each with the reason in one clause and who or what it waits on. When a card records no reason, write `[reason]` for the user to fill in rather than guessing. Write "None" when there are no blockers; never invent one.

Every bullet traces to at least one item in the report. Refer to work by repo and number (`kio#112`) or by its outcome; paste URLs only when the user asks.

## 4. Voice

Write it as the user would post it in Slack.

- Concise: one line per bullet, the outcome first. Cut every word that carries no weight.
- Mechanism over opinion: name the resource, route, service or failure, not "worked on auth".
- No hedging ("I think", "seems", "hopefully"), no filler ("just", "quick"), no marketing words ("robust", "seamless", "leverage").
- No emoji. Sentence case. Company names as written, for example "Acme", never "ACME".
- Slack formatting: `*Yesterday*` style labels, `•` bullets, backticks for paths, keys and commands.

## 5. Show it

Print the draft in a fenced `text` block so it pastes cleanly. Below it, outside the block, list in one line the items you left out on purpose, so the user can add any back, and repeat any `warnings` from the report.

## Rules

- Draft only. Never post to Slack or anywhere else, never comment on GitHub, never move a card, never write to the vault.
- Never say how the draft was made: no mention of the knowledge base, brains, vault or this skill. Work on those repos themselves is ordinary work and is named like any other.
