package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/kislow/kio/internal/kb"
	"github.com/kislow/kio/internal/ki"
	"github.com/kislow/kio/pkg/scope"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const kbUsage = `usage: ki kb brains  [--brain B]... [--identity NAME] [--json]
       ki kb search  "<query>" [--brain B]... [--all] [--limit N] [--identity NAME] [--json]
       ki kb recent  [--brain B]... [--all] [--since 7d] [--limit N] [--identity NAME] [--json]
       ki kb activity [--brain B]... [--all] [--since 30d] [--identity NAME] [--json]
       ki kb graph --brain B (--query Q | --node ID) [--limit 30] [--identity NAME] [--json]
       ki kb context "<query>" [--brain B]... [--all] [--budget N] [--identity NAME] [--json]

Read-only views of the knowledge base through the same server, token and brain rules as a
session. brains lists every brain this machine can reach with its document count, last ingest
time and the projects that use it. search ranks facts with their source, a snippet and a
GitHub link when the source has one; recent lists the newest documents; activity counts the documents each brain ingested per
day, by ingest time rather than commit time (default --since 30d); context prints the
pack get_context gives agents; graph prints the facts of one brain as entity -[relation]-> entity lines,
around a query or around one entity (a node id from --json output), at most 60 (default 30).

Without --brain or --all the scope is the brains a session in this repo gets, else cortex plus
every non-sensitive brain of the identity's projects (--identity, else the repo's, else the
machine's only one). --all is every brain the token reads except voice; --brain adds the ones
named, sensitive brains included. --json prints one stable object for a UI.`

const (
	kbTimeout        = time.Minute
	kbSnippetRunes   = 240
	kbDefaultLimit   = 15
	kbMaxSearchLimit = 50
	kbDefaultRecent  = 20
	kbMaxRecent      = 100
	kbDefaultGraph   = 30
	kbMaxGraph       = 60
	kbDefaultSince   = 30 * 24 * time.Hour
)

type kbArgs struct {
	sub, text, identity string
	query, node         string
	brains              []string
	all, json           bool
	limit, budget       int
	since               time.Time
}

var kbValueFlags = map[string]bool{"--brain": true, "--identity": true, "--limit": true, "--since": true, "--budget": true, "--query": true, "--node": true}

// parseKBArgs reads the arguments after `ki kb <sub>`; flags may come before or after the text.
func parseKBArgs(sub string, args []string, now time.Time) (kbArgs, error) {
	ka := kbArgs{sub: sub}
	var words []string
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case kbValueFlags[a] && i+1 < len(args):
			if err := ka.set(a, args[i+1], now); err != nil {
				return ka, err
			}
			i++
		case kbValueFlags[a]:
			return ka, fail("%s needs a value", a)
		case a == "--json":
			ka.json = true
		case a == "--all":
			ka.all = true
		case strings.HasPrefix(a, "-"):
			return ka, fail("unknown flag %s\n\n%s", a, kbUsage)
		default:
			words = append(words, a)
		}
	}
	ka.text = strings.TrimSpace(strings.Join(words, " "))
	needsText := sub == "search" || sub == "context"
	switch {
	case needsText && ka.text == "":
		return ka, fail("ki kb %s needs a query\n\n%s", sub, kbUsage)
	case !needsText && ka.text != "":
		return ka, fail("ki kb %s takes no query; got %q\n\n%s", sub, ka.text, kbUsage)
	}
	if err := ki.ValidateBrains(ka.brains); err != nil {
		return ka, fail("%v", err)
	}
	if err := ka.checkGraph(); err != nil {
		return ka, err
	}
	return ka, nil
}

// checkGraph holds graph to one brain and one anchor, and the other commands to neither.
func (ka kbArgs) checkGraph() error {
	if ka.sub != "graph" {
		if ka.query != "" || ka.node != "" {
			return fail("--query and --node belong to ki kb graph\n\n%s", kbUsage)
		}
		return nil
	}
	switch {
	case len(ka.brains) != 1 || ka.all:
		return fail("ki kb graph reads exactly one brain: pass --brain B\n\n%s", kbUsage)
	case (ka.query == "") == (ka.node == ""):
		return fail("ki kb graph needs exactly one of --query and --node\n\n%s", kbUsage)
	}
	return nil
}

