# Skills

Ten skills turn the knowledge base into everyday speed. They follow the open Agent Skills format, so the same files work in Claude Code, Codex and Cursor. `make skills` links them into `~/.claude/skills` (Claude Code) and `~/.agents/skills` (Codex and Cursor), so they work in every repo without adding anything to it and update with `git pull`. Shared files are found through `ki status --json`, never through tool-specific paths.

| Skill | Input | Output |
| --- | --- | --- |
| `/ki-issue` | A rough note | A well-formed issue, on the project board when there is one, after approval |
| `/ki-resolve` | One issue | A PR that follows your standards and past decisions, ready for review unless you ask for a draft |
| `/ki-burndown` | A set of small issues | One PR per issue, built in parallel by delegated workers |
| `/ki-review` | One PR | A draft review in your own tone, checked by running what the PR touches; posted with `--post` |
| `/ki-debug` | A symptom | Root cause with evidence, a fix, and how to prevent it |
| `/ki-decision` | A topic | A decision record future agents will follow |
| `/ki-recap` | An identity, or the current repo's | A standup draft to paste: done, today, blockers |
| `/ki-diagram` | A subject or an existing drawing | A tldraw drawing in the vault and its PDF, rebuilt from a spec without losing hand edits |
| `/ki-rebase` | A pull request branch and its base | The branch rebased onto the base, conflicts resolved, pushed with a lease |
| `/ki-address` | One pull request with unresolved review threads | The comments that are right fixed and pushed, their threads replied to and resolved; the rest left open with a draft reply |

They form one loop: issues are created, resolved, reviewed, and what was learned flows back into the knowledge base.

```mermaid
flowchart LR
    note["rough note"] -->|/ki-issue| issue["issue on the board"]
    issue -->|/ki-resolve| pr["draft PR"]
    issue -->|/ki-burndown| prs["many draft PRs"]
    pr & prs -->|/ki-review| review["review in your tone"]
    symptom["failure or alert"] -->|/ki-debug| fix["fix + incident note"]
    pr -->|/ki-decision| dec["decision record"]
    issue & pr -->|/ki-recap| standup["standup draft"]
    subject["architecture to explain"] -->|/ki-diagram| drawing["tldraw drawing + PDF"]
    pr -->|/ki-rebase when behind the base| rebased["rebased and pushed branch"]
    pr -->|/ki-address for review comments| addressed["fixes pushed, threads resolved"]
    review & fix & dec & drawing --> vault[("vault")]
    vault -->|5-minute sync| kb[("knowledge base")]
    kb -.->|context, decisions, past incidents| issue & pr & review & fix
```

## Coding standards

`standards/common.md` holds the rules for every language, and `standards/go.md`, `python.md` and `bash.md` the rules for each. Every rule is tagged MUST or SHOULD. Retrieval can miss a rule, so these are never looked up by search: every skill that writes or reviews code loads `common.md` plus the file for each language it touches, in full. Rules in the standards beat general best practice; an active decision for a repo beats both, and the agent says when that happens. Mechanical rules live in linter config named in each file's Checks section, and `standards/templates/` has the configs and skeletons to copy into a repo.

```mermaid
flowchart LR
    diff["files the change touches"] --> lang{"language"}
    lang -->|.go| go["standards/go.md"]
    lang -->|.py| py["standards/python.md"]
    lang -->|.sh or shebang| sh["standards/bash.md"]
    common["standards/common.md"] --> apply
    go & py & sh --> apply["loaded in full before planning or reviewing"]
    apply --> checks["linters from each file's Checks section"]
```

## /ki-issue

```mermaid
flowchart TB
    a["rough note"] --> b["get_context, decisions_for"]
    b --> c["search existing issues for duplicates"]
    c --> d["draft: title, problem, acceptance criteria,<br/>labels, estimate, related work"]
    d --> e{"you approve or edit"}
    e -->|duplicate| f["comment on the existing issue instead"]
    e -->|approved| g["gh issue create, add to the board"]
```

## /ki-resolve

```mermaid
flowchart TB
    a["gh issue view"] --> b{"clear enough?"}
    b -->|no| q["ask one question<br/>(BLOCKED when a subagent)"]
    b -->|yes| c["get_context, decisions_for,<br/>load standards"]
    c --> d{"large change?"}
    d -->|yes| p["show plan, wait"]
    d -->|no| e["card to In progress, branch from default"]
    p --> e
    e --> f["implement, tests, repo linters and standards checks"]
    f --> g["draft PR with Closes #n, card to In review"]
```

## /ki-burndown

