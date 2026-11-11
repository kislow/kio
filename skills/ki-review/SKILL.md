---
name: ki-review
description: Review one pull request the way the user does - run what the change touches, check order, siblings, blast radius and claims, block only on a traceable failure - and print the review as a draft. Posts one review only with --post.
argument-hint: <pr-number-or-url> [--post]
disable-model-invocation: true
ki-role: judgement
ki-tools:
  - gh pr view
  - gh pr diff
  - gh pr list
  - gh pr checks
  - gh issue view
  - gh repo view
  - gh run list
  - gh run view
  - git fetch origin
  - git show
  - git log
  - git diff
  - git grep
  - bash -n
  - shellcheck
  - terraform fmt -check
  - Bash(terraform -chdir=* init -backend=false *)
  - Bash(terraform -chdir=* validate*)
  - helm lint
  - helm template
  - go vet
  - go build
  - python3 -m py_compile
  - actionlint
  - ki status
allowed-tools: mcp__ki-kb__get_context mcp__ki-kb__decisions_for mcp__ki-kb__search Bash(gh pr view *) Bash(gh pr diff *) Bash(gh pr list *) Bash(gh pr checks *) Bash(gh issue view *) Bash(gh repo view *) Bash(gh run list *) Bash(gh run view *) Bash(git fetch origin *) Bash(git show *) Bash(git log *) Bash(git diff *) Bash(git grep *) Bash(git worktree add --detach *) Bash(git worktree remove *) Bash(bash -n *) Bash(shellcheck *) Bash(terraform fmt -check *) Bash(terraform -chdir=* init -backend=false *) Bash(terraform -chdir=* validate*) Bash(helm lint *) Bash(helm template *) Bash(go vet *) Bash(go build *) Bash(python3 -m py_compile *) Bash(actionlint *) Bash(ki status *) Bash(ki vault push *)
---

Review pull request $ARGUMENTS. Without `--post` this is a draft: print it and stop after step 5. With `--post`, post it in step 6.

## 1. Read the change

