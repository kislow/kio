---
name: ki-issue
description: Turn a rough note into a well-formed GitHub issue with acceptance criteria, labels and an estimate, checked for duplicates, and, when the project has a GitHub Projects board, add it to it.
argument-hint: <rough description of the work>
disable-model-invocation: true
allowed-tools: mcp__ki-kb__get_context mcp__ki-kb__search mcp__ki-kb__decisions_for Bash(gh issue list *) Bash(gh label list *) Bash(gh repo view *) Bash(ki status *)
---

Create a GitHub issue in the current repository from this rough input: $ARGUMENTS

## 1. Gather context

- Run `ki status --json` for the project name and its board (`board`, null when the project has none: the issue is then created without one).
- When the `ki-kb` tools are offered, call `get_context` with the rough input, and `decisions_for` on any component it names; skip silently when they are not.
- Look for duplicates and related work: `gh issue list --state all --search "<3-5 key words>" --limit 20 --json number,title,state,url`.
- Get the available labels: `gh label list --json name,description`.

## 2. Draft

- **Title:** imperative, specific, under 70 characters ("Retry upstream calls only on timeouts and 5xx").
- **Problem:** what is wrong or missing and who it affects, in two or three sentences.
- **Proposed approach:** only if the context makes one clearly right; otherwise leave it open.
- **Acceptance criteria:** a checklist of observable outcomes, each one testable.
- **Labels:** only existing labels.
- **Estimate:** S, M or L, following any estimation convention in the context; otherwise S is under a day, M is a few days, L should be split.
- **Related:** links to related or duplicate issues and to relevant past decisions, described in plain terms.

## 3. Confirm

Show the user the draft and any likely duplicates. If an open issue already covers it, suggest commenting on that one instead. Wait for approval or edits.

## 4. Create

- `gh issue create --title "<title>" --body-file <file> --label <label>...`, adding `--project "<board>"` only when `board` is set.
- If a board is set and adding to it fails with a scope error, tell the user to run `gh auth refresh -s project` once.
- Report the issue URL.

## Rules

- Never mention the knowledge base, ki, brains or decision records in the issue. Describe everything in terms of the product and the code.