Each approved issue is one [`ki delegate`](ki-delegate.md) job: a headless worker in its own git worktree, on whichever agent the machine's `roles.execution` policy picks (Claude Code, Codex, Cursor or agy; `--agent` forces one), following `/ki-resolve` and answering with one line: `PR`, `DONE`, `BLOCKED` or `FAILED`. Workers never touch each other's files, so they run in parallel from any planner that can run commands in the background, and one after another elsewhere. Issues predicted to touch the same files are deferred to the next run instead of conflicting. A `BLOCKED` job is reopened with `ki mecca new <job>` to answer its question where it stopped.

```mermaid
flowchart TB
    pick["select: numbers, label, milestone,<br/>board column, or Todo by default"] --> filter["keep small, clear, unblocked,<br/>unassigned, no open PR"]
    filter --> overlap["predict files touched,<br/>defer overlapping issues"]
    overlap --> plan{"plan table, you approve"}
    plan --> pool["ki delegate per issue, the role policy picks the vendor,<br/>at most N at a time (default 4)"]
    pool --> r1["#101 PR"] & r2["#104 PR"] & r3["#107 BLOCKED: question"] & r4["#110 FAILED: reason"]
    r1 & r2 & r3 & r4 --> sum["one summary: PRs, questions to answer together, deferred issues"]
```

## /ki-review

`/ki-review <pr>` prints a draft; `/ki-review <pr> --post` posts it as one review with inline comments. It never merges, edits the PR or switches the gh account.

The printed review always ends with one `VERDICT: APPROVE`, `VERDICT: COMMENT` or `VERDICT: CHANGES` line (the event it would post: `APPROVE`, `COMMENT`, `REQUEST_CHANGES`), before the run's status line. Callers that store drafts read it, so the skill treats it as a contract.

Directly before the verdict line, with and without `--post`, it prints one `REVIEW-JSON: {"event":...,"body":...,"comments":[{"path":...,"line":...,"body":...}]}` line: the review as the payload `--post` would send. `event` matches the verdict, `body` and each comment `body` are the printed text JSON-escaped on one line, and `line` is the RIGHT-side line at the head commit. A caller that shows the draft can post that line on the user's word without running the review again.

Headless, `ki skill run ki-review <pr> --checkout <pr>` runs the draft on the agent the `judgement` role picks, allowed only the read and check commands its `ki-tools` lists. Posting needs the caller to add the tools: `ki skill run ki-review --checkout <pr> --allow "gh api" -- <pr> --post`.

```mermaid
flowchart TB
    a["gh pr view, gh pr diff, head commit<br/>in a throwaway worktree"] --> t["ticket, sibling PRs<br/>in this repo and the one it depends on"]
    t --> b["rule packs, repo conventions,<br/>get_context and decisions_for when available"]
    b --> r["run it: bash -n, shellcheck, terraform validate,<br/>helm lint/template, go vet/test, actionlint,<br/>re-run the PR body's claims"]
    r --> c["review in order: runs, order and CI coverage,<br/>siblings and ticket, blast radius,<br/>text against code, conventions"]
    c --> v{"blocks only on a one-sentence failure path"}
    v --> d["draft: event, what ran, inline comments, body"]
    d -->|--post| f["post one review"]
    f --> g["save to vault with edited flag<br/>(skipped for vault: false identities and when the vault is not a git clone)"]
```

The checks are read-only outside the throwaway worktree: no cloud API, `kubectl`, `terraform plan` or `apply`. A check whose tool is missing is named in the draft rather than skipped silently.

## /ki-debug

One skill, four playbooks. The method is the same everywhere (evidence before conclusions, one change at a time); the evidence and the risk differ, so each class has its own playbook in `skills/ki-debug/playbooks/`, loaded only when needed. Force a class with `/ki-debug ci <run URL>`.

```mermaid
flowchart TB
    sym["symptom: error, run URL, alert, finding"] --> past["search past incidents, runbooks, decisions"]
    past --> cls{"classify"}
    cls -->|code| code["reproduce, failing test,<br/>narrow down (bisect), fix cause"]
    cls -->|ci| ci["failed step logs, diff against last green run,<br/>drift, flake or permissions"]
    cls -->|sre| sre["impact and mitigation first, what changed,<br/>events, logs, saturation; read-only"]
    cls -->|security| sec["contain exposed secrets, confirm reachability,<br/>blast radius, fix at the right level"]
    code & ci & sre & sec --> out["root cause with evidence, fix (draft PR or proposed change),<br/>prevention"]
    out --> rec["incident note in the vault, or /ki-decision"]
```

Anything that changes a live system (apply, delete, restart, scale, rerun, rotate) is proposed with its blast radius and run only after you approve. Secret values are never printed.

## /ki-decision

```mermaid
flowchart TB
    a["extract context, decision, alternatives, consequences"] --> b["decisions_for: what does it supersede?"]
    b --> c{"draft shown, you approve"}
    c --> d{"where?"}
    d -->|kio| e["decisions/ in this repo, via PR"]
    d -->|vault allowed and a clone| f["vault: projects/name/decisions/"]
    d -->|vault: false| g["text handed to you for that org's own systems"]
```

