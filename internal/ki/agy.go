package ki

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// AgySettingsPath is agy's user settings file under home. Its
// permissions.allow rules are what headless agy runs without a prompt.
func AgySettingsPath(home string) string {
	return filepath.Join(home, ".gemini", "antigravity-cli", "settings.json")
}

// AgyRule is agy's permission rule for one allow-list entry: command(git),
// command(git status).
func AgyRule(entry string) string { return "command(" + entry + ")" }

// EnsureAgyAllow adds command(<entry>) to permissions.allow in agy's settings
// file for every entry not already there, and returns the entries it added.
// It only merges: existing rules keep their order, the deny and ask lists and
// every other key stay as they are. The file is written through a temporary
// file in the same folder and a rename, mode 0600, creating the folder and
// the file when missing. A file that is not a JSON object is an error and is
// never overwritten.
func EnsureAgyAllow(path string, entries []string) ([]string, error) {
	if len(entries) == 0 {
		return nil, nil
	}
	// Write through a symlink to the file it points at, not over the link.
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	data, err := os.ReadFile(path) // #nosec G304 -- agy's settings path, from KI_AGY_SETTINGS or the home dir
	missing := errors.Is(err, os.ErrNotExist)
	if err != nil && !missing {
		return nil, fmt.Errorf("reading agy settings %s: %w", path, err)
	}
	var top []member
	if !missing {
		if !json.Valid(data) {
			return nil, fmt.Errorf("agy settings %s is not valid JSON; fix it by hand, ki will not overwrite it", path)
		}
		if top, err = readObject(data); err != nil {
			return nil, fmt.Errorf("agy settings %s: %w", path, err)
		}
	}
	var perms []member
	if raw, ok := lookup(top, "permissions"); ok && !isNull(raw) {
		if perms, err = readObject(raw); err != nil {
			return nil, fmt.Errorf("agy settings %s: permissions: %w", path, err)
		}
	}
	var allow []json.RawMessage
	if raw, ok := lookup(perms, "allow"); ok {
		if err := json.Unmarshal(raw, &allow); err != nil {
			return nil, fmt.Errorf("agy settings %s: permissions.allow is not a list: %w", path, err)
		}
	}
	have := map[string]bool{}
	for _, r := range allow {
		var s string
		if json.Unmarshal(r, &s) == nil {
			have[s] = true
		}
	}
	var added []string
	for _, e := range entries {
		rule := AgyRule(e)
		if have[rule] {
			continue
		}
		raw, err := marshalJSON(rule)
		if err != nil {
			return nil, err
		}
		allow = append(allow, raw)
		have[rule] = true
		added = append(added, e)
	}
	if len(added) == 0 {
		return nil, nil
	}
	allowRaw, err := marshalJSON(allow)
	if err != nil {
		return nil, err
	}
	permsRaw, err := writeObject(set(perms, "allow", allowRaw))
	if err != nil {
		return nil, err
	}
	out, err := writeObject(set(top, "permissions", permsRaw))
	if err != nil {
		return nil, err
	}
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, out, "", "  "); err != nil {
		return nil, fmt.Errorf("formatting agy settings: %w", err)
	}
	pretty.WriteByte('\n')
	if err := writeAtomic(path, pretty.Bytes()); err != nil {
		return nil, err
	}
	return added, nil
}

// member is one key of a JSON object, its value kept as written, so a
// rewrite keeps the keys' order and every value ki does not touch.
type member struct {
	key string
	val json.RawMessage
}

var errNotObject = errors.New("not a JSON object")

func readObject(data []byte) ([]member, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return nil, errNotObject
	}
	var ms []member
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, fmt.Errorf("reading a key: %w", err)
		}
		key, _ := tok.(string)
		var val json.RawMessage
		if err := dec.Decode(&val); err != nil {
			return nil, fmt.Errorf("reading %q: %w", key, err)
		}
		ms = append(ms, member{key: key, val: val})
	}
	if _, err := dec.Token(); err != nil {
		return nil, fmt.Errorf("reading the closing brace: %w", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errNotObject
	}
	return ms, nil
}

func writeObject(ms []member) ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, m := range ms {
		if i > 0 {
			b.WriteByte(',')
		}
		k, err := marshalJSON(m.key)
		if err != nil {
			return nil, err
		}
		b.Write(k)
		b.WriteByte(':')
		b.Write(m.val)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

func lookup(ms []member, key string) (json.RawMessage, bool) {
	for _, m := range ms {
		if m.key == key {
			return m.val, true
		}
	}
	return nil, false
}

// set replaces key's value in place, or appends the key when it is missing.
func set(ms []member, key string, val json.RawMessage) []member {
	for i := range ms {
		if ms[i].key == key {
			ms[i].val = val
			return ms
		}
	}
	return append(ms, member{key: key, val: val})
}

func isNull(raw json.RawMessage) bool { return string(bytes.TrimSpace(raw)) == "null" }

// marshalJSON encodes without HTML escaping, so a rule reads as written.
func marshalJSON(v any) (json.RawMessage, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, fmt.Errorf("encoding agy settings: %w", err)
	}
	return bytes.TrimSuffix(b.Bytes(), []byte("\n")), nil
}

// writeAtomic replaces path with data, mode 0600: a temporary file in the
// same folder, synced, then renamed over it, so agy never reads half a file.
func writeAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("creating %s: %w", dir, err)
	}
	f, err := os.CreateTemp(dir, ".settings-*.tmp")
	if err != nil {
		return fmt.Errorf("writing agy settings: %w", err)
	}
	tmp := f.Name()
	defer os.Remove(tmp) // a no-op once renamed
	// CreateTemp already makes the file 0600; the chmod states it.
	err = f.Chmod(0o600)
	if err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	if err = errors.Join(err, f.Close()); err != nil {
		return fmt.Errorf("writing %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("replacing %s: %w", path, err)
	}
	return nil
}
