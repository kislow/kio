---
name: ki-resolve
description: Resolve one GitHub issue end to end, following your coding standards and the knowledge base's conventions and past decisions, and open a PR (ready for review unless the brief asks for a draft).
argument-hint: <issue-number-or-url>
disable-model-invocation: true
allowed-tools: mcp__ki-kb__get_context mcp__ki-kb__decisions_for mcp__ki-kb__search Bash(gh issue view *) Bash(gh repo view *) Bash(gh project *) Bash(git status *) Bash(git diff *) Bash(git log *) Bash(ki status *)
---

Resolve GitHub issue $ARGUMENTS in the current repository.

## 1. Understand the issue

- Run `gh issue view $ARGUMENTS --json number,title,body,labels,comments,url`.
- Its Done-when list is the acceptance criteria. If there is none, treat the issue as too vague.
- If it is too vague to implement safely (no clear outcome, conflicting comments), stop and ask one focused question instead of guessing. When running as a subagent, return `BLOCKED: <question>` instead.

## 2. Load context before planning

- Run `ki status --json` and note `harness`, `project` and `board`.
- When the `ki-kb` tools are offered, call `get_context` with the issue title plus one line on what you are about to change; skip silently when they are not.
- When the `ki-kb` tools are offered, call `decisions_for` on each component you will touch. If the issue asks for something that contradicts an active decision, say so before writing code.
- Load the coding standards for every language you will touch, as described in [standards.md](standards.md). They override general conventions.

## 3. Plan

Write a short plan: files to change, approach, tests to add. If the change is large (roughly more than 400 changed lines, or more than one service), show the plan and wait for a go-ahead. Otherwise continue.

## 4. Implement

- If `board` is set, move the issue's card to In progress, following [board.md](board.md).
- `git fetch origin` and branch from the default branch as `<type>/<issue-number>-<short-slug>`, where type is feat, fix, chore, docs or refactor.
- Make the change in the style of the surrounding code and your standards. Add or update tests.
- Run the repo's own test and lint commands (Makefile, package.json, pyproject, go.mod), plus the linters your standards name. Fix failures you caused; report pre-existing ones rather than fixing unrelated code.

## 5. Open the PR

- Commit, push the branch, and run `gh pr create` with an imperative title and a body covering summary, approach, how it was tested, and `Closes #<issue-number>`.
- Open it ready for review. Add `--draft` only when the brief says draft, for example a change the planner wants to look at before reviewers are notified.
- If `board` is set, move the card to In review.
- Report the PR link, plus any decision made during the work that should be recorded with `/ki-decision`.

## Rules

- Never mention the knowledge base, ki, brains, context packs, standards files or decision records in code, commits, branch names or PR text. Explain reasoning in terms of the code and the problem.
- No attribution lines: no `Co-Authored-By`, `Generated with` or session links in commit messages or the PR body.
- Never push to the default branch, and never merge.
