# deploy

Kustomize for the `harness` namespace, and Helm values plus Postgres for Temporal in namespace `temporal`. Apply them with `kubectl apply -k` or let a GitOps tool (Argo CD, Flux) sync them at a release tag.

On one machine, [docs/local-stack.md](../docs/local-stack.md) is simpler. Deploying these manifests to a cluster is covered in [docs/self-hosting.md](../docs/self-hosting.md).

```bash
make check-manifests    # render and verify graphs, sensitivity, images and default-deny
```

| Path | What |
| --- | --- |
| `kustomization.yaml` | Namespace, config maps, release image tags (placeholder registry, see below) |
| `namespaces.yaml` | `harness`, with pod security labels |
| `network-policy.yaml` | Default-deny in and out, one rule per allowed path; pods labelled `app.kubernetes.io/component: ki-mcp-client` (see [ki-mcp consumers](#ki-mcp-consumers)) get ki-mcp on 8787 only |
| `config/projects.yaml` | Projects, identities, brains, ingest rules (the fallback layout: a worked example with generic names) |
| `config/models.env` | Model providers |
| `config/aliases.yaml` | Canonical names applied before extraction |
| `config/mcp-config.json` | Brains served by the MCP server and their graphs |
| `config/config.yaml` | Settings that are not projects (the knowledge-base URL); read by `ki`, not by the cluster |
| `kb/` | FalkorDB, kb-worker, kb-query, the embedding service |
| `mcp/` | ki-mcp and its IngressRoute |
| `temporal/` | Postgres and Helm values |
| `vault/` | vault-pusher: Obsidian Sync into a volume, `ki vault push` after five quiet minutes; the remote, the vault name and the git identity come from the overlay |
| `check.py` | Fails if graphs, sensitivity, images or the default-deny policy are wrong, or if any policy lets a ki-mcp consumer reach more than ki-mcp on 8787 |

## ki-mcp consumers

A workload in namespace `harness` that calls ki-mcp (a dashboard, an agent runner) must carry this label on its pod template:

```yaml
spec:
  template:
    metadata:
      labels:
        app.kubernetes.io/component: ki-mcp-client
```

The `ki-mcp-from-consumers` policy admits pods with that label to ki-mcp on 8787 and nothing else in the namespace, and `check.py` fails if any rendered policy lets them reach more. A consumer ships its own egress policy limited to ki-mcp on 8787, DNS in `kube-system`, and the internet on 22 and 443. Without the label the default-deny policy blocks the consumer's calls to ki-mcp. Labels the consumer's pod template adds are included in the check.

## Your own configuration as an overlay

The generated config maps carry a content hash in their names (`kb-config-<hash>`), so a change to `projects.yaml`, `models.env` or the brains map renames the map, kustomize rewrites every reference, and the worker, kb-query and ki-mcp roll on their own. An overlay must not set `generatorOptions.disableNameSuffixHash`, or the pods keep their old environment until someone restarts them.

`deploy/` is a complete kustomization with its own `config/`, an example with generic names that nothing should deploy as is. A configuration of your own is a directory of the same five files plus a `kustomization.yaml` that overlays this base: it references a released version of this repository, replaces the three config maps from its files and patches `KI_ALLOWED_GRAPHS` on `kb-worker` and `kb-query` to the graphs its brains map serves. [`examples/config/`](../examples/config/) is that directory for a two-brain setup: copy it wherever you keep your configuration (a private repo, a folder of your GitOps repo, a directory on your laptop), change `resources` to `<repo-url>//deploy?ref=vX.Y.Z`, and apply it with `kubectl apply -k` or point your GitOps tool at it. The same directory is what `ki setup config <dir>` links on a laptop. An overlay can live anywhere except inside `deploy/`, because kustomize refuses a base that contains its overlay.

Everything that names your installation is set by the overlay, so the base holds only `example.com` values:

| Setting | Base value | Overlay |
| --- | --- | --- |
| Image registry | `ghcr.io/kislow` for kb and mcp; `registry.example.com` as the placeholder for the vault image, which has no public copy | `images:` entries whose `name` is the base's full image name and whose `newName` is your registry; the release tag is kept |
| ki-mcp hostname | `ki.example.com` in `mcp/ingressroute.yaml` | JSON patch on the IngressRoute's `match`; the same name goes in `config.yaml` as `knowledge_base.url` |
| FalkorDB Browser hostname | `falkor.example.com` in `kb/falkordb.yaml` (IngressRoute and `NEXTAUTH_URL`) | JSON patch on the IngressRoute and an env patch on the StatefulSet |
| Temporal UI hostname | `temporal.example.com` in `temporal/values.yaml` | Helm values of the Temporal release (`web.ingress.hosts`) |
| Vault repo, Obsidian vault name, git identity | `alice/vault`, `vault`, `Alice Example` in `vault/` | Env patch on `vault-pusher` (`VAULT_REMOTE`, `OBSIDIAN_VAULT`) and a `gitconfig` merged into `vault-pusher-git` |

The hostnames are meant to be private: whatever sits in front of the ingress must admit only the networks you trust, and ki-mcp also needs a device token on every request.
