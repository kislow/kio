package cli

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
)

// withExtension registers e for one test and restores the registry afterwards.
func withExtension(t *testing.T, e extension) {
	t.Helper()
	saved := slices.Clone(extensions)
	t.Cleanup(func() { extensions = saved })
	register(e)
}

func TestRegisterMakesExtensionDiscoverable(t *testing.T) {
	if _, ok := findExtension("acme"); ok {
		t.Fatal("acme is registered before the test registers it")
	}
	withExtension(t, extension{name: "acme", usage: "  ki acme   run the acme command\n"})

	got, ok := findExtension("acme")
	if !ok || got.name != "acme" {
		t.Fatalf("findExtension(acme) = %+v, %v", got, ok)
	}
	if _, ok := findExtension("other"); ok {
		t.Error("findExtension(other) found an unregistered command")
	}

	u := usage()
	if !strings.Contains(u, "  ki acme   run the acme command\n") {
		t.Errorf("usage() lacks the extension lines:\n%s", u)
	}
	if !strings.HasPrefix(u, coreUsage) || !strings.HasSuffix(u, "  ki version") {
		t.Errorf("usage() lost the core or version lines:\n%s", u)
	}
}

func TestRunDispatchesToExtension(t *testing.T) {
	var gotArgs []string
	var out bytes.Buffer
	app := &App{Stdout: &out}
	withExtension(t, extension{
		name: "acme",
		run: func(a *App, _ context.Context, args []string) error {
			if a != app {
				t.Error("extension got another App")
			}
			gotArgs = args
			return errors.New("acme failed")
		},
	})

	err := app.Run(context.Background(), []string{"acme", "--flag", "x"})
	if err == nil || err.Error() != "acme failed" {
		t.Fatalf("Run err = %v, want the extension's error", err)
	}
	if want := []string{"--flag", "x"}; !slices.Equal(gotArgs, want) {
		t.Errorf("extension args = %v, want %v", gotArgs, want)
	}
}

func TestExtensionsRestoredAfterTest(t *testing.T) {
	before := len(extensions)
	t.Run("register", func(t *testing.T) {
		withExtension(t, extension{name: "acme"})
		if len(extensions) != before+1 {
			t.Fatalf("len(extensions) = %d, want %d", len(extensions), before+1)
		}
	})
	if len(extensions) != before {
		t.Errorf("len(extensions) = %d after cleanup, want %d", len(extensions), before)
	}
}
