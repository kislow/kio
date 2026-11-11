package cli

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/kislow/kio/internal/gitisolate"
)

// TestMain lets the test binary stand in for ki: the hooks ki gives a worker
// run `<ki> delegate guard ...`, and the binary they find is this one.
func TestMain(m *testing.M) {
	if len(os.Args) > 2 && os.Args[1] == "delegate" && os.Args[2] == "guard" {
		os.Exit(runGuardAsKi())
	}
	os.Exit(gitisolate.Main(m.Run))
}

func runGuardAsKi() int {
	if err := New(os.Stdout, os.Stderr, "test").Run(context.Background(), os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "ki:", err)
		return 1
	}
	return 0
}
