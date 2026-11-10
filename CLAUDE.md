# kio: agent instructions

Before planning a change, call the `get_context` tool from the `ki-kb` MCP server with a one-line description of the task. Use `decisions_for` before changing how a component works. Read `docs/architecture.md` for how the parts fit together.

## Rules that keep brains apart

Every brain is its own graph in one FalkorDB in namespace `harness`; device tokens decide which brains a machine can read, and brains listed in `sensitive_brains` (none today) are never in a default scope.

- Never weaken a fail-closed check: `scope.Resolve`, `config.Lookup`, `Settings.check_graph`, `projects.parse` validation, the scanner, `_safe_error`, `deploy/check.py`, or the default-deny network policy.
- Document text must never enter Temporal workflow inputs or results. Workflows pass project names and counts; activities read content and the projects file themselves.
- Model providers are never defaulted in code; `deploy/config/models.env` names them, and embeddings stay in the cluster.
- Graph names must match `[a-z0-9-]` (Graphiti rejects colons): `cortex`, `brain-<name>`.
- Secrets live in a secrets manager and reach the cluster through its operator from your GitOps repo; never commit one or document a `kubectl create secret` flow.

## Rules that keep project repos clean

- Nothing harness-related is ever written into a project repo. Config lives in `projects.yaml` of the configuration directory (`deploy/config/` is the fallback and the example); decision records for projects live in the vault.
- Skills and agent instructions must keep telling agents not to mention the harness in code, commits, PRs, issues or reviews.

## Conventions

- Follow `standards/common.md` and the language file (`standards/go.md`, `python.md`, `bash.md`); this repo uses the templates' lint configs as they are.
- Go: latest stable toolchain, standard library first, `cmd/` only wires `internal/`, errors wrapped with `%w`, table tests, `golangci-lint` (pinned version in the Makefile and CI) clean, 80% coverage on the packages CI lists.
- Python only where Graphiti requires it (`kb/`): 3.13+, pydantic at boundaries, JSON logging, `ruff`, `mypy --strict`, 80% coverage. Everything else, including the CLI, is Go.
- No agent attribution anywhere: no `Co-Authored-By`, `Generated with` or session-link lines in commits or PR descriptions.
- `projects.yaml` is validated in Go (`pkg/projects`) and Python (`kb/src/ki_kb/projects.py`); change both together and add a fixture to `testdata/projects/`.
- Code-writing skills load `standards/common.md` and `standards/<lang>.md` in full; keep `skills/ki-resolve/standards.md` in step if the layout changes. Templates in `standards/templates/` stay generic: nothing harness-related, since agents copy them into project repos.
- Keep `docs/` in step with behaviour changes, including the Mermaid diagrams.
- PR titles are Conventional Commits (`feat:`, `fix:`, `docs:`, `chore:`, `ci:`, `feat!:`) and PRs are squash-merged; release-please builds versions and the changelog from those titles, and CI rejects other formats. Write commit messages the same way. Never edit `CHANGELOG.md`, `.release-please-manifest.json` or the version lines by hand.
- Every change has an issue with a Done-when list (see `CONTRIBUTING.md`); the PR says `Closes #N` and how each item is met. Larger work gets a parent issue with a checklist of child issues, each sized for one agent.
- Run `make test` and `make lint` before opening a PR.
- Record non-trivial choices in `decisions/` using `decisions/TEMPLATE.md`.