func (ka *kbArgs) set(flag, v string, now time.Time) error {
	switch flag {
	case "--brain":
		ka.brains = append(ka.brains, v)
	case "--identity":
		ka.identity = v
	case "--query":
		ka.query = v
	case "--node":
		ka.node = v
	case "--limit", "--budget":
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			return fail("%s %q: use a positive number", flag, v)
		}
		if flag == "--limit" {
			ka.limit = n
		} else {
			ka.budget = n
		}
	case "--since":
		t, err := ki.ParseSince(v, now)
		if err != nil {
			return fail("%v", err)
		}
		ka.since = t
	}
	return nil
}

func (a *App) kb(ctx context.Context, args []string) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		fmt.Fprintln(a.Stdout, kbUsage)
		return nil
	}
	sub := args[0]
	if !slices.Contains([]string{"brains", "search", "recent", "activity", "graph", "context"}, sub) {
		return fail("unknown ki kb command %q\n\n%s", sub, kbUsage)
	}
	ka, err := parseKBArgs(sub, args[1:], time.Now())
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, kbTimeout)
	defer cancel()
	c, err := a.kbConnect(ctx, ka)
	if err != nil {
		return err
	}
	defer c.close()
	switch sub {
	case "brains":
		return a.kbBrains(ctx, c, ka)
	case "search":
		return a.kbSearch(ctx, c, ka)
	case "recent":
		return a.kbRecent(ctx, c, ka)
	case "activity":
		return a.kbActivity(ctx, c, ka)
	case "graph":
		return a.kbGraph(ctx, c, ka)
	default:
		return a.kbContext(ctx, c, ka)
	}
}

// kbScope is what a command reads: the brains it names in the X-Ki-Brains
// header (none means the token's default scope) and the token that grants them.
type kbScope struct {
	url, token string
	header     []string
	// all asks for the token's default scope minus voice, which needs the
	// server's list before the real call.
	all bool
}

func (a *App) kbScope(ctx context.Context, ka kbArgs) (*kbScope, *here, error) {
	h, err := a.current(ctx, nil)
	if err != nil {
		return nil, nil, err
	}
	url := a.mcpURL(h)
	if url == "" {
		return nil, nil, noURL(h, "ki kb")
	}
	s := ki.Session{}
	for _, b := range ka.brains {
		s.Sensitive = s.Sensitive || h.cfg.IsSensitive(b)
	}
	sc := &kbScope{url: url}
	switch {
	case ka.sub == "brains" && len(ka.brains) > 0:
		sc.header = appendNew([]string{scope.Cortex}, ka.brains...)
	case ka.sub == "brains":
		// Everything the token reads by default.
	case ka.all:
		sc.all = true
	default:
		m, err := a.machine(h.cfg)
		if err != nil {
			return nil, nil, err
		}
		id, err := askIdentity(h, m, ka.identity)
		if err != nil {
			return nil, nil, err
		}
		sc.header = askBrains(h.cfg, h.session, id.Name, ka.brains)
		for _, b := range sc.header {
			s.Sensitive = s.Sensitive || h.cfg.IsSensitive(b)
		}
	}
	if sc.token, err = readToken(a.tokenFile(s)); err != nil {
		return nil, nil, err
	}
	return sc, h, nil
}

// kbSession is one MCP connection to the knowledge base, with the token
// scrubbed from anything it reports.
type kbSession struct {
	cs    *mcp.ClientSession
	token string
}

func (s *kbSession) close() {
	if s != nil && s.cs != nil {
		_ = s.cs.Close() // best effort: the command already has its answer
	}
}

// kbClient is the connection plus what the commands print around its results.
type kbClient struct {
	*kbSession
	here *here
	// brains are the ones named in the connection's header; none is the token's default scope.
	brains []string
}

