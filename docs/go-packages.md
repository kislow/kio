# Go packages

Most of the harness's Go code is in `internal/` and cannot be imported from another module. Four packages are public, for projects that build on the harness; see the [decision](../decisions/2026-10-25-public-go-packages.md). Import paths start with the module path declared in this repository's `go.mod`, shown here as `<module>`.

| Package | What |
| --- | --- |
| `<module>/pkg/projects` | Parse and validate `projects.yaml`: projects, identities, ingest rules, brains |
| `<module>/pkg/scope` | Brain names, families (`client-*`) and scope resolution |
| `<module>/pkg/config` | The MCP server configuration and token store |
| `<module>/pkg/sessions` | Versioned agent session mirror wire types and validation |

They are versioned with the harness: pin a release tag, and read the changelog for breaking changes (`feat!:` or `fix!:`) before moving the pin. Everything else a consumer needs goes through the ki-mcp API or the `ki` CLI (`--json`), not through Go imports.

## Using them

```bash
go get <module>@vX.Y.Z      # a release tag, never a branch
```

The tag lands in your `go.mod` and the checksum in `go.sum`, so a build is reproducible. To move the pin, read the changelog between the two tags first, then `go get <module>@vX.Y.Z` again and run your tests.
