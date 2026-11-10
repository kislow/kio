// Command covercheck fails when statement coverage of the named packages, read
// from a `go test -coverprofile` file, is below a threshold. Go has no built-in
// coverage gate; CI runs this after the tests.
//
//	go run ./internal/tools/covercheck -min 80 cover.out pkg/scope pkg/config
package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
)

func main() {
	os.Exit(realMain(os.Args[1:], os.Stdout, os.Stderr))
}

func realMain(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("covercheck", flag.ContinueOnError)
	fs.SetOutput(stderr)
	minPct := fs.Float64("min", 80, "minimum statement coverage per package, in percent")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() < 2 {
		fmt.Fprintln(stderr, "usage: covercheck [-min N] <profile> <package dir>...")
		return 2
	}
	f, err := os.Open(fs.Arg(0))
	if err != nil {
		fmt.Fprintln(stderr, "covercheck:", err)
		return 2
	}
	defer f.Close()
	cov, err := parseProfile(f)
	if err != nil {
		fmt.Fprintln(stderr, "covercheck:", err)
		return 2
	}
	failed := false
	for _, dir := range fs.Args()[1:] {
		pct, ok := cov.percent(dir)
		switch {
		case !ok:
			fmt.Fprintf(stdout, "FAIL %s: no coverage data\n", dir)
			failed = true
		case pct < *minPct:
			fmt.Fprintf(stdout, "FAIL %s: %.1f%% < %.0f%%\n", dir, pct, *minPct)
			failed = true
		default:
			fmt.Fprintf(stdout, "ok   %s: %.1f%%\n", dir, pct)
		}
	}
	if failed {
		return 1
	}
	return 0
}

// coverage maps a package import path to covered and total statements.
type coverage map[string][2]int

var errProfile = errors.New("malformed coverage profile")

// parseProfile reads lines like "pkg/file.go:1.2,3.4 5 1" (statements, count).
func parseProfile(r io.Reader) (coverage, error) {
	type block struct {
		pkg     string
		stmts   int
		covered bool
	}
	// With several test binaries a block can appear more than once; count it once,
	// as covered if any run covered it.
	blocks := map[string]block{}
	sc := bufio.NewScanner(r)
	for n := 1; sc.Scan(); n++ {
		line := sc.Text()
		if n == 1 && strings.HasPrefix(line, "mode:") || line == "" {
			continue
		}
		pos, rest, ok := strings.Cut(line, " ")
		fields := strings.Fields(rest)
		file, _, okFile := strings.Cut(pos, ":")
		if !ok || !okFile || len(fields) != 2 {
			return nil, fmt.Errorf("%w: line %d", errProfile, n)
		}
		stmts, err1 := strconv.Atoi(fields[0])
		count, err2 := strconv.Atoi(fields[1])
		if err1 != nil || err2 != nil {
			return nil, fmt.Errorf("%w: line %d", errProfile, n)
		}
		b := blocks[pos]
		b.pkg, b.stmts = file[:max(strings.LastIndex(file, "/"), 0)], stmts
		b.covered = b.covered || count > 0
		blocks[pos] = b
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("reading profile: %w", err)
	}
	cov := coverage{}
	for _, b := range blocks {
		c := cov[b.pkg]
		c[1] += b.stmts
		if b.covered {
			c[0] += b.stmts
		}
		cov[b.pkg] = c
	}
	return cov, nil
}

// percent returns coverage for the package whose import path ends in dir.
func (c coverage) percent(dir string) (float64, bool) {
	dir = strings.Trim(dir, "/")
	pkgs := make([]string, 0, len(c))
	for p := range c {
		pkgs = append(pkgs, p)
	}
	sort.Strings(pkgs)
	for _, p := range pkgs {
		if p == dir || strings.HasSuffix(p, "/"+dir) {
			v := c[p]
			if v[1] == 0 {
				return 0, false
			}
			return 100 * float64(v[0]) / float64(v[1]), true
		}
	}
	return 0, false
}
