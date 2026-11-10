// Command ki starts agent sessions scoped to the current repo's brains and
// GitHub identity, as defined in the central projects file. See `ki help`.
//
// Nothing is written into the repo except `ki cursor`'s .cursor/mcp.json and
// .cursor/rules/ki.mdc, and only when git already ignores both files.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/kislow/kio/internal/cli"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	os.Exit(realMain())
}

func realMain() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := cli.New(os.Stdout, os.Stderr, version).Run(ctx, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "ki:", err)
		return 1
	}
	return 0
}
