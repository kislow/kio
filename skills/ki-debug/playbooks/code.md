# Playbook: code

For failing tests, wrong behaviour and stack traces in this repository.

1. **Reproduce.** Find the smallest command that shows the failure (one test, one input). If you cannot reproduce it, collect what differs between where it fails and where it does not (versions, config, data, environment) before going further.
2. **Pin it with a test.** Write or isolate a test that fails for the reported reason. It becomes the regression test.
3. **Narrow down.**
   - Read the stack trace from the top frame in this repo's code, not the library frames.
   - If it used to work, find when it broke: `git log` on the involved files, then `git bisect` with the failing test as the check if the window is large.
   - Add temporary logging or a debugger at the boundary where expected and actual first diverge; remove it before committing.
4. **Fix the cause, not the symptom.** If the fix is a workaround, say so explicitly and why the real fix is out of scope.
5. **Verify.** The new test passes, the full suite passes, and the original reproduction now behaves correctly.

Language hints:

- Go: run with `-race` for anything concurrent; check for ignored errors and nil interfaces holding typed nils; `go test -run <Name> -count=1 -v`.
- Python: check for mutable default arguments, naive versus aware datetimes, and async code called without await; `pytest -x -k <name> -vv`.
- Bash: run with `bash -x`; check quoting, `set -euo pipefail` behaviour in pipelines and subshells, and exit codes swallowed by `|| true`.
