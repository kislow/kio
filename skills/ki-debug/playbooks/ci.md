# Playbook: ci

For failing GitHub Actions runs: build, test, lint, image and deploy pipelines.

1. **Get the failing step.** `gh run view <run-id> --log-failed` (or `gh run list --branch <branch> --limit 5` to find the run). Quote the first real error, not the last line.
2. **Compare with the last green run.** Find the most recent successful run on the same workflow and branch. What changed between them: the code (`git diff <green-sha>..<red-sha>`), the workflow file, an action version, a runner image, a dependency resolved fresh, or a secret or variable?
3. **Classify the failure.**
   - Deterministic and caused by the diff: hand over to the code playbook with the failing command as the reproduction.
   - Environment drift (runner image, unpinned action or dependency, upstream outage): pin the version or add the missing setup step.
   - Flaky (passes on rerun, timing or network): prove it by its history in `gh run list`, then fix the flakiness rather than adding retries. A retry is only acceptable around a known-flaky external call, and must be commented.
   - Permissions or secrets (403, missing secret, token scope): name the secret or permission needed; never print values.
   - Queued and never started: no runner took the job. Jobs run on `${{ vars.RUNNER || 'ubuntu-latest' }}`: with `RUNNER` set to a self-hosted label, check that those runners are up and listening for this repository before touching the workflow; on GitHub-hosted runners, check the Actions status page and the account's concurrency limits.
4. **Reproduce locally where possible:** run the same command the step runs, with the same versions.
5. **Fix in the pipeline or the code,** never by disabling the check. Rerunning a workflow is a change: propose it, do not trigger it unprompted.

Pin third-party actions to a full version (or SHA for anything with write permissions), and keep `permissions:` minimal in any workflow you touch.