func (a *App) kbConnect(ctx context.Context, ka kbArgs) (*kbClient, error) {
	sc, h, err := a.kbScope(ctx, ka)
	if err != nil {
		return nil, err
	}
	c := &kbClient{here: h}
	if sc.all {
		// The default scope's names come from the server, so the call below
		// can leave voice out without the CLI knowing which brains exist.
		list, err := a.kbOpen(ctx, sc, nil)
		if err != nil {
			return nil, err
		}
		var out kbBrainsReply
		err = list.call(ctx, "brains", nil, &out)
		list.close()
		if err != nil {
			return nil, err
		}
		sc.header = []string{scope.Cortex}
		for _, b := range out.Brains {
			if b.Name != ki.VoiceBrain {
				sc.header = appendNew(sc.header, b.Name)
			}
		}
		sc.header = appendNew(sc.header, ka.brains...)
	}
	c.brains = sc.header
	if c.kbSession, err = a.kbOpen(ctx, sc, sc.header); err != nil {
		return nil, err
	}
	return c, nil
}

// kbOpen connects to ki-mcp with the bearer token and the brains header.
func (a *App) kbOpen(ctx context.Context, sc *kbScope, brains []string) (*kbSession, error) {
	hc := &http.Client{Transport: kbHeaders{token: sc.token, brains: brains}}
	client := mcp.NewClient(&mcp.Implementation{Name: "ki", Version: a.Version}, nil)
	cs, err := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint: sc.url + "/mcp", HTTPClient: hc, MaxRetries: -1, DisableStandaloneSSE: true,
	}, nil)
	s := &kbSession{token: sc.token}
	if err != nil {
		return nil, fail("knowledge base at %s: %s", sc.url, s.scrub(err.Error()))
	}
	s.cs = cs
	return s, nil
}

// kbHeaders adds what ki-mcp reads from every request, as a session's MCP config does.
type kbHeaders struct {
	token  string
	brains []string
}

func (h kbHeaders) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+h.token)
	if len(h.brains) > 0 {
		r.Header.Set("X-Ki-Brains", strings.Join(h.brains, ","))
	}
	resp, err := http.DefaultTransport.RoundTrip(r)
	if err != nil {
		return nil, fmt.Errorf("sending the request: %w", err)
	}
	return resp, nil
}

func (s *kbSession) scrub(msg string) string {
	if s.token == "" {
		return msg
	}
	return strings.ReplaceAll(msg, s.token, "***")
}

// call runs one tool and decodes its structured result into out; the text of a
// tool without one is returned. A refusal from the server is the command's error.
func (s *kbSession) call(ctx context.Context, tool string, args map[string]any, out any) error {
	_, err := s.callText(ctx, tool, args, out)
	return err
}

func (s *kbSession) callText(ctx context.Context, tool string, args map[string]any, out any) (string, error) {
	res, err := s.cs.CallTool(ctx, &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		return "", fail("knowledge base %s: %s", tool, s.scrub(err.Error()))
	}
	var text strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			text.WriteString(tc.Text)
		}
	}
	if res.IsError {
		return "", fail("knowledge base %s: %s", tool, s.scrub(strings.TrimSpace(text.String())))
	}
	if out != nil {
		raw, err := json.Marshal(res.StructuredContent)
		if err != nil {
			return "", fmt.Errorf("reading %s result: %w", tool, err)
		}
		if err := json.Unmarshal(raw, out); err != nil {
			return "", fmt.Errorf("reading %s result: %w", tool, err)
		}
	}
	return text.String(), nil
}

// kbBrainsReply, kbSearchReply and kbRecentReply are what ki-mcp's structured results carry.
type kbBrainsReply struct {
	Brains []struct {
		Name       string     `json:"name"`
		Documents  int        `json:"documents"`
		LastIngest *time.Time `json:"last_ingest"`
	} `json:"brains"`
}

