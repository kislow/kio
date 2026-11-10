// Package scope decides which brains a request may read.
//
// The rules are deliberately small and fail closed:
//   - A token grants a fixed set of brains. Nothing outside that set is ever readable.
//   - The harness sends the brains a session wants (from projects.yaml).
//     Asking for a brain the token does not grant is an error, not a silent drop,
//     so a misconfigured session is visible immediately.
//   - With no requested brains, the session gets every granted brain except
//     sensitive ones. Sensitive brains are only ever included by name.
//   - Cortex is always included when the token grants it.
//   - A grant may name a family, "client-*", which covers every brain named
//     "client-<suffix>". Families are never expanded by default: their members
//     are only read when named, so a new sub-project brain needs no new grant and
//     never leaks into a default scope.
package scope

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Cortex is the shared brain every scope builds on.
const Cortex = "cortex"

var nameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// ErrForbidden is returned when a request asks for a brain its token does not grant.
var ErrForbidden = errors.New("brain not permitted for this token")

// ValidName reports whether s is a well-formed brain name.
func ValidName(s string) bool { return nameRE.MatchString(s) }

// FamilySuffix marks a family pattern: "client-*" covers "client-<suffix>".
const FamilySuffix = "-*"

// IsFamily reports whether s is written as a family pattern.
func IsFamily(s string) bool { return strings.HasSuffix(s, FamilySuffix) }

// ValidFamily reports whether s is a well-formed family pattern: a valid name
// followed by "-*".
func ValidFamily(s string) bool {
	return IsFamily(s) && ValidName(strings.TrimSuffix(s, FamilySuffix))
}

// ValidEntry reports whether s is a valid brain name or family pattern.
func ValidEntry(s string) bool { return ValidName(s) || ValidFamily(s) }

// Matches reports whether name is entry itself, or a member of the family entry.
// A family never matches its own base name: "client-*" does not match "client".
func Matches(entry, name string) bool {
	if !IsFamily(entry) {
		return entry == name
	}
	prefix := strings.TrimSuffix(entry, "*")
	return strings.HasPrefix(name, prefix) && len(name) > len(prefix) && ValidName(name)
}

// MatchesAny reports whether name matches any of the entries.
func MatchesAny(entries []string, name string) bool {
	for _, e := range entries {
		if Matches(e, name) {
			return true
		}
	}
	return false
}

// ParseHeader splits a comma-separated brain list (the X-Ki-Brains header).
func ParseHeader(v string) ([]string, error) {
	var out []string
	for _, part := range strings.Split(v, ",") {
		b := strings.TrimSpace(part)
		if b == "" {
			continue
		}
		if !ValidName(b) {
			return nil, fmt.Errorf("invalid brain name %q", b)
		}
		out = append(out, b)
	}
	return out, nil
}

// Resolve returns the sorted brains a request may read.
// sensitive reports whether a brain must only be included when explicitly requested.
func Resolve(granted, requested []string, sensitive func(string) bool) ([]string, error) {
	result := map[string]bool{}
	if len(requested) == 0 {
		for _, b := range granted {
			if !IsFamily(b) && !sensitive(b) {
				result[b] = true
			}
		}
	} else {
		for _, b := range requested {
			if !ValidName(b) || !MatchesAny(granted, b) {
				return nil, fmt.Errorf("%w: %s", ErrForbidden, b)
			}
			result[b] = true
		}
	}
	if MatchesAny(granted, Cortex) {
		result[Cortex] = true
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("%w: token grants no readable brains", ErrForbidden)
	}

	out := make([]string, 0, len(result))
	for b := range result {
		out = append(out, b)
	}
	sort.Strings(out)
	return out, nil
}
