---
name: ki-burndown
description: Burn down a batch of small GitHub issues in parallel, one delegated job per issue in its own worktree on whichever agent the machine's policy picks, each opening a PR, ready for review unless the user asked for drafts.
argument-hint: "[issue numbers | label:<name> | milestone:<name> | column:<name>] [--parallel N] [--agent A]"
disable-model-invocation: true
allowed-tools: mcp__ki-kb__get_context mcp__ki-kb__search mcp__ki-kb__decisions_for Bash(gh issue list *) Bash(gh issue view *) Bash(gh project *) Bash(gh pr list *) Bash(ki status *) Bash(ki delegate *)
---

Burn down small issues in the current repository: $ARGUMENTS

## 1. Select candidates

- Run `ki status --json` for `project` and `board`.
- Build the candidate list from the arguments:
  - issue numbers: use them as given;
  - `label:<name>` or `milestone:<name>`: `gh issue list --state open --label ...` or `--milestone ...`, `--json number,title,labels,assignees,body`;
  - `column:<name>`: open issues in that board column (see `<harness>/skills/ki-resolve/board.md` (`harness` from `ki status --json`) for finding the project and its items);
  - nothing: open issues in the board's Todo or Ready column when `board` is set, otherwise open issues labelled with a small size (`size:S`, `S`, `small`). Take at most 8.
- `--parallel N` sets how many run at once; default 4, maximum 6. `--agent A` names the worker instead of the machine's policy.

## 2. Keep only issues an agent can finish alone

Drop, and list with the reason, any issue that:

- has no clear outcome or acceptance criteria;
- is estimated larger than small, or would obviously touch more than one service;
- is labelled blocked, or is assigned to someone other than the current user;
- already has an open PR (`gh pr list --search "<number> in:body" --state open`).

## 3. Avoid collisions

For each remaining issue, when the `ki-kb` tools are offered call `get_context` with its title (skip silently when they are not), and predict the files or packages it will touch. If two issues will likely touch the same files, keep the smaller one in this run and defer the other to the next burndown; parallel branches from the same base would conflict.

## 4. Show the plan and wait

Present a table: issue, title, predicted area, and "run now" or "deferred: <reason>". Wait for the user's approval or edits. Nothing is dispatched before that.

## 5. Dispatch

Each approved issue is one delegated job, run by `ki` headless in its own git worktree on the agent the machine's execution role picks (`--agent A` from the arguments forces one). For each issue run:

```bash
ki delegate --json --issue <owner>/<repo>#<number> --task "Resolve issue #<number> in <owner>/<repo>. Follow <harness>/skills/ki-resolve/SKILL.md together with standards.md and board.md next to it. You are in your own git worktree on a branch of your own: stay in it, never touch the default branch or other issues, and open a PR ready for review<, or as a draft if the user asked for drafts>. If the change turns out larger than small, return BLOCKED: larger than expected: <why>."
```

Run at most N at a time where your agent can run commands in the background, starting the next as one finishes; otherwise one after another. Read each result's `status` and `detail`: `PR` with the URL, `DONE` without a PR (treat as a question: why no PR?), `BLOCKED` with the question, `FAILED` with the reason, `STALLED` when the worker went silent and ki ended it (its worktree and commits are kept: report the job id, do not re-dispatch the issue). `QUOTA` when the worker ran out of usage mid-job: report the job id and continue it with `ki delegate resume <job> --json` (another provider, same worktree), never by dispatching the issue again. `QUEUED` when `--when-available` was passed and no provider is available: the job starts by itself once one is back; report the job id and the expected start. A `LAUNCH_FAILED` means no provider could start at all: stop the run and report it with the job id. `--issue` claims the issue first (the `agent: working` label, kept until the job ends), so the job shows its issue in `ki delegate ls`. When ki refuses because the issue is already claimed, do not dispatch it again and never pass `--steal`: report it as "claimed" (the refusal names the issue, not a machine; `ki delegate ls` on this machine lists its own jobs) and move on. Never run two jobs for the same issue.

## 6. Report

When all have returned, give one summary table: issue, result (PR link, blocked question, failure, or "claimed"), and the next action. List the blocked questions together so the user can answer them in one go, and the deferred issues for the next run. Never merge anything.