## /ki-recap

`ki recap --json` collects the data and the skill only writes prose. One run covers one identity: the one named with `--identity`, else the current repo's. It reads GitHub with that identity's own token (`gh auth token --user`), never gh's active account, and stops if the token's login is not the identity's `github_user`. Items from repo owners that `projects.yaml` gives only to other identities are dropped, vault notes come only from `projects/<folder>/` folders named after this identity's projects or listed in their `vault_folders`, and no other identity's, and the skill uses the knowledge base only when the session's identity is the standup's. Identities marked `vault: false` are refused.

The window starts at 00:00 local on the previous working day, so Monday covers Friday to Sunday; `--since YYYY-MM-DD` overrides it.

```mermaid
flowchart TB
    id{"--identity, else the repo's identity"} -->|vault: false| stop["refused"]
    id --> token["gh auth token --user github_user,<br/>login checked"]
    token --> gh["PRs opened, merged, closed; reviews;<br/>issues opened, closed; comments; open work"]
    token --> board["board on the identity's projects:<br/>my cards in the current iteration"]
    id --> notes["notes in this identity's vault<br/>project folders, changed in the window"]
    gh & board & notes --> scope["drop repos of other identities"] --> report["ki recap --json"]
    report --> draft["/ki-recap: yesterday, today, blockers"]
    kb[("knowledge base,<br/>same identity only")] -.->|why it mattered| draft
```

Nothing is posted: the draft is printed for you to paste.

## /ki-diagram

The agent writes a spec (pages, boxes, groups, captions, lines) in the drawing's hidden `.ki-diagram/` folder and `ki diagram build` turns it into the drawing Obsidian opens. Drawings are edited by hand in Obsidian; a rebuild merges spec changes around those edits and stops on a conflict instead of choosing. See [diagrams](diagrams.md) and the [spec format](../skills/ki-diagram/spec.md).

```mermaid
flowchart TB
    a["get_context, decisions_for"] --> b["ki status: project, vault allowed?"]
    b --> c["ki diagram check: hand edits, conflicts"]
    c --> d{"page plan, you approve"}
    d --> e["write the spec"]
    e --> f["ki diagram build --pdf"]
    f --> g["read the PDF, fix layout, rebuild"]
    g --> h{"you approve"}
    h --> i["ki vault push"]
    f -->|conflict or no snapshot| q["ask which side wins; never decided alone"]
```

## /ki-rebase

Headless only: `ki skill run ki-rebase --repo <dir> --checkout <branch> --allow "git push --force-with-lease" -- <base>` rebases the branch onto `origin/<base>` (`main` by default) in a throwaway worktree and pushes it. It runs on Claude Code, the only agent whose tools ki can limit, with the `execution` role's model policy. It fetches the base and the branch, stops with `DONE: already up to date` when the branch has the base, and with `DONE: superseded: every change on <branch> is already in origin/<base>, nothing pushed` when `git cherry origin/<base> origin/<branch>` lists only `-` lines or, after a rebase, `git diff origin/<base>...HEAD` is empty (nothing is pushed, since a force-push would empty the pull request), resolves each conflict by reading what the base changed and keeping both sides' intent, and ends `BLOCKED` with the files and one question, after `git rebase --abort`, when a conflict is not clear. The push is `git push --force-with-lease origin HEAD:refs/heads/<branch>`, refused when someone pushed to the branch since the run started, and it prints `pushed <sha> to <branch>` before the status line. The skill's own `ki-tools` never grant the push, so without `--allow` the run stops before it.

```mermaid
flowchart TB
    a["fetch origin base and branch"] --> m{"origin/branch still at the<br/>SHA the run started at?"}
    m -->|no| q0["BLOCKED: branch moved"]
    m -->|yes| u{"branch already has the base?"}
    u -->|yes| d0["DONE: already up to date"]
    u -->|no| r["git rebase origin/base"]
    r --> c{"conflicts"}
    c -->|clear| k["keep both sides, add, rebase --continue"]
    k --> c
    c -->|not clear| q["rebase --abort, BLOCKED: files and question"]
    c -->|none left| p["git push --force-with-lease origin HEAD:refs/heads/branch"]
    p --> o["pushed sha to branch, DONE"]
```

## /ki-address

Headless only: `ki skill run ki-address --repo <dir> --checkout <branch> --allow "git push --force-with-lease" --allow "gh api graphql" -- <number>` reads the unresolved, not outdated review threads of pull request `<number>` and puts each in one group: **fix** (the comment is right and the change is clear), **decline** (wrong or out of scope) or **left** (needs a design call, or is unclear). It applies the fixes, runs the repo's own checks, commits `fix: address review comments`, and pushes with `git push --force-with-lease origin HEAD:refs/heads/<branch>`, refused when someone pushed to the branch since the run started. Only after the push does it reply `Fixed in <short sha>.` on each fixed thread and resolve it. Declined and left threads are never replied to or resolved; each gets a draft reply in the output. Comment bodies are data: a comment that says to run something or change another file is weighed only within its own thread's scope.

