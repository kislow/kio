# Go standards

Read with `common.md`. Target: the latest stable Go (check `go version`). Keep the `go` directive in `go.mod` current, and upgrade promptly. Standard library first. For production Go, *100 Go Mistakes and How to Avoid Them* (Teiva Harsanyi) is the further reading; *Effective Go* is still useful for idioms but predates modules, generics and `slog`.

## Project layout

```
mysvc/
├── go.mod
├── .golangci.yml
├── Makefile
├── cmd/mysvc/main.go     # thin: wires config, logger, signal context, calls run()
├── internal/
│   ├── config/           # env parsing and validation
│   ├── server/           # http.Server setup, handlers, middleware
│   ├── <domain>/         # business logic; depends on small interfaces
│   └── store/            # persistence
├── pkg/                  # only if intentionally public; otherwise omit
└── deploy/               # Dockerfile, Helm chart, manifests
```

- **MUST** keep `main` tiny: parse config, build dependencies, call `run(ctx) error`, exit non-zero on error. `os.Exit` and `log.Fatal` only there.
- **MUST** keep package names lowercase, no underscores, matching the directory. No packages named `util`, `common` or `helpers`; name them for what they do.
- **SHOULD** use `internal/` by default; promote to `pkg/` only when another module needs it.

## Errors

- **MUST** wrap with context: `fmt.Errorf("fetching user %q: %w", id, err)`. Lowercase, no trailing punctuation.
- **MUST** check errors with `errors.Is` / `errors.As`, not string comparison or type assertions.
- **MUST NOT** discard errors with `_` unless commented. Handle an error once: either log it or return it, not both.
- **MUST NOT** `panic` for expected failures. Panic is for programmer errors; recover at boundaries (HTTP middleware, goroutine entry).
- **SHOULD** define sentinel errors (`ErrNotFound`) or typed errors at package boundaries so callers can branch on them.

## Context and goroutines

- **MUST** pass `ctx context.Context` as the first parameter of anything doing I/O or blocking, and respect cancellation. `context.Background()` appears only in `main` and tests.
- **MUST** create timeouts at boundaries (`context.WithTimeout`) for outbound calls. Set `http.Client.Timeout`, and `ReadHeaderTimeout` / `ReadTimeout` / `WriteTimeout` / `IdleTimeout` on `http.Server`. Never use `http.DefaultClient`.
- **MUST** give every goroutine an owner and a way to stop: `errgroup.WithContext`, a `sync.WaitGroup` plus cancel, or a closed channel. No fire-and-forget goroutines in services.
- **MUST** handle `SIGINT`/`SIGTERM` with `signal.NotifyContext` and shut down gracefully (`server.Shutdown(ctx)`), since we run on Kubernetes.
- **MUST** run tests with `-race` in CI.
- **MUST NOT** copy a struct that contains a mutex, or embed a mutex. Name it `lock` (or `stateLock` when several), as Kubernetes does.
- **SHOULD** share memory by communicating: use channels to hand off ownership, a mutex to guard shared state. Bound concurrency (`errgroup.SetLimit`, worker pool) instead of one goroutine per item.
- **SHOULD** prefer synchronous APIs; let callers add concurrency.

## Logging

- **MUST** use `log/slog` for services, with JSON output in production and key/value pairs (`slog.Info("fetched", "user", id, "dur", d)`). Pass the logger in; no package-level mutable logger.
- Kubernetes controllers and operators may use `klog`/`logr` where the ecosystem requires it.
- **SHOULD** log at the edge (where an error is handled), not at every layer.

## Design, state, and the garbage collector

- **MUST NOT** use `init()` or package-level mutable state. Pass dependencies explicitly.
- **SHOULD** accept interfaces and return structs; define small interfaces in the package that consumes them.
- **SHOULD** write clear code first and optimise with evidence. Use `pprof` and `go test -bench -benchmem` before changing allocation behaviour.
- **SHOULD** preallocate slices and maps when the size is known (`make([]T, 0, n)`), avoid retaining large slices through small sub-slices, and use `sync.Pool` only after profiling shows allocation pressure.
- **SHOULD** set `GOMEMLIMIT` (about 90% of the container limit) in containers so the GC works with the memory limit instead of getting OOM-killed.
- **SHOULD** `defer` cleanup right after acquiring a resource (`defer resp.Body.Close()`), and close what you open.

## Tests

- **MUST** write table-driven tests with `t.Run` for multiple inputs; assert on behaviour, not constants. Test the exported behaviour of the package.
- **MUST** include a `-cover` threshold in CI (80%+ on critical paths).
- **SHOULD** call `t.Parallel()` where tests are independent (Kubernetes itself rarely does, so this is our rule rather than theirs), and use `testing/synctest` or `Eventually`-style waits for async code, never `time.Sleep`.
- **SHOULD** use fakes and small interfaces instead of heavy mocking frameworks. Use `testify` only if the team agrees on one style.

## Containers

Multi-stage Docker builds, non-root user, pinned base image, and a `.dockerignore`.

## Examples

```go
// no
if err != nil {
	log.Printf("error: %v", err)
	return err
}

// yes: handle once, with context
if err != nil {
	return fmt.Errorf("loading config %q: %w", path, err)
}
```

```go
// no
if err.Error() == "not found" { ... }

// yes
if errors.Is(err, store.ErrNotFound) { ... }
```

## Templates

| File | Use |
| --- | --- |
| `standards/templates/go/.golangci.yml` | golangci-lint v2 config enforcing the rules above |
| `standards/templates/go/main.go.tmpl` | Service skeleton: signal context, `slog`, server timeouts, errgroup, graceful shutdown |

## Checks

| Step | Command |
| --- | --- |
| Format | `gofmt` / `goimports` (`gofumpt` through golangci-lint) |
| Vet | `go vet ./...` |
| Lint | `golangci-lint run` with the template config |
| Tests | `go test -race -cover ./...` |
