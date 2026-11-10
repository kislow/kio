# Third-party licences

kio is licensed under Apache-2.0 (see [`LICENSE`](../LICENSE) and [`NOTICE`](../NOTICE)). This page records what the dependencies are licensed under and what a user of the repository must do about the two that are not plain permissive licences.

`make licences` (CI job `licences`) lists the licence of every runtime dependency and fails on GPL, AGPL, LGPL, SSPL, BUSL, unknown or custom licences. A package outside those classes passes; one inside them passes only when [`.licence-allow.txt`](../.licence-allow.txt) names it with a reason. Dev-only tools (golangci-lint, ruff, mypy, pytest) are not bundled and not counted.

## Go

28 third-party packages for linux (the release archives and the images), found with `go-licenses report ./...`.

| Licence | Packages |
| --- | --- |
| BSD-3-Clause | 9 (`golang.org/x/*`, `google.golang.org/protobuf`, `mvdan.cc/sh`, `yosida95/uritemplate`, `munnerz/goautoneg`, a vendored `gddo` in client_golang) |
| Apache-2.0 | 5 (`modelcontextprotocol/go-sdk`, `prometheus/client_golang`, `client_model`, `common`, `procfs`) |
| MIT | 5 (`goccy/go-yaml`, `google/jsonschema-go`, `beorn7/perks`, `cespare/xxhash`, `segmentio/encoding`) |
| MIT-0, reported as unknown | 9 (`segmentio/asm` subpackages) |

`segmentio/asm` ships a licence file in "MIT No Attribution" form, which the detector does not recognise; the allow-list says so. Nothing is copyleft.

The Prometheus modules carry a `NOTICE` file; its text is reproduced in [`NOTICE`](../NOTICE), as Apache-2.0 section 4(d) requires for the `ki` and `ki-mcp` binaries.

## Python (`kb/`)

46 packages in the runtime environment (`uv sync --no-dev`), including `ki-kb` itself, found with `pip-licenses`.

| Licence | Packages |
| --- | --- |
| MIT | 21 (includes `temporalio`, `redis`, `FalkorDB`, `pydantic`, `httpx`) |
| BSD | 10 |
| Apache-2.0 | 8 (includes `graphiti-core` and `ki-kb`) |
| Several permissive licences combined | 5 (`neo4j`, `numpy`, `prometheus_client`, `python-dateutil`, `sniffio`: each Apache-2.0, BSD, MIT, PSF, 0BSD, Zlib or CC0 only) |
| PSF-2.0 | 1 (`typing_extensions`) |
| MPL-2.0 | 1 (`certifi`) |

`certifi` is the only weak-copyleft entry. MPL-2.0 is file-level: it applies to modified `certifi` files, and the image installs it unmodified from PyPI.

## Node (`diagram/`)

155 packages, found with `license-checker --production`.

| Licence | Packages |
| --- | --- |
| MIT (including two reported as `MIT*`) | 145 |
| ISC | 3 |
| Apache-2.0 | 2 (`playwright-core`, `ki-diagram` itself) |
| 0BSD | 1 (`tslib`) |
| tldraw licence | 4 (`tldraw`, `@tldraw/editor`, `@tldraw/assets`, `@tldraw/driver`) |

The other `@tldraw/*` packages (`store`, `state`, `state-react`, `tlschema`, `utils`, `validate`) are MIT. `playwright-core` is not redistributed with the repository; it downloads its own browser build on `make diagram`.

## tldraw

Conclusion: the repository can ship `diagram/` as it is, because it contains no tldraw code, only `package.json` and a lockfile that fetch tldraw 5.4.0 from npm. What a user needs is their own tldraw licence for anything beyond development. If that requirement is not acceptable for a first public release, move `diagram/` out of it (the decision is the owner's).

What the tldraw licence says (https://github.com/tldraw/tldraw/blob/main/LICENSE.md and https://tldraw.dev/community/license, read 2026-11-10):

- The SDK is source available, not open source. The `LICENSE.md` bundled in `tldraw`, `@tldraw/editor`, `@tldraw/assets` and `@tldraw/driver` points to that licence; the other `@tldraw/*` packages are MIT.
- The default terms permit use in development environments only. Production use needs a trial (100 days, one per business unit, a form), a hobby licence (non-commercial, shows a "made with tldraw" watermark) or a commercial licence. Each comes with a licence key passed to the SDK as `licenseKey`; keys are checked in the client and work offline.
- You may not disable or interfere with the key enforcement or the watermark, distribute the SDK as a standalone product, or ship it without a verbatim copy of the licence. The tldraw name and branding follow its trademark policy.
- An open source project may depend on it, but the SDK keeps its own licence and downstream users need their own for production.

What it means here:

- Apache-2.0 covers kio's own code in `diagram/`. It does not cover tldraw, which stays under the tldraw licence in `node_modules`.
- `ki diagram` runs the tldraw editor in headless Chromium to export PDF and SVG, and passes no `licenseKey` (there is no option for one). Neither page says whether a headless export counts as production use, and this was not tested against a key or the watermark.
- A user who runs `ki diagram` for development or evaluation needs nothing. A user who runs it in production, for example in CI or on a shared machine that publishes documents, should get a trial, hobby or commercial licence from tldraw first and confirm with tldraw that headless export is covered.
- The feature is optional: nothing else in kio needs `diagram/`, and the Obsidian vault works without it.

The README or `docs/diagrams.md` should say the same in two sentences (see the pull request for the files this change does not touch).

## FalkorDB, Graphiti, Temporal and the other images

Images are referenced, never bundled: the repository holds manifests and a compose file, and the user's runtime pulls them.

| Component | Licence | Note |
| --- | --- | --- |
| FalkorDB server (`falkordb/falkordb`) | SSPL v1 | Not bundled. Running it for your own use is unrestricted. SSPL section 13 applies to offering FalkorDB itself as a service to third parties, which would require releasing the source of the service stack. The Python client `FalkorDB` is MIT. |
| Graphiti (`graphiti-core`) | Apache-2.0 | A Python dependency of `kb/`. |
| Temporal Python SDK (`temporalio`) | MIT | A Python dependency of `kb/`. |
| Temporal server and CLI (`temporalio/temporal`) | MIT | Image reference only (development server in the compose stack). |
| Text embeddings inference | Apache-2.0 | Image reference only. |
| `redis_exporter` | MIT | Image reference only. |
| PostgreSQL | PostgreSQL licence | Image reference only. |

The published `kio-kb`, `kio-mcp` and `kio-vault` images are Apache-2.0 (the `org.opencontainers.image.licenses` label) plus the licences of what they install.

One finding the owner should decide on: `vault/Dockerfile` installs `obsidian-headless` from npm, whose package metadata says `UNLICENSED` (all rights reserved). It is not in the source tree, so the repository is unaffected, but a `kio-vault` image published to a public registry would redistribute it. Options: build and publish that image only privately, or confirm the terms with Obsidian before publishing it.

## Third-party text in the repository

`standards/` and `skills/` were checked. They cite public style guides (Google Go and Shell style guides, Uber Go guide, Kubernetes coding conventions, PEP 8 and 484) by link in `standards/common.md` and quote none of them. The templates under `standards/templates/` are original, and the pre-commit template names public hook repositories by URL only. No text there is copied from elsewhere.
