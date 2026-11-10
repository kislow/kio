package kb

import (
	"strings"
	"unicode"
)

// Resolve returns each fact once: a repeat of the same kind and text, however
// spaced or cased, is dropped. Facts keep their original order; the input is
// not modified.
func Resolve(facts []Fact) []Fact {
	out := make([]Fact, 0, len(facts))
	seen := map[string]bool{}
	for _, f := range facts {
		key := string(f.Kind) + "|" + normalize(f.Fact)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, f)
	}
	return out
}

func normalize(s string) string {
	return strings.Join(strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '.' && r != '_'
	}), " ")
}
