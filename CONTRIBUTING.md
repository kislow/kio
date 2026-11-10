# Contributing

Thanks for helping. You do not need any agent tooling: a Go toolchain, a Python environment and a text editor are enough. Please follow the [code of conduct](CODE_OF_CONDUCT.md). Report vulnerabilities privately as described in [SECURITY.md](SECURITY.md), never in a public issue.

## Set up

Install:

- Go at the version in `go.mod` (`go 1.27.3` today).
- [uv](https://docs.astral.sh/uv/), which fetches the Python the knowledge-base service needs (`requires-python` in `kb/pyproject.toml`, 3.13 or newer) and its dependencies.
- Node 22 or newer (`engines` in `diagram/package.json`) for the diagram tool.
- [golangci-lint](https://golangci-lint.run/) at the version the Makefile pins (`GOLANGCI_VERSION`), so local lint finds what CI finds:

  ```bash
  go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.15.0
  ```

- [kustomize](https://kubectl.docs.kubernetes.io/installation/kustomize/) (CI uses 5.8.3), which `make test` uses to render and check the cluster manifests.

Then clone and check that everything passes before you change anything:

```bash
git clone https://github.com/kislow/kio.git
cd kio
make test
make lint
```

- `make test` runs the Go tests with the race detector, the Python tests, the diagram tests and the manifest checks. The PDF test in `diagram/` skips itself when headless Chromium is missing; `make diagram` installs it.
- `make lint` runs `golangci-lint`, `ruff` and `mypy`.
- `make validate-projects` checks `deploy/config/projects.yaml` with both validators. Run it after editing `projects.yaml` or `testdata/projects/`.
- `make help` lists the other targets.

To try your change as a command, build it and run it from the checkout:

```bash
go build -o /tmp/ki ./cmd/ki
/tmp/ki --version
```

The binary is called `ki`; the repository and the Go module are called `kio`.

## Changes

1. **Issue first.** Search the [open issues](https://github.com/kislow/kio/issues), then open a bug report or a feature request. For a small fix you can go straight to a PR, but anything that changes behaviour deserves a short conversation first. Maintainers write changes up with the change template: Why, What, Done when, Related. The Done when list is the acceptance criteria, so a PR can say how each item is met.
2. **Branch and work.** Keep the change to what the issue asks. Add or update tests, and keep `docs/` in step with behaviour, including the Mermaid diagrams.
3. **Check.** Run `make test` and `make lint`. Go packages listed in CI must stay at 80 percent coverage, and so must the Python code.
4. **Open a PR.** Fill in the [PR template](.github/pull_request_template.md): a summary, `Closes #N`, how each Done when item is met, and how you tested.

### PR titles

PRs are squash-merged, so the PR title becomes the one commit on `main`. CI rejects a title that does not match this pattern:

```text
^(feat|fix|docs|refactor|perf|test|build|ci|chore|revert|style)(\([a-z0-9-]+\))?!?: .+
```

For example `fix(delegate): keep the worktree when a job times out` or `feat!: drop the legacy config path`. Write commit messages the same way: imperative, lowercase first word, no trailing period.

### Releases

[release-please](https://github.com/googleapis/release-please) reads the titles on `main` to choose the next version and write the changelog. Never edit `CHANGELOG.md`, `.release-please-manifest.json` or the version lines by hand; the release PR it opens does that. Maintainers cut releases as described in [operations](docs/operations.md#releases).

### Decisions

A non-trivial choice (a library, a pattern, a trade-off, a change to a fail-closed check) is recorded in `decisions/`: copy `decisions/TEMPLATE.md` to `decisions/YYYY-MM-DD-short-title.md` and fill in Context, Decision, Alternatives rejected and Consequences. Link it from the PR. Read `decisions/` before changing how a component works so you do not reverse one by accident.

## Conventions

- Follow `standards/common.md` and the file for the language you touch (`standards/go.md`, `standards/python.md`, `standards/bash.md`).
- Examples and fixtures use neutral names: `acme`, `alice`, `example.com`, identities `me`, `work` and `client`. No real hosts, accounts, tokens or personal data.
- The architecture is in [docs/architecture.md](docs/architecture.md).

## Licence

This project is under the [Apache License 2.0](LICENSE). There is no CLA and no sign-off: by submitting a contribution you license it under Apache-2.0, as section 5 of the licence provides.

## Agents

Agents are optional. Maintainers sometimes run issues through `ki delegate` ([docs/ki-delegate.md](docs/ki-delegate.md)); you do not need it, and a PR written by hand is as welcome. If you use an agent yourself, you are responsible for the diff: read it, run the checks, and keep the PR to one issue.
