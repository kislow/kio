---
name: ki-rebase
description: Rebase a pull request branch onto its base branch, resolve conflicts keeping both sides' intent, and push it with a lease. Runs headless through ki skill run.
argument-hint: <base-branch>
disable-model-invocation: true
ki-role: execution
ki-agents: [claude]
ki-tools:
  - Edit
  - git status
  - git fetch
  - git rebase
  - git -c core.editor=true rebase --continue
  - git add
  - git rm
  - git log
  - git diff
  - git cherry
  - git rev-parse
  - git show
---

Rebase the branch this run was started on onto its base branch and push it. Below, `<base>` is the argument (`main` when none is given), `<branch>` the branch on origin and `<start>` the SHA the worktree is detached at; the last two are in the run's workspace line. When that line names no branch (a run on a pull request number), end `BLOCKED: run it with --checkout <branch>, the pull request number does not name the branch to push`.

The caller must pass `--allow "git push --force-with-lease"`: without it the push in step 4 is denied, so stop at step 3 and end `BLOCKED: rebased locally, the run was not allowed to push`.

## 1. Fetch

- `git fetch origin <base> <branch>`. Never rebase onto a local `main`: the worktree's local branches can be hundreds of commits behind origin.
- `git rev-parse origin/<branch>` must equal `<start>`. When it does not, someone pushed to the branch after the run started, and a lease on the newer tip would overwrite their commits: end `BLOCKED: <branch> moved on origin since the run started, rebase again from its new tip?`
- `git cherry origin/<base> origin/<branch>` prints one line per commit on the branch, `-` when the base already has an equivalent change and `+` when it does not. When it prints only `-` lines, the branch is superseded: push nothing and end with the line in step 5.
- `git log --oneline HEAD..origin/<base>` prints nothing when the branch already contains the base: print `DONE: already up to date` and stop.

## 2. Rebase

`git rebase origin/<base>`. A clean rebase goes to step 4.

## 3. Conflicts

For each file `git status` lists as unmerged:

- Read the conflict markers, then `git log -p -n 3 origin/<base> -- <file>` to see what the base changed and why. `git show REBASE_HEAD` shows the branch's own commit being replayed.
- Edit the file so both sides' intent survives: the base's change and the branch's change both still work. A rename or deletion on one side is applied to the other side's edits (`git rm` for a file the base deleted that the branch only touched trivially). Never resolve by taking one side wholesale, and never leave a conflict marker.
- `git add <file>`, and when no unmerged files are left, `git -c core.editor=true rebase --continue`. Never plain `git rebase --continue`: it opens an editor that nobody can answer.

When a conflict is not clear (both sides rewrote the same logic, a semantic clash the markers do not show, a file you cannot read the intent of), `git rebase --abort` and end `BLOCKED: <files>: <the one question about what the merged result should do>`. Nothing is pushed.

After the last `--continue`, `git diff origin/<base>...HEAD --stat` should list only the branch's own files; when it lists others, abort and end `BLOCKED` naming them.

After any rebase, clean or resolved, `git diff origin/<base>...HEAD` printing nothing means the branch now equals the base: the rebase stays as it is locally, push nothing and end with the line in step 5.

## 4. Push

`git push --force-with-lease origin HEAD:refs/heads/<branch>`

This form on purpose. The explicit lease, `--force-with-lease=<branch>:<start>`, is not matched by the caller's `Bash(git push --force-with-lease:*)` rule and would be denied. The bare lease compares against `origin/<branch>`, which step 1 checked equals `<start>`, so a push by someone else between the fetch and now is refused (`stale info`). End `BLOCKED: the push was refused because <branch> moved, rebase again?` when it is. Any other push form, and `--force`, are never used.

Then print exactly one line, using `git rev-parse --short HEAD`:

```text
pushed <new short sha> to <branch>
```

and after it `DONE: rebased <branch> onto origin/<base>`.

## 5. Superseded

When step 1 or the end of step 2 or 3 finds every change on `<branch>` already in `origin/<base>`, print exactly one line and stop, with no push (a force-push would empty the pull request):

```text
DONE: superseded: every change on <branch> is already in origin/<base>, nothing pushed
```

## Rules

- The only write to the remote is the one push in step 4, to `<branch>`. Never push to `<base>`, create a branch or tag, or push after a step ended `BLOCKED`.
- Do not change anything the rebase did not require: no reformatting, no dependency bumps, no extra fixes. A conflict resolution is the minimum that makes both sides work.
- Nothing you write into the repository or a commit message mentions ki, the harness, agents or AI. Resolved commits keep their original messages, authors and dates.
