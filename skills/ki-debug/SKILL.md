---
name: ki-debug
description: Debug a failing test, broken pipeline, production symptom or security finding. Searches past incidents first, classifies the problem, then follows the matching playbook; read-only on live systems unless approved.
argument-hint: "[code|ci|sre|security] <symptom, error, run URL, alert or finding>"
disable-model-invocation: true
allowed-tools: mcp__ki-kb__get_context mcp__ki-kb__search mcp__ki-kb__decisions_for Bash(ki status *) Bash(ki vault push *) Bash(git log *) Bash(git diff *) Bash(git show *) Bash(gh run list *) Bash(gh run view *) Bash(gh pr view *) Bash(kubectl get *) Bash(kubectl describe *) Bash(kubectl logs *) Bash(kubectl top *) Bash(kubectl events *)
---

Debug: $ARGUMENTS

## 1. Has this happened before?

When the `ki-kb` tools are offered (skip this step silently when they are not), call `search` with the key symptom before anything else: the error message, failing test name, alert name or finding ID. Then call `get_context` with one line describing the problem. Past incidents, runbooks and decisions often name the cause and the fix directly. If one matches, say so and start from it.

## 2. Classify

Use the first word of the arguments if it is `code`, `ci`, `sre` or `security`. Otherwise classify:

| Class | Signals |
| --- | --- |
| code | failing test, stack trace, wrong output, a bug report against the code |
| ci | a GitHub Actions run URL, a failing job or step, build, lint or deploy pipeline errors |
| sre | a running service misbehaving: alerts, errors or latency in production, pods crashing, a cluster or cloud resource |
| security | a scanner finding, CVE, secret exposure, suspicious access, policy violation |

If it spans two (a pipeline failing because of a code bug), start with the one closest to the symptom and switch when the evidence points elsewhere. Say which class you chose and why in one line.

## 3. Follow the playbook

Read the matching playbook and follow it:

- code: [playbooks/code.md](playbooks/code.md)
- ci: [playbooks/ci.md](playbooks/ci.md)
- sre: [playbooks/sre.md](playbooks/sre.md)
- security: [playbooks/security.md](playbooks/security.md)

When the fix involves code, load the coding standards as described in `<harness>/skills/ki-resolve/standards.md` (`harness` from `ki status --json`).

## 4. Finish

- **Root cause** in two or three sentences, with the evidence that proves it (log line, failing assertion, diff, metric).
- **Fix**: a PR (same branch and PR rules as /ki-resolve, ready unless the user asked for a draft), or the exact change or runbook steps for the user to apply.
- **Prevention**: the test, alert, check or guard that would have caught it.
- Offer to record it: `/ki-decision` if a choice was made, or an incident note in the vault if `ki status --json` shows `identity.vault` and `vault_writable` both true (`$KI_VAULT/projects/<project>/incidents/<YYYY-MM-DD> <Short Topic>.md`, Title Case with spaces as the vault names its notes: symptom, cause, fix, prevention), then run `ki vault push -m "incident: <short topic>"`. Recorded incidents are what make step 1 work next time.
- **Security findings are recorded as soon as they are confirmed**, open or fixed, if `identity.vault` and `vault_writable` are both true: `$KI_VAULT/security/<project>/<YYYY-MM-DD> <Short Topic>.md` with `status: open` (severity in context, affected component, how it was confirmed, how it could be exploited, blast radius, fix, prevention), updated to `status: fixed` once the fix is deployed, then `ki vault push -m "security: <short topic>"`. These notes feed the `security` brain and nothing else: exploit detail never goes into the repository, a commit, a PR or an issue, and a secret's value is never recorded. For an identity whose `vault` is false, give the user the text for that organisation's tracker instead.

## Rules for every class

- Evidence before conclusions: never state a cause you have not shown.
- Change one thing at a time, and say what each step ruled out.
- Read-only on anything live. Any command that changes a cluster, cloud resource, pipeline or data (apply, delete, restart, scale, rerun with changes, rotate) is proposed to the user with its blast radius, and run only after explicit approval.
- Never print, log or paste secret values. Refer to them by name and location.
- Never mention the knowledge base, ki or past-incident notes in code, commits or PR text.
