---
name: ki-decision
description: Record a decision made in this session as a decision record, so future agents know what was decided and why.
argument-hint: [short topic]
disable-model-invocation: true
allowed-tools: mcp__ki-kb__decisions_for mcp__ki-kb__search Bash(ki status *) Bash(ki vault push *) Bash(git status *)
---

Record the decision about "$ARGUMENTS" (or, if empty, the most recent significant decision in this conversation).

## 1. Extract it

From the conversation, work out the context, what was decided, the alternatives that were rejected and why, and the consequences. If any of these is unclear, ask the user one question rather than inventing it.

## 2. Check what it replaces

When the `ki-kb` tools are offered, call `decisions_for` on the component it applies to (skip silently when they are not). If an active decision is being replaced, name it in `Supersedes`.

## 3. Draft and confirm

Show the user the record in this exact format and wait for approval or edits:

```markdown
# <short decision title>

Date: <YYYY-MM-DD>
Applies to: <service, component or repo>
Supersedes: <earlier decision title, or "none">

## Context
## Decision
## Alternatives rejected
## Consequences
```

## 4. Save it where it will be ingested

Run `ki status --json` to see which project this repo belongs to.

- If the project is `kio`, save to `decisions/<YYYY-MM-DD>-<slug>.md` in this repo, on a branch, and open a PR.
- Otherwise, if `identity.vault` and `vault_writable` are both true, save to `$KI_VAULT/projects/<project>/decisions/<YYYY-MM-DD> <Short Topic>.md` (Title Case with spaces, as the vault names its notes; kebab-case is only for this repo's `decisions/`), then run `ki vault push -m "decision: <short topic>"` (it commits, pulls with rebase and pushes the vault; if it stops on a conflict, show the user its message and stop). Never write decision records into the project repo itself.
- If `identity.vault` is false, do not save it anywhere; tell the user it belongs in that organisation's own systems and give them the text.