type kbSearchReply struct {
	Hits []struct {
		Brain      string  `json:"brain"`
		Kind       string  `json:"kind"`
		Fact       string  `json:"fact"`
		Source     string  `json:"source"`
		Score      float64 `json:"score"`
		Superseded bool    `json:"superseded"`
	} `json:"hits"`
}

type kbRecentReply struct {
	Documents []struct {
		Brain       string     `json:"brain"`
		Kind        string     `json:"kind"`
		Source      string     `json:"source"`
		CommittedAt *time.Time `json:"committed_at"`
	} `json:"documents"`
}

type kbActivityReply struct {
	Brains map[string][]kbActivityDay `json:"brains"`
}

type kbActivityDay struct {
	Day       string `json:"day"`
	Documents int    `json:"documents"`
}

// The JSON the commands print with --json. Fields only ever get added.

type kbBrainOut struct {
	Name       string     `json:"name"`
	Documents  int        `json:"documents"`
	LastIngest *time.Time `json:"last_ingest"`
	Projects   []string   `json:"projects"`
}

type kbHitOut struct {
	Title      string  `json:"title"`
	Source     string  `json:"source"`
	Brain      string  `json:"brain"`
	Kind       string  `json:"kind"`
	Snippet    string  `json:"snippet"`
	Link       string  `json:"link,omitempty"`
	Score      float64 `json:"score"`
	Superseded bool    `json:"superseded"`
}

type kbDocOut struct {
	Title       string     `json:"title"`
	Source      string     `json:"source"`
	Brain       string     `json:"brain"`
	Kind        string     `json:"kind"`
	Link        string     `json:"link,omitempty"`
	CommittedAt *time.Time `json:"committed_at"`
}

// kbGraphEdgeOut keeps both times in the JSON, null when unset, so a current fact has "invalid_at": null.
type kbGraphEdgeOut struct {
	ID        string     `json:"id"`
	From      string     `json:"from"`
	To        string     `json:"to"`
	Name      string     `json:"name"`
	Fact      string     `json:"fact"`
	Kind      kb.Kind    `json:"kind"`
	Source    string     `json:"source,omitempty"`
	ValidAt   *time.Time `json:"valid_at"`
	InvalidAt *time.Time `json:"invalid_at"`
}

func (a *App) kbBrains(ctx context.Context, c *kbClient, ka kbArgs) error {
	var reply kbBrainsReply
	if err := c.call(ctx, "brains", nil, &reply); err != nil {
		return err
	}
	users := map[string][]string{}
	for _, p := range c.here.cfg.Projects {
		for _, b := range p.Brains {
			users[b] = appendNew(users[b], p.Name)
		}
	}
	out := struct {
		Brains []kbBrainOut `json:"brains"`
	}{Brains: []kbBrainOut{}}
	for _, b := range reply.Brains {
		out.Brains = append(out.Brains, kbBrainOut{Name: b.Name, Documents: b.Documents, LastIngest: utc(b.LastIngest), Projects: orEmpty(users[b.Name])})
	}
	sort.Slice(out.Brains, func(i, j int) bool { return out.Brains[i].Name < out.Brains[j].Name })
	if ka.json {
		return a.printJSON(out)
	}
	for _, b := range out.Brains {
		last := "never"
		if b.LastIngest != nil {
			last = b.LastIngest.Format("2006-01-02 15:04Z")
		}
		fmt.Fprintf(a.Stdout, "%-24s %6d documents  last ingest %-17s  %s\n", b.Name, b.Documents, last, strings.Join(b.Projects, ", "))
	}
	return nil
}

