# Loading coding standards

Your coding standards live in the kio checkout, one file per language. They are loaded in full for every language a change touches, not looked up by search, so no rule is ever missed.

1. Get the harness path: `ki status --json`, field `harness`.
2. Work out which languages the change touches. If it touches any code, also load `<harness>/standards/common.md`, which holds the rules for every language:

   | Files | Standard |
   | --- | --- |
   | `*.go`, `go.mod` | `<harness>/standards/go.md` |
   | `*.py`, `pyproject.toml` | `<harness>/standards/python.md` |
   | `*.sh`, `*.bash`, or any file whose first line is a bash or sh shebang | `<harness>/standards/bash.md` |

3. Read each matching file that exists, before planning or reviewing. Templates they name (`standards/templates/...`) are under `<harness>` too.
4. Precedence when rules conflict: the standards files, then the repo's own config (linters, formatters, existing patterns), then the authority each file names (for example the Google Go Style Guide), then general best practice. If a standard conflicts with an active decision for this repo, follow the decision and say so.
   - When reviewing, a broken **MUST** is a required change; a broken **SHOULD** is a suggestion unless the code gives no reason for deviating.
   - When writing code in a repo that already breaks a rule throughout, follow the rule in the lines you touch and do not rewrite the rest unasked; mention it once.
5. Each standards file ends with a "Checks" section naming the linters and commands that enforce its mechanical rules. Run them if they are installed; say which ones were not.
