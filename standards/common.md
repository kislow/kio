# Standards for every language

Loaded with the language file whenever a change touches code. Every rule is tagged **MUST** (enforced by tooling or review, no exceptions without a written reason) or **SHOULD** (the default; deviate knowingly). Templates live in `standards/templates/` in the harness checkout.

## Where these rules come from

We follow established standards instead of rewriting them, and add our own rules on top. When a question is not answered here, the authority for the language decides.

| Language | Authority | Our additions |
| --- | --- | --- |
| Go | [Google Go Style Guide](https://google.github.io/styleguide/go), [Go Code Review Comments](https://go.dev/wiki/CodeReviewComments), [Uber Go Style Guide](https://github.com/uber-go/guide), [Kubernetes coding conventions](https://github.com/kubernetes/community/blob/master/contributors/guide/coding-conventions.md) | `go.md`, `slog`, `templates/go/.golangci.yml` |
| Bash | [Google Shell Style Guide](https://google.github.io/styleguide/shellguide.html) (also Kubernetes' choice) | `bash.md`, strict-mode template, `shellcheck` and `shfmt` in CI |
| Python | PEP 8 and PEP 484, `ruff`, `mypy` | `python.md`, `pyproject.toml` plus `uv`, structured logging, pydantic at boundaries |

## Rules

1. **MUST** format and lint automatically (pre-commit and CI). Style is not discussed in review.
2. **MUST NOT** hardcode secrets, tokens, tenant IDs or environment URLs. Read config from the environment; keep secrets in a secrets manager or the platform's secret store.
3. **MUST** return or raise errors with context. Never swallow an error (`except: pass`, `_ = f()`, `|| true` without a comment saying why).
4. **MUST** log in structured form (key/value or JSON), with levels, never `print` / `fmt.Println` / bare `echo` for diagnostics. CLI output meant for a user's stdout is allowed; diagnostics go to stderr or the logger.
5. **MUST** put a timeout on every outbound call (HTTP, DB, subprocess).
6. **MUST** make anything destructive (delete, overwrite, apply) support a dry-run, and print what it would do.
7. **MUST NOT** commit build artefacts, binaries, `.DS_Store`, `.idea/`, `__pycache__/`, or exported data (user lists, dumps). Add them to `.gitignore`.
8. **SHOULD** have tests alongside the code for critical paths (target 80%+ coverage there). A test that cannot fail is worse than none.
9. **SHOULD** keep feature flags in environment variables, with no cache that lacks an expiry or invalidation rule.

## Applying these in a repo

- A repo's own config (its linter settings, formatter, existing patterns) is followed where these files are silent. Where they conflict, these files win, unless an active decision for the repo says otherwise; then follow the decision and say so.
- When a repo has no lint or format setup, propose adding the matching template from `standards/templates/` as its own change, not mixed into a feature.
- Templates are generic: copying one into a project repo adds nothing harness-related.

## Checks

Before every commit: the language file's Checks, plus `gitleaks` for secrets. `standards/templates/.pre-commit-config.yaml` runs all of them for all three languages:

```bash
cp <harness>/standards/templates/.pre-commit-config.yaml .
pre-commit install && pre-commit autoupdate
```
