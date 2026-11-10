# Vault and diagrams (optional)

ki runs without any of this. A knowledge base built from git repos of docs, `ki delegate`, `ki ask` and every skill work with no vault, no Obsidian and no Sync subscription. Read this page only if you keep notes in an [Obsidian](https://obsidian.md) vault and want agents to write to it, the cluster to ingest it, or drawings built from specs.

With no `KI_VAULT` and no workspace in `projects.yaml`, `ki doctor`, `ki setup` and `ki status` say nothing about the vault, `ki recap` reports no vault notes and no warning, and the note-writing skills hand you the text instead of saving it.

## What is optional

| Piece | What it is | Needs |
| --- | --- | --- |
| The vault | A folder of Markdown notes, a git clone of your private vault repo that Obsidian opens (`~/notes` in the examples) | `KI_VAULT` exported on the machine that holds the clone |
| `ki vault push` | Commits whatever changed in the vault, pulls with rebase and pushes | `KI_VAULT` |
| `ki-vault` MCP server | `vault_write`, `vault_read`, `vault_list` and `vault_search` for any agent, started by `ki` over stdio (`ki vault mcp`) | `KI_VAULT` |
| Vault as a brain source | The vault repo listed in `projects.yaml` like any other project, with ingest rules per folder | The repo in `projects.yaml` |
| Obsidian Sync | Carries notes between your devices | An Obsidian Sync subscription |
| `vault-pusher` | A cluster deployment that receives the vault through Obsidian Sync and pushes it to the vault repo | Sync, a deploy key, a `kio-vault` image you build ([below](#build-the-image-yourself)) |
| `ki diagram` | tldraw drawings built from spec files, hand edits kept, PDF or SVG output | node 22 or later and `make diagram` once |

## Use a vault on a laptop

Clone your vault repo and export it in the shell profile of the machine that holds the clone:

```bash
export KI_VAULT="$HOME/notes"
```

From then on:

- `/ki-decision`, `/ki-review`, `/ki-debug` and `/ki-diagram` write under `$KI_VAULT/projects/<project>/`, only for identities whose `vault` is not `false` in `projects.yaml` (see [skills](skills.md)), then run `ki vault push`.
- Every session also gets the `ki-vault` server. Claude Code gets it through the session's MCP config, Codex through the `ki setup codex` block (rerun it after moving the vault or reinstalling `ki`), Cursor through `ki cursor`. It refuses what `ki vault push` refuses, plus credential-shaped values and a second note with an existing name.
- `ki ask` can use `--source vault`, read-only.
- `ki recap` adds the notes changed in the window from `projects/<folder>/` folders that belong to the identity.
- `ki doctor` gains a Vault section (whether `KI_VAULT` is a usable clone with an origin) and a Diagrams section.

`ki vault push` refuses to run from a repo whose identity is `vault: false`, when the vault would be committed with an email other than its identity's, and while a rebase from an earlier conflict is still open.

Add the vault repo to `projects.yaml` under the identity that owns it to have the cluster ingest it. Workspaces (named views over projects, brains and vault folders) are described in [projects and brains](projects-and-brains.md).

## A second machine

Install Obsidian, sign in to Obsidian Sync and connect the remote vault. That is all a second machine needs to read and write notes. Leave `KI_VAULT` unset there: the `vault-pusher` in the cluster pushes what Sync brings in, and the skills on that machine say so and hand you the text. Set `KI_VAULT` on a second machine only if it should push too. It then needs its own git clone of the vault repo, not the Sync folder.

Obsidian Sync carries the notes between devices; git carries them to the cluster. Two things push to the vault repo: `ki vault push` on your main machine and the `vault-pusher` in the cluster, which pushes once nothing has changed for five minutes. Both pull with rebase, so they converge. A real conflict stops the pusher until someone resolves it.

## The vault-pusher in the cluster

`vault-pusher` is left out of `deploy/kustomization.yaml`, and the example overlay in `examples/config/kustomization.yaml` carries it as commented lines. The knowledge base deploys, and `deploy/check.py` passes, with or without it.

To enable it:

1. In your configuration repo's overlay, uncomment `../../deploy/vault` under `resources` (point it at `github.com/<owner>/kio//deploy/vault?ref=vX.Y.Z` when the base is the released harness), the `vault-pusher-git` generator, the `kio-vault` entry of `images:` and the `vault-pusher` patch. Set `VAULT_REMOTE` to your vault repo, `OBSIDIAN_VAULT` to the vault's name in Obsidian Sync, and the committer email in `gitconfig` to the identity that owns the vault repo in `projects.yaml`.
2. Create the deploy key and the Obsidian login, then watch the logs: the steps are in [Operations](operations.md#vault-pusher). The `vault-deploy-key` secret is listed in [self-hosting](self-hosting.md#secrets).
3. Build and push the `kio-vault` image yourself (below); there is no public one. In a fork, setting the repository variable `PUBLISH_VAULT` to `true` makes the `publish` workflow build and push it with the other images.

The image (`vault/Dockerfile`) is the `ki` binary plus Node and `obsidian-headless`, Obsidian's headless Sync client, which needs Node 22 or later. `vault/sync.sh` keeps a folder in step with Sync; `vault/pusher.sh` runs `ki vault push` after five quiet minutes. The release PR pins the tag in `deploy/vault/kustomization.yaml` next to the base's images.

### Build the image yourself

The `kio-vault` image is not published. The image installs `obsidian-headless` from npm, and that package's metadata gives no licence, so a public image would redistribute software whose terms are unstated. Build it from the repo root and push it to a registry you control:

```bash
docker build -f vault/Dockerfile -t registry.example.com/acme/kio-vault:0.1.0 .
docker push registry.example.com/acme/kio-vault:0.1.0
```

Then point the overlay's `images:` entry at it. The `name` is the base's placeholder, `newName` your repository and `newTag` the tag you pushed:

```yaml
images:
  - name: registry.example.com/kio-vault
    newName: registry.example.com/acme/kio-vault
    newTag: 0.1.0
```

Keep the registry private, and check the package's terms with its publisher before pushing the image anywhere public.

Turning it off again is removing those lines and deleting the `vault-pusher` Deployment and its `vault-data` volume. Nothing else in the harness depends on them.

## Diagrams

`ki diagram` builds tldraw drawings for the vault from spec files, keeps hand edits made in Obsidian when a drawing is rebuilt, and renders each drawing to PDF or SVG. Agents draw through the `/ki-diagram` skill. It needs node 22 or later and `make diagram` once in the harness checkout, which installs the pinned packages and Playwright's headless Chromium (about 100 MB). Setup, the file layout, every command and the spec format are in [diagrams](diagrams.md).

Without a vault, none of this runs and `ki doctor` does not mention it. The packages under `diagram/` are only installed by `make diagram`; `make test` runs the diagram tests but nothing else needs them.