func (a *App) kbSearch(ctx context.Context, c *kbClient, ka kbArgs) error {
	limit := ka.limit
	if limit == 0 {
		limit = kbDefaultLimit
	}
	if limit > kbMaxSearchLimit {
		return fail("--limit %d: search returns at most %d", limit, kbMaxSearchLimit)
	}
	var reply kbSearchReply
	if err := c.call(ctx, "search", map[string]any{"query": ka.text, "limit": limit}, &reply); err != nil {
		return err
	}
	out := struct {
		Query  string     `json:"query"`
		Brains []string   `json:"brains"`
		Hits   []kbHitOut `json:"hits"`
	}{Query: ka.text, Brains: orEmpty(c.brains), Hits: []kbHitOut{}}
	for _, h := range reply.Hits {
		out.Hits = append(out.Hits, kbHitOut{
			Title: ki.SourceTitle(h.Source), Source: h.Source, Brain: h.Brain, Kind: h.Kind,
			Snippet: ki.Snippet(h.Fact, kbSnippetRunes), Link: ki.SourceLink(c.here.cfg, h.Source),
			Score: h.Score, Superseded: h.Superseded,
		})
	}
	if ka.json {
		return a.printJSON(out)
	}
	if len(out.Hits) == 0 {
		fmt.Fprintln(a.Stdout, "No results.")
	}
	for _, h := range out.Hits {
		fmt.Fprintf(a.Stdout, "[%s/%s] %s%s\n  %s\n", h.Brain, h.Kind, orDefault(h.Title, "(no source)"), superseded(h.Superseded), h.Snippet)
		if h.Source != "" {
			fmt.Fprintf(a.Stdout, "  %s\n", orDefault(h.Link, h.Source))
		}
	}
	return nil
}

func (a *App) kbRecent(ctx context.Context, c *kbClient, ka kbArgs) error {
	limit := ka.limit
	if limit == 0 {
		limit = kbDefaultRecent
	}
	if limit > kbMaxRecent {
		return fail("--limit %d: recent returns at most %d", limit, kbMaxRecent)
	}
	args := map[string]any{"limit": limit}
	if !ka.since.IsZero() {
		args["since"] = ka.since.UTC().Format(time.RFC3339)
	}
	var reply kbRecentReply
	if err := c.call(ctx, "recent", args, &reply); err != nil {
		return err
	}
	out := struct {
		Since     *time.Time `json:"since"`
		Brains    []string   `json:"brains"`
		Documents []kbDocOut `json:"documents"`
	}{Brains: orEmpty(c.brains), Documents: []kbDocOut{}}
	if !ka.since.IsZero() {
		since := ka.since.UTC()
		out.Since = &since
	}
	for _, d := range reply.Documents {
		out.Documents = append(out.Documents, kbDocOut{
			Title: ki.SourceTitle(d.Source), Source: d.Source, Brain: d.Brain, Kind: d.Kind,
			Link: ki.SourceLink(c.here.cfg, d.Source), CommittedAt: utc(d.CommittedAt),
		})
	}
	if ka.json {
		return a.printJSON(out)
	}
	if len(out.Documents) == 0 {
		fmt.Fprintln(a.Stdout, "No documents.")
	}
	for _, d := range out.Documents {
		at := "unknown date"
		if d.CommittedAt != nil {
			at = d.CommittedAt.Format("2006-01-02")
		}
		fmt.Fprintf(a.Stdout, "%s  [%s/%s] %s\n", at, d.Brain, d.Kind, d.Source)
	}
	return nil
}

func (a *App) kbActivity(ctx context.Context, c *kbClient, ka kbArgs) error {
	now := time.Now().UTC()
	since := ka.since
	if since.IsZero() {
		since = now.Add(-kbDefaultSince)
	}
	since = since.UTC()
	var reply kbActivityReply
	if err := c.call(ctx, "activity", map[string]any{"since": since.Format(time.RFC3339)}, &reply); err != nil {
		return err
	}
	out := struct {
		Since    time.Time                  `json:"since"`
		Brains   []string                   `json:"brains"`
		Activity map[string][]kbActivityDay `json:"activity"`
	}{Since: since, Brains: orEmpty(c.brains), Activity: map[string][]kbActivityDay{}}
	names := make([]string, 0, len(reply.Brains))
	for name, days := range reply.Brains {
		out.Activity[name] = days
		names = append(names, name)
	}
	sort.Strings(names)
	if ka.json {
		return a.printJSON(out)
	}
	if len(names) == 0 {
		fmt.Fprintln(a.Stdout, "No brains.")
	}
	for _, name := range names {
		days := out.Activity[name]
		total := 0
		for _, d := range days {
			total += d.Documents
		}
		fmt.Fprintf(a.Stdout, "%-24s %6d documents  %s\n", name, total, sparkline(days, since, now))
	}
	return nil
}

