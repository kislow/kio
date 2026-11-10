# Bash standards

Read with `common.md`. Use bash for glue under about 50 to 100 lines. If you need data structures, JSON parsing or retries, use Python. Scripts must also run on macOS (bash 3.2), so no associative arrays and no `mapfile` unless the script installs a newer bash.

## Rules

- **MUST** start with `#!/usr/bin/env bash` and `set -Eeuo pipefail`, then follow the template (`standards/templates/bash/script.sh`).
- **MUST** quote every variable expansion: `"$var"`, `"${arr[@]}"`. Use `[[ ... ]]` and `$(...)`, never backticks.
- **MUST** declare function variables `local`, constants `readonly`, and require inputs with `${VAR:?message}`.
- **MUST** create temp files with `mktemp` and clean up in a `trap ... EXIT`.
- **MUST** send diagnostics to stderr with a `log`/`die` helper (`printf`, with timestamps). Use `printf` over `echo` for anything with variables.
- **MUST** validate arguments, and provide `--dry-run` for scripts that delete or modify.
- **MUST** pass `shellcheck` with no warnings and `shfmt -w -i 2`.
- **MUST NOT** parse `ls`, put secrets in arguments or the script (they show up in `ps`), or run `rm -rf` on a computed path without checking that it is non-empty and inside the expected directory.
- **SHOULD** wrap logic in `main "$@"` and avoid `exit` inside helpers; return a status and let `main` decide.

## Examples

```bash
# no
rm -rf $BUILD_DIR/*

# yes
[[ -n "$build_dir" && "$build_dir" == "$PWD"/build* ]] || die "refusing unsafe path: $build_dir"
run rm -rf -- "$build_dir"
```

```bash
# no
echo "deploying $env"
curl https://api.example.com/deploy -H "Authorization: Bearer $TOKEN"

# yes
log "deploying $env"
curl --fail --silent --show-error --max-time 30 \
  -H @<(printf 'Authorization: Bearer %s\n' "${TOKEN:?TOKEN must be set}") \
  https://api.example.com/deploy
```

## Templates

| File | Use |
| --- | --- |
| `standards/templates/bash/script.sh` | Strict-mode script with `log`/`die`, `trap` cleanup, `--dry-run`, argument checks |

## Checks

| Step | Command |
| --- | --- |
| Format | `shfmt -d -i 2 <files>` (`-w` to fix) |
| Lint | `shellcheck <files>` |
| Tests | `bats` for anything non-trivial |