- `gh pr view <n> --json number,title,body,author,baseRefName,headRefName,headRefOid,files,url` and `gh pr diff <n>`.
- Get the head commit on disk without touching the user's working tree: if `HEAD` is already `headRefOid`, use the checkout; otherwise `git fetch origin pull/<n>/head` and `git worktree add --detach "${TMPDIR:-/tmp}/ki-review-<n>" <headRefOid>`. Run every check in step 3 there. Remove the worktree at the end.
- Read the surrounding code for anything the diff alone does not explain: callers, the workflow that runs the changed file, the module that consumes a changed output.
- The ticket: the `#N` in the title or branch. `gh issue view N` for its acceptance criteria, and note when the number points at an unrelated issue.
- Siblings: `gh pr list --repo <owner>/<repo> --state open --json number,title,headRefName,files` in this repo (always pass `--repo`, the checkout's remote may not be GitHub), and `gh pr list --repo <owner>/<other> --search "<ticket or keyword>" --state all --json number,title,state,mergedAt` in the repo the change depends on. Look for an open PR touching the same file or block, and for a sibling this one needs merged or applied first.

## 2. Load the rules

- The coding standards described in `<harness>/skills/ki-resolve/standards.md` (`harness` from `ki status --json`).
- The repo's own rules: `AGENTS.md`, `CLAUDE.md`, `CONTRIBUTING.md`, lint configs, and the existing pattern next to the change. A convention is evidence only when you can cite the file and line that states or shows it.
- Optional, when the `ki-kb` tools are there: `get_context` with the PR title, `decisions_for` on each component the PR changes, `search` for past reviews of similar code to match the user's tone. Skip silently when they are not.

## 3. Run it

Run what the change touches instead of only reading it, then cite what ran and what it printed. Checks are read-only on anything outside the worktree: never call a cloud API, run `aws` or `kubectl` other than their local `help`/`--help`, run `terraform plan` or `apply`, deploy, or write to a remote.

| Changed | Run, on the changed files or directories |
| --- | --- |
| `*.sh`, inline `run:` bash | `/bin/bash -n <file>` (the macOS 3.2 binary), `shellcheck <file>` |
| `*.tf`, `*.tfvars` | `terraform fmt -check -diff <dir>`, `terraform -chdir=<dir> init -backend=false -input=false`, `terraform -chdir=<dir> validate` |
| `charts/**`, `values*.yaml` | `helm lint <chart> -f <values>`, `helm template <chart> -f <values>` per changed values file |
| `*.go` | `go vet`, `go build` on the touched packages; `gofumpt -l`, `golangci-lint run` if installed. `go test` only on your own PRs: it runs the PR's code with your credentials |
| `*.py` | `python3 -m py_compile <file>`; the module's tests if they run offline |
| `.github/workflows/*` | `actionlint <file>` |
| A script, filter or `jq`/`yq` expression a workflow feeds | Run it on the exact input the workflow gives it: the changed paths, the event payload shape, the file it reads |

- A tool that is not installed or a check that needs credentials: say which, and move on.
- The PR body: re-run each claim that says something was tested, verified or measured. When one does not reproduce, or the body describes commits that are not on the branch, that is a comment (push them, or fix the body), not a block.
- Semantics that only show at run time (does this CLI read stdin, does this API accept that field, what does this action return): check the tool's own local help or the provider schema, or run it against a local input. Name the version you checked.
- When CI is green but your run fails, find why CI did not catch it (it never ran on this path, a different invocation, a skipped job).

## 4. Review, in this order

1. **Does it run.** Step 3's results. A command that can never succeed, an output that never gets a value, a script that errors on the input it will get.
2. **Order and CI coverage.** Merge order, apply order, deploy order. Does CI plan or test this path at all (read the workflow triggers and the plan or path detector, and run the detector on this PR's paths)? What does merging trigger: an auto-apply, a replay of saved plans, a deploy to more environments than intended? What has to be run by hand after merge, and does a stale plan or an unapplied sibling break it?
3. **Siblings and the ticket.** Collisions with open PRs on the same file or block, a sibling PR in another repo this one depends on or contradicts, and whether the change does what the ticket's acceptance criteria and the PR title say, no more and no less.
4. **Blast radius.** For each new grant, read what the API returns, not what the action is called: a `Get*` that returns a secret header, a `List*` that exposes keys, an account-wide `Resource: "*"`. Flag settings that cannot be undone (compliance-mode locks, key deletion, retention), wildcards in trust policies, account IDs outside the known set, and anything reachable from more clusters, repos or branches than needed.
5. **Text against code.** Every comment, README line, doc and PR-body claim the change touches or makes stale: `git grep` the old name or value across the repo. A claim that overstates what the code guarantees is a comment.
6. **Conventions.** Only those you can cite by file and line (step 2). Plus correctness the steps above did not cover: edge cases, error handling, a missing test where the repo has a harness for it.

Most reviews do not block: expect about one PR in three. What blocks, and nothing else: a failure you can state in one sentence with its mechanism, introduced by this PR. Typical: it can never succeed; damage goes live before validation; a merge or apply ordering problem that only surfaces at apply; a grant that leaks a secret; an irreversible setting with no recorded decision; the change does not do what its title or ticket is for. Scope the block: when it only bites at a later flag flip or apply, block that step and say merging as inert code is fine.

What does not block: anything not introduced by this PR (mention it once, as such), failures that are loud and low-severity, choices you would have made differently, missing tests alone, doc drift. Formatter-only issues get one line in the review body with the exact command that fixes them.

Before drafting, try to refute each finding against the code at the head commit: state its failure path in one sentence or drop it. When the answer needs a fact you cannot check from here, ask it as a question that names the decision. Keep a short list of what you checked and found fine, so nobody re-derives it.

## 5. Draft

Print, and without `--post` stop here:

- **Event**: `REQUEST_CHANGES` only when at least one finding blocks. `APPROVE` when nothing asks for a change and the checks you ran pass (say if CI status could not be read). `COMMENT` otherwise.
- **Ran**: each check with its result in one line (`shellcheck scripts/x.sh: clean`, `terraform validate infra/dns: 1 error, line 14`), and the checks you could not run.
- **Inline comments**: `path:line`, then the body. Every point that asks for a change is an inline comment; anchor it on the nearest changed line when the cause sits outside the diff, and say where the cause is.
- **Review body**.
- **Review JSON line**: with and without `--post`, directly before the verdict line, one line of its own: `REVIEW-JSON: {"event":"COMMENT","body":"<review body>","comments":[{"path":"dir/file.tf","line":42,"body":"<comment>"}]}`. `event` is `APPROVE`, `COMMENT` or `REQUEST_CHANGES` and matches the verdict line. `body` and every comment `body` are the printed text, JSON-escaped on one line (newlines as `\n`). `line` is the RIGHT-side line of the diff at the head commit, the anchor `--post` uses. A caller posts this line as is, so no other line starts with `REVIEW-JSON:`.
- **Verdict line**: end the printed review, with and without `--post`, with exactly one line of its own, `VERDICT: APPROVE`, `VERDICT: COMMENT` or `VERDICT: CHANGES`, matching the event (`APPROVE`, `COMMENT`, `REQUEST_CHANGES`). Callers match `^VERDICT: (APPROVE|COMMENT|CHANGES)[ \t\r]*$` and take the last such line, so it is a machine-read contract: nothing but the run's status line may follow it, and no other line may start with `VERDICT:`. Only the `REVIEW-JSON:` line comes right before it.

Inline comment voice:

- One finding per comment. Open on the fix as an imperative, or on what breaks in plain words. No preamble, no restating the diff, and never open with a verdict such as "This blocks".
- The mechanism with real identifiers in backticks: variable, resource, API and job names, concrete counts. A line number only where it points somewhere the anchor does not.
- Evidence: the two or three lines of output that prove it, in a fenced block, or one sentence saying what you ran and what it returned. No tables, no full transcripts.
- The fix as a code block when it is more than a word, with the real values from the repo, never placeholders. Or name the alternative and its cost ("either X, which ..., or Y, which ...").
- Around 90 words, rarely past 130; a small point can be two sentences. Short spaced paragraphs. Cut the second example, the follow-up checklist and the exhaustive list of cases.
- Peer register, not a report: write it the way you would type it to a colleague, plain words, comma splices and run-ons are fine, a lowercase continuation after a comma is fine, an occasional trailing `....` is fine. Not every sentence needs to be complete or polished, and not every comment has the same shape.
- Never: em dashes (use a comma, colon or full stop), "I think", "it seems" or hedging adjectives, severity labels (`BLOCK`, `Callout:`, `Nit`), emoji, praise. Name uncertainty exactly ("could not reproduce this on terraform 1.14").
- A blocking comment says what exactly it blocks (the merge, or only the apply or flag flip) in its last sentence, in plain words ("blocking the apply on this one, merging is fine").

Before printing, search the draft for the em dash character and rewrite each one with a comma, colon or full stop.

Review body: the verdict first in one line (how many blockers, how many smaller points); what you ran and verified fine, with specifics; commands to run after merge if any; on a security or bootstrap surface, "second reviewer recommended" with the reason; formatter one-liners. Close a long review with `hope these help`. An approval starts with `LGTM` and one sentence of reason, or `LGTM, second reviewer recommended: <reason>` on a security surface.

## 6. Post, only with --post, then remember

- Run the self-check again on the final text: no em dash, every comment states its failure path.
- Post one review: `gh api repos/{owner}/{repo}/pulls/{number}/reviews -X POST --input <json>` with `event`, `body` and `comments` (`path`, `line`, `side: "RIGHT"`, `body`). If GitHub rejects a line, move that comment into the body and submit once more. Report the review URL.
- Then save the review as posted, if `ki status --json` shows `identity.vault` and `vault_writable` both true: `$KI_VAULT/reviews/<owner>-<repo>/PR <number> <Short Topic>.md` (Title Case with spaces) with frontmatter `type: pr-review`, `repo`, `pr`, `date`, `verdict`, and `edited: true` if the user changed the draft (otherwise `false`). Then `ki vault push -m "review <owner>/<repo>#<number>"`; if it stops on a conflict, show its message. Skip this silently otherwise.

## Rules

- Never merge, never edit, push to or close the PR, never switch the gh account. The only write is the one review in step 6.
- Nothing posted mentions ki, the harness, the knowledge base, decision records, brains, agents or AI. Explain everything in terms of the code. No attribution line.
- Remove the temporary worktree from step 1 when done.