// sparkline draws one block per UTC day from since to now, scaled to the busiest day; days without documents are
// blank.
func sparkline(days []kbActivityDay, since, now time.Time) string {
	counts := make(map[string]int, len(days))
	peak := 0
	for _, d := range days {
		counts[d.Day] = d.Documents
		peak = max(peak, d.Documents)
	}
	blocks := []rune("▁▂▃▄▅▆▇█")
	var b strings.Builder
	for day := since.Truncate(24 * time.Hour); !day.After(now); day = day.AddDate(0, 0, 1) {
		n := counts[day.Format(time.DateOnly)]
		if n == 0 {
			b.WriteRune(' ')
			continue
		}
		b.WriteRune(blocks[(n-1)*len(blocks)/peak])
	}
	return b.String()
}

func (a *App) kbGraph(ctx context.Context, c *kbClient, ka kbArgs) error {
	limit := ka.limit
	if limit == 0 {
		limit = kbDefaultGraph
	}
	if limit > kbMaxGraph {
		return fail("--limit %d: graph returns at most %d edges", limit, kbMaxGraph)
	}
	args := map[string]any{"brain": ka.brains[0], "limit": limit}
	if ka.query != "" {
		args["query"] = ka.query
	} else {
		args["node"] = ka.node
	}
	var reply kb.Graph
	if err := c.call(ctx, "graph", args, &reply); err != nil {
		return err
	}
	out := struct {
		Brain string           `json:"brain"`
		Nodes []kb.GraphNode   `json:"nodes"`
		Edges []kbGraphEdgeOut `json:"edges"`
	}{Brain: ka.brains[0], Nodes: reply.Nodes, Edges: make([]kbGraphEdgeOut, 0, len(reply.Edges))}
	if out.Nodes == nil {
		out.Nodes = []kb.GraphNode{}
	}
	for _, e := range reply.Edges {
		out.Edges = append(out.Edges, kbGraphEdgeOut{
			ID: e.ID, From: e.From, To: e.To, Name: e.Name, Fact: e.Fact, Kind: e.Kind,
			Source: e.Source, ValidAt: utc(e.ValidAt), InvalidAt: utc(e.InvalidAt),
		})
	}
	if ka.json {
		return a.printJSON(out)
	}
	if len(out.Edges) == 0 {
		fmt.Fprintln(a.Stdout, "No facts.")
	}
	names := make(map[string]string, len(out.Nodes))
	for _, n := range out.Nodes {
		names[n.ID] = n.Name
	}
	for _, e := range out.Edges {
		source := e.Source
		if source == "" {
			source = string(e.Kind)
		}
		fmt.Fprintf(a.Stdout, "%s -[%s]-> %s: %s (%s)\n", names[e.From], e.Name, names[e.To], e.Fact, source)
	}
	return nil
}

func (a *App) kbContext(ctx context.Context, c *kbClient, ka kbArgs) error {
	args := map[string]any{"task": ka.text}
	if ka.budget > 0 {
		args["budget"] = ka.budget
	}
	pack, err := c.callText(ctx, "get_context", args, nil)
	if err != nil {
		return err
	}
	if ka.json {
		return a.printJSON(struct {
			Query   string   `json:"query"`
			Brains  []string `json:"brains"`
			Context string   `json:"context"`
		}{ka.text, orEmpty(c.brains), pack})
	}
	fmt.Fprintln(a.Stdout, strings.TrimRight(pack, "\n"))
	return nil
}

func (a *App) printJSON(v any) error {
	if err := json.NewEncoder(a.Stdout).Encode(v); err != nil {
		return fmt.Errorf("printing the result: %w", err)
	}
	return nil
}

func utc(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}

func orEmpty(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func superseded(old bool) string {
	if old {
		return " (superseded)"
	}
	return ""
}
