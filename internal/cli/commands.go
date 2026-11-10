package cli

import (
	"context"
	"strings"
)

// extension is a command that lives in its own files and registers itself from
// init(): removing those files removes the command, its usage lines and its
// doctor rows, with nothing else to edit.
type extension struct {
	name string
	// usage holds the command's lines for `ki help`, each ending in a newline.
	usage  string
	run    func(a *App, ctx context.Context, args []string) error
	doctor func(a *App, c *checks)
}

var extensions []extension

func register(e extension) { extensions = append(extensions, e) }

func findExtension(name string) (extension, bool) {
	for _, e := range extensions {
		if e.name == name {
			return e, true
		}
	}
	return extension{}, false
}

// usage is the text `ki help` prints: the core commands, the registered
// extensions, then the version line.
func usage() string {
	var b strings.Builder
	b.WriteString(coreUsage)
	for _, e := range extensions {
		b.WriteString(e.usage)
	}
	b.WriteString("  ki version")
	return b.String()
}
