package ki

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// CursorSandboxFile is where Cursor reads a workspace's sandbox policy.
const CursorSandboxFile = ".cursor/sandbox.json"

// cursorSandboxPolicy allows what `gh` needs and nothing else: a GraphQL or
// REST call goes to api.github.com, git push already works over github.com.
const cursorSandboxPolicy = `{
  "networkPolicy": {
    "allow": ["api.github.com"]
  }
}
`

// EnsureCursorSandbox writes the workspace's sandbox policy so a headless
// Cursor worker in auto mode can reach api.github.com; there is nobody to
// approve the network prompt. It reports whether it wrote the file. An
// existing file is the repo's own policy and is never touched.
func EnsureCursorSandbox(dir string) (bool, error) {
	path := filepath.Join(dir, filepath.FromSlash(CursorSandboxFile))
	if _, err := os.Stat(path); err == nil {
		return false, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, fmt.Errorf("reading %s: %w", path, err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return false, fmt.Errorf("creating %s: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(cursorSandboxPolicy), 0o600); err != nil {
		return false, fmt.Errorf("writing %s: %w", path, err)
	}
	return true, nil
}

// AppendLineOnce adds line to the file at path unless a line equal to it is
// already there, creating the file and its folder when missing.
func AppendLineOnce(path, line string) error {
	data, err := os.ReadFile(path) // #nosec G304 -- git's exclude file, path from git itself
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("reading %s: %w", path, err)
	}
	for _, l := range bytes.Split(data, []byte("\n")) {
		if string(bytes.TrimSpace(l)) == line {
			return nil
		}
	}
	if len(data) > 0 && !bytes.HasSuffix(data, []byte("\n")) {
		data = append(data, '\n')
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return fmt.Errorf("creating %s: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, append(data, line+"\n"...), 0o600); err != nil { // #nosec G703 -- git's exclude file, path from git itself
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}