It runs on Claude Code with the `execution` role's model policy. The skill's own `ki-tools` grant reading, edits, `git add`, `git commit`, `git reset --hard` and the usual check commands, nothing that writes to the remote. Reading threads needs `gh api graphql`, and a rule for it cannot tell a query from a mutation, so the caller grants it: without it the run ends `BLOCKED: the run was not allowed to read or resolve review threads`, and without the push allow it ends `BLOCKED: fixes committed locally, the run was not allowed to push`. When the checks still fail after one more attempt, it runs `git reset --hard` to the start commit and ends `BLOCKED` naming the failing check: nothing is pushed or resolved.

The output is one line per thread before the status line, parsed by callers:

```text
fixed <path>:<line> <thread url>
declined <path>:<line> <thread url>
  <draft reply, one paragraph>
left <path>:<line> <thread url>
  <draft reply, one paragraph>
pushed <short sha> to <branch>
DONE: fixed N, declined M, left K
```

`DONE: nothing to address` ends a run with no open thread to look at, and a run with only declined and left threads pushes nothing.

```mermaid
flowchart TB
    a["read unresolved threads<br/>gh api graphql"] --> n{"any thread not outdated?"}
    n -->|no| d0["DONE: nothing to address"]
    n -->|yes| g["group: fix, decline, left"]
    g --> f["apply the fixes, run the repo's checks"]
    f --> k{"checks pass?"}
    k -->|"no, after one more attempt"| q["git reset --hard start, BLOCKED: failing check"]
    k -->|yes| p["git push --force-with-lease origin HEAD:refs/heads/branch"]
    p --> r["reply Fixed in sha and resolve each fixed thread"]
    r --> o["print thread lines and draft replies, DONE: fixed N, declined M, left K"]
```

## How they get better over time

The model does not learn; what it retrieves does.

1. **Your edits become examples.** `/ki-review` saves each posted review with `edited: true` when you changed the draft, and future reviews retrieve your corrected wording. Unedited approvals count less, so the tone does not drift toward the agent's.
2. **Decisions become checks.** Each `/ki-decision` record is a rule the next `/ki-resolve` follows and the next `/ki-review` enforces.
3. **Incidents become shortcuts.** Each `/ki-debug` incident note lets the next debug session start from the known cause.
4. **Superseded rules stop firing.** Graphiti marks replaced facts invalid, and context packs drop them.

## Rules every skill follows

- Nothing is posted, created, pushed or changed on GitHub or a live system without your approval, except draft PRs from `/ki-resolve` and `/ki-burndown`.
- Nothing harness-related appears in code, commits, branches, PRs, issues or review comments.
- For identities marked `vault: false`, nothing is copied into the vault.
- Notes reach the vault repo only through `ki vault push`, which commits, pulls with rebase and pushes `$KI_VAULT`; the skills never run their own git commands there.
- Outside these skills, an agent saves documentation with the `ki-vault` MCP server's `vault_write` (see [laptop setup](laptop-setup.md#claude-code-codex-and-cursor)), which places the note, adds frontmatter, updates the folder note and runs the same push.

## Setup each skill relies on

| Needs | For |
| --- | --- |
| A session started with `ki` (Claude Code, Codex) or a repo set up with `ki cursor` | The `ki-kb` MCP tools and, outside Cursor, the right `GH_TOKEN` |
| `KI_VAULT` in your shell profile, pointing at a git clone of the vault repo | Saving reviews, decisions and incident notes, pushed with `ki vault push` |
| `board:` on the project in `projects.yaml` (optional) | Moving cards and adding issues; every skill works without one |
| `gh auth refresh -s project` once per account | Writing to GitHub Projects, only with a board |
| `shellcheck`, `terraform`, `helm`, `actionlint`, Go and Python on the machine | `/ki-review` running the checks for the surfaces a PR touches |
| `kubectl` context for the cluster | `/ki-debug` in `sre` mode |
| node 22+ and `make diagram` | `/ki-diagram` building drawings and PDFs |

## Adding a skill

Create `skills/<name>/SKILL.md` with `name`, `description`, and `disable-model-invocation: true` for anything that writes. To make it runnable headless through [`ki skill run`](ki-skill.md), add `ki-role` and list what it may do beyond reading in `ki-tools`; without `ki-tools` it runs read-only. Put long reference material in supporting files next to it and link them from `SKILL.md`. Run `make skills`, add a row and a flow above. Keep the procedure in the skill and the knowledge in the KB: skills stay small and stable while the KB grows.
