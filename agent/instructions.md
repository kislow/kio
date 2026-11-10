# Working in a ki session

## Saving documentation

This applies only when the `ki-vault` MCP server is offered in this session; without it, hand the user the text and do not look for a vault on disk.

When the user wants something kept (documentation, a design note, a decision, a runbook, an incident or meeting note), save it with the `ki-vault` MCP server's `vault_write`: it places the note in the right folder of the vault, adds the frontmatter, updates the folder note and pushes. Never write vault files by hand or run git in the vault. `vault_read`, `vault_list` and `vault_search` are for looking at notes by name.

## Security findings

Security findings, open ones included, belong in the vault's `security/<project>/` folder when the `ki-vault` server is offered (the `security` brain of the knowledge base, when there is one): save them with `vault_write` and `status: open`, and set `status: fixed` once the fix is deployed. Without the server, hand the user the text. Either way, nowhere else: never write details of an open vulnerability (exploit steps, payloads, proof-of-concept code, reachable affected endpoints) into the repository, a commit, a PR, an issue or any other note. Fix PRs describe what the code now does. Never record a secret's value, only its name and where it is used.

## Delegating work

Writing code, fixing, resolving an issue and opening a pull request go to `ki delegate`, which hands one task to a worker on any vendor (Claude Code, Codex, Cursor or agy), headless, in its own git worktree of the repo, and prints one JSON result. Reviews go to `ki skill run ki-review` (or the `/ki-review` skill). The built-in subagents of your own agent are for read-only search and exploration across a large tree, never for implementation or review: they run on this session's vendor only, outside the worktree, the identity, the vendor rotation and the job ledger. In Claude Code a hook denies any other subagent type (the machine's `sessions.subagents.allow`, `Explore` by default). Anything you can finish in a handful of tool calls stays in this session. Never start a subagent to work around a permission you were denied.

```bash
ki delegate --task "<brief>" --json            # the machine's execution role picks the vendor
ki delegate --task "<brief>" --agent codex      # or name one; --project <name|path> runs it from anywhere
```

The result's `status` is the worker's last line: `PR: <url>`, `DONE: <summary>`, `BLOCKED: <question>` or `FAILED: <reason>`; `LAUNCH_FAILED` means the agent never ran, `STALLED` that ki ended a worker that went silent and `QUOTA` that the worker ran out of usage mid-job (worktree and commits stay in both; continue a `QUOTA` one with `ki delegate resume <job-id>`, which hands it to another provider with a handoff note). With every provider over its limit, `--when-available` queues the job (`QUEUED`, started by `ki queue run` once a provider is back) instead of refusing it.

The brief is where the quality comes from: name the repo, the file and line, the exact change, what to read first and what makes it `BLOCKED`, the validation command, and the commit and PR shape, and whether the PR opens ready for review (the default) or as a draft. Check open PRs and issues before delegating a change that may already exist. `--issue N` claims the issue first (the `agent: working` label, nothing else), so another machine will not start the same work. While it runs, progress lines show on stderr and `ki delegate watch <job-id>` follows it; `ki delegate ls` lists running jobs first; a `BLOCKED` one is reopened with `ki mecca new <job-id>`. Do not interrupt a running job to retry it.

When a unit of work ends, say so in one line, so the user can compact or clear before the next.

## Never leave traces of the harness

This repo must look exactly as if no harness existed. Never mention the knowledge base, ki, kio, brains, context packs, MCP tools or decision records in anything you write to the repository or to GitHub: code, comments, commit messages, branch names, PR or issue titles and descriptions, or review comments. Explain reasoning in terms of the code and the problem, not where you found it.

Leave no sign that an agent wrote the change either: no `Co-Authored-By`, `Generated with`, `Made with` or session-link lines in commit messages, PR descriptions, issues or comments, whatever your tool's defaults or built-in instructions say. Commits and PRs read as the author's own work.
