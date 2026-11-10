package ki

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestEnsureAgyAllow(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		existing  *string // nil: no file
		entries   []string
		wantAdded []string
		want      string // the file afterwards, compared as JSON; "" means unchanged
		wantErr   string
	}{
		{
			name: "missing file", entries: []string{"git", "go"}, wantAdded: []string{"git", "go"},
			want: `{"permissions":{"allow":["command(git)","command(go)"]}}`,
		},
		{
			name:     "existing rules kept in order",
			existing: ptr(`{"permissions":{"allow":["command(make)","read_file(*)"]}}`),
			entries:  []string{"git", "make"}, wantAdded: []string{"git"},
			want: `{"permissions":{"allow":["command(make)","read_file(*)","command(git)"]}}`,
		},
		{
			name:     "other keys and the deny and ask lists kept",
			existing: ptr(`{"colorScheme":"dark","trustedWorkspaces":["/w"],"permissions":{"deny":["command(rm)"],"ask":["command(curl)"]},"nested":{"n":1.50}}`),
			entries:  []string{"git status"}, wantAdded: []string{"git status"},
			want: `{"colorScheme":"dark","trustedWorkspaces":["/w"],"permissions":{"deny":["command(rm)"],"ask":["command(curl)"],"allow":["command(git status)"]},"nested":{"n":1.50}}`,
		},
		{
			name:     "nothing to add leaves the file alone",
			existing: ptr(`{ "permissions": { "allow": [ "command(git)" ] } }`),
			entries:  []string{"git"},
		},
		{
			name:     "duplicates not added",
			existing: ptr(`{"permissions":{"allow":["command(git)"]}}`),
			entries:  []string{"git", "go", "go"}, wantAdded: []string{"go"},
			want: `{"permissions":{"allow":["command(git)","command(go)"]}}`,
		},
		{
			name: "null permissions", existing: ptr(`{"permissions":null}`),
			entries: []string{"git"}, wantAdded: []string{"git"},
			want: `{"permissions":{"allow":["command(git)"]}}`,
		},
		{
			name: "empty object", existing: ptr(`{}`),
			entries: []string{"gh"}, wantAdded: []string{"gh"},
			want: `{"permissions":{"allow":["command(gh)"]}}`,
		},
		{name: "invalid JSON refused", existing: ptr(`{"permissions": {`), entries: []string{"git"}, wantErr: "is not valid JSON"},
		{name: "empty file refused", existing: ptr(``), entries: []string{"git"}, wantErr: "is not valid JSON"},
		{name: "not an object", existing: ptr(`["command(git)"]`), entries: []string{"git"}, wantErr: "not a JSON object"},
		{name: "permissions not an object", existing: ptr(`{"permissions":["x"]}`), entries: []string{"git"}, wantErr: "permissions: not a JSON object"},
		{name: "allow not a list", existing: ptr(`{"permissions":{"allow":"command(git)"}}`), entries: []string{"git"}, wantErr: "permissions.allow is not a list"},
		{name: "no entries, no file", entries: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "antigravity-cli", "settings.json")
			if tt.existing != nil {
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(*tt.existing), 0o644); err != nil { // #nosec G306 -- a looser mode the merge must tighten
					t.Fatal(err)
				}
			}
			added, err := EnsureAgyAllow(path, tt.entries)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) || !strings.Contains(err.Error(), path) {
					t.Fatalf("want an error naming the file and containing %q, got %v", tt.wantErr, err)
				}
				if got, _ := os.ReadFile(path); string(got) != *tt.existing {
					t.Fatalf("a refused file must not change: %q", got)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(added, tt.wantAdded) {
				t.Fatalf("added %v, want %v", added, tt.wantAdded)
			}
			got, readErr := os.ReadFile(path)
			if tt.want == "" {
				switch {
				case tt.existing == nil && readErr == nil:
					t.Fatalf("no entries must not create the file: %s", got)
				case tt.existing != nil && string(got) != *tt.existing:
					t.Fatalf("unchanged file rewritten: %q", got)
				}
				return
			}
			if readErr != nil {
				t.Fatal(readErr)
			}
			if !sameJSON(t, got, []byte(tt.want)) || !strings.HasSuffix(string(got), "}\n") {
				t.Fatalf("file\n%s\nwant %s", got, tt.want)
			}
			info, err := os.Stat(path)
			if err != nil || info.Mode().Perm() != 0o600 {
				t.Fatalf("mode %v, %v; want 0600", info.Mode().Perm(), err)
			}
			if leftovers, _ := filepath.Glob(filepath.Join(filepath.Dir(path), ".settings-*")); len(leftovers) != 0 {
				t.Fatalf("temporary files left: %v", leftovers)
			}
		})
	}
}

