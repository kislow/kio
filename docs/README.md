# Harness documentation

Everything needed to set this up from nothing: the knowledge base (on one machine with Docker Compose, or on a Kubernetes cluster), the laptops that use it, and the agent skills on top.

Read in this order the first time:

| Page | What it covers |
| --- | --- |
| [Install with an agent](../INSTALL.md) | For an agent to follow: interviews the person about their agents, vendors, knowledge base, vault and GitHub identities, then installs and checks ki |
| [Architecture](architecture.md) | What the parts are, how a document becomes context, how an agent query is scoped |
| [Projects and brains](projects-and-brains.md) | `projects.yaml`, identities, brains, and how to add a project or a brain |
| [Self-hosting](self-hosting.md) | Running the knowledge base yourself: the Compose stack as the main path, and generic notes for deploying `deploy/` to any Kubernetes cluster (ingress, secrets, network access, images) |
| [Local stack](local-stack.md) | The knowledge base on one machine with Docker Compose, no cluster: setup, the loopback-only security model, the smoke test |
| [Laptop setup](laptop-setup.md) | Setting up a machine: git identities, `ki setup machine`, your own configuration (`ki setup config`), the model policy, tokens, `make laptop`, `ki doctor`, every `ki` command, permission modes |
| [ki-mecca](ki-mecca.md) | The tmux hub for agent sessions: `ki mecca new`, `ls`, `attach`, `kill`, and how a project name finds its checkout |
| [ki delegate](ki-delegate.md) | One task run headless on any agent in its own worktree, answered with one status line, optionally claiming a GitHub issue so two machines do not start it: how a planner hands work to Claude Code, Codex, Cursor or agy |
| [ki ask](ki-ask.md) | One read-only question answered by the agent the machine's search role picks, from the knowledge base, the vault and GitHub, with a source for every fact; what a UI calls instead of running an agent itself |
| [ki skill run](ki-skill.md) | One skill run headless on the agent its role picks, allowed only the tools the skill's frontmatter declares, in a scratch folder or a throwaway worktree of a pull request or branch; what a UI calls instead of running `claude -p` with a fixed model |
| [ki mcp ls](ki-mcp-ls.md) | Every MCP server each agent on the machine can reach (Claude Code, Cursor, Codex, and ki's own), with source, transport, a redacted target and status, as a table or stable JSON for a UI; never a token |
| [ki kb](ki-kb.md) | Read-only views of the knowledge base as JSON for UIs: the brains a machine can reach with document counts and last ingest, ranked search with sources and links, the newest documents and the context pack, through the same server, token and brain rules as a session |
| [ki approve](ki-approve.md) | Approve a pull request as the repo's identity, right away or after a read-only agent reviewed it clean: own pull requests, the security-surface rule, merging, and what it never writes |
| [Models](models.md) | Which model does what, what it costs, how to switch providers |
| [Skills](skills.md) | `/ki-issue`, `/ki-resolve`, `/ki-burndown`, `/ki-review`, `/ki-debug`, `/ki-decision`, `/ki-recap`, `/ki-diagram`, `/ki-rebase`, `/ki-address`, coding standards, and how they learn |
| [Diagrams](diagrams.md) | Optional. `ki diagram`: tldraw drawings built from specs, hand edits kept, PDFs |
| [Vault and diagrams](vault.md) | Optional. A notes vault in git, Obsidian Sync, the `vault-pusher` image and the diagram builder |
| [Third-party licences](third-party-licences.md) | What the dependencies are licensed under, the tldraw and FalkorDB conclusions, and what `make licences` enforces |
| [Security and brain separation](security.md) | Every control that keeps sensitive material where it belongs |
| [Operations](operations.md) | Day-two tasks on a cluster: watch ingest and model cost, rotate a token, revoke a laptop, rebuild a brain, alerts, the vault pusher, releases |
| [Go packages](go-packages.md) | The four packages other repos may import, and how to pin them |
| [Roadmap](roadmap.md) | What is built, what is next |

## Repository layout

| Path | What |
| --- | --- |
| `cmd/ki`, `cmd/ki-mcp` | Go: the `ki` CLI and the MCP server, one module: shared packages in `internal/`, and `pkg/projects`, `pkg/scope`, `pkg/config` and `pkg/sessions` public for other repos ([Go packages](go-packages.md)) |
| `kb/` | Python, only because Graphiti is Python: Temporal ingest workers and the kb-query search service |
| `skills/` | Agent skills for Claude Code, Codex, Cursor and agy, installed with `make skills` |
| `diagram/` | Node, because tldraw is: `ki diagram` builds the vault's tldraw drawings from specs and renders them to PDF |
| `vault/` | The `vault-pusher` image: Obsidian's headless Sync client plus `ki vault push`, so the cluster commits the vault to git itself |
| `standards/` | Go, Python and Bash standards and templates, loaded in full by every skill that writes or reviews code |
| `agent/` | Generic instructions injected into every agent session; the configuration directory can add its own after them |
| `deploy/` | Kustomize for the `harness` namespace (knowledge base, ki-mcp, vault-pusher), Temporal Helm values, `projects.yaml`, model config, deployment check, and the Compose stack in `deploy/compose/` |
| `decisions/` | Decision records for the harness itself |
| `docs/` | Architecture and setup guides |
| [`LICENSE`](../LICENSE), [`NOTICE`](../NOTICE) | Apache-2.0 and the notices the binaries must carry; copied into the release archives and the images |

Which projects exist, their brains and their identities live only in `projects.yaml` in the configuration directory: a directory of your own linked with `ki setup config`, else this repo's [`deploy/config/`](../deploy/config/projects.yaml); [`examples/config/`](../examples/config/) is the template ([decision](../decisions/2026-10-24-config-split.md)).

Decisions behind the design live in [`../decisions/`](../decisions/). This folder and the decisions are ingested into the `cortex` brain, so agents can answer questions about the harness itself.
