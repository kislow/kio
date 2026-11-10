# Coding standards

Your standards for Go, Python and Bash. Agents follow them over general best practice.

| File | Loaded when a change touches |
| --- | --- |
| `common.md` | any code (always loaded with a language file) |
| `go.md` | `*.go`, `go.mod` |
| `python.md` | `*.py`, `pyproject.toml` |
| `bash.md` | `*.sh`, `*.bash`, files with a bash or sh shebang |

`/ki-resolve`, `/ki-review`, `/ki-debug` and `/ki-burndown` load these files **in full** (see `skills/ki-resolve/standards.md`), never through search, so no rule is missed. They are also ingested into cortex, so `get_context` can explain a rule's reasoning.

Rules are tagged **MUST** (enforced by tooling or review, no exceptions without a written reason) or **SHOULD** (the default; deviate knowingly). Each language file ends with a **Checks** section naming the tools that enforce its mechanical rules; mechanical rules belong in linter config, so reviews spend their attention on what a linter cannot see.

## Templates

| File | Use |
| --- | --- |
| `templates/python/pyproject.toml` | uv project with ruff, mypy `--strict`, pytest and an 80% coverage gate |
| `templates/python/logging_setup.py` | JSON logging on the standard library; call once from `main()` |
| `templates/go/.golangci.yml` | golangci-lint v2 config enforcing the Go rules |
| `templates/go/main.go.tmpl` | Service skeleton: signal context, `slog`, server timeouts, errgroup, graceful shutdown |
| `templates/bash/script.sh` | Strict-mode script with `log`/`die`, `trap` cleanup, `--dry-run`, argument checks |
| `templates/.pre-commit-config.yaml` | Format, lint, and secret scanning for all three languages |

The Go skeleton, the bash template and the logger were tested on 2026-10-17: the skeleton builds, vets, passes `golangci-lint` with this config, and shuts down cleanly on SIGTERM; the bash template passes `shellcheck`.

## Baseline: where the archived code stands

Measured on 2026-10-17 over the archive (patterns counted by script, not full code review). It shows which rules matter most, not a judgement of old code.

| | Archive | Target |
| --- | --- | --- |
| Python type hints | 79% of args, 72% of returns | 100% on new code |
| Python logging | 772 `print()` vs 83 logger calls | structured logging only |
| Python tests | pytest in 33 files, 8 unittest | pytest |
| Bash `set -e` / `-u` / `pipefail` | 65% / 48% / 33% | all three |
| Bash `trap`, `local` | 5%, 14% | wherever relevant |
| Go `gofmt` clean | 82% | 100% |
| Go error wrapping (`%w`) | 0 | all returned errors |
| Go `panic` / `log.Fatal` per 1k lines | 4.8 / 4.8 | about 0 outside `main` |
| Go tests | few; one test compared two constants and never called the code | table-driven, assert real behaviour |

Kubernetes (control-plane sample, 400k lines) for reference: 100% gofmt clean, 61% of wrapped errors use `%w`, 0.13 panics per 1k lines, 62% of test files table-driven.

## Checklist before merging

| | Python | Go | Bash |
| --- | --- | --- | --- |
| Format | `ruff format` | `gofmt`/`goimports` | `shfmt` |
| Lint | `ruff check` | `golangci-lint run` | `shellcheck` |
| Types/vet | `mypy --strict` | `go vet ./...` | n/a |
| Tests | `uv run pytest --cov` | `go test -race -cover ./...` | `bats` for anything non-trivial |
| Secrets | no literals | no literals | no literals |

## Tools on a new machine

```bash
brew install uv golangci-lint shellcheck shfmt pre-commit gitleaks
```

`ki doctor` does not require them, but the skills run whichever are installed and say which were missing.