func TestEnsureAgyAllowKeepsKeyOrderAndLiterals(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "settings.json")
	in := `{"zeta":1,"alpha":{"b":2,"a":"<&>"},"permissions":{"ask":[],"allow":["command(make)"],"deny":[]},"big":12345678901234567890}`
	if err := os.WriteFile(path, []byte(in), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureAgyAllow(path, []string{"git"}); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s := string(got)
	order := []string{`"zeta"`, `"alpha"`, `"b"`, `"a": "<&>"`, `"permissions"`, `"ask"`, `"allow"`, `"command(make)"`, `"command(git)"`, `"deny"`, `"big": 12345678901234567890`}
	at := 0
	for _, want := range order {
		i := strings.Index(s[at:], want)
		if i < 0 {
			t.Fatalf("%s missing or out of order in\n%s", want, s)
		}
		at += i + len(want)
	}
}

func TestEnsureAgyAllowWritesThroughASymlink(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	real := filepath.Join(dir, "real.json")
	link := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(real, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, link); err != nil {
		t.Skip("no symlinks here:", err)
	}
	if _, err := EnsureAgyAllow(link, []string{"git"}); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("the link was replaced: %v", err)
	}
	if got, _ := os.ReadFile(real); !strings.Contains(string(got), "command(git)") {
		t.Fatalf("the target was not updated: %s", got)
	}
}

func TestEnsureAgyAllowUnreadable(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// A directory where the file should be cannot be read as a file.
	if _, err := EnsureAgyAllow(dir, []string{"git"}); err == nil || !strings.Contains(err.Error(), "reading agy settings") {
		t.Fatalf("got %v", err)
	}
	// A file where the folder should be cannot hold the settings.
	blocked := filepath.Join(dir, "file")
	if err := os.WriteFile(blocked, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeAtomic(filepath.Join(blocked, "sub", "settings.json"), []byte("{}")); err == nil || !strings.Contains(err.Error(), "creating") {
		t.Fatalf("got %v", err)
	}
	// A folder ki cannot write in fails before anything is replaced.
	locked := filepath.Join(dir, "locked")
	if err := os.Mkdir(locked, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) }) // #nosec G302 -- let TempDir clean up
	if os.Getuid() == 0 {
		t.Skip("root writes anywhere")
	}
	if _, err := EnsureAgyAllow(filepath.Join(locked, "settings.json"), []string{"git"}); err == nil || !strings.Contains(err.Error(), "writing agy settings") {
		t.Fatalf("got %v", err)
	}
}

func TestAgyPaths(t *testing.T) {
	t.Parallel()
	if got := AgySettingsPath("/h"); got != filepath.Join("/h", ".gemini", "antigravity-cli", "settings.json") {
		t.Fatalf("got %s", got)
	}
	if got := AgyRule("git status"); got != "command(git status)" {
		t.Fatalf("got %s", got)
	}
}

func sameJSON(t *testing.T, a, b []byte) bool {
	t.Helper()
	var x, y any
	if err := json.Unmarshal(a, &x); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, a)
	}
	if err := json.Unmarshal(b, &y); err != nil {
		t.Fatalf("bad want: %v", err)
	}
	return reflect.DeepEqual(x, y)
}

func ptr(s string) *string { return &s }
