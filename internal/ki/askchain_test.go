package ki

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func writeResult(t *testing.T, dir string, r Result) {
	t.Helper()
	if err := WritePrivate(filepath.Join(dir, r.ID, "result.json"), r); err != nil {
		t.Fatal(err)
	}
}

func askJob(id, continues, question, answer string) Result {
	return Result{ID: id, Kind: KindAsk, Status: JobDone, Continues: continues, Last: answer, Ask: &AskState{Question: question}}
}

func TestAskChainIsOldestFirstAndStopsAtAGoneJob(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	a, b, c := "j-20261107-000001-aaaa", "j-20261107-000002-aaaa", "j-20261107-000003-aaaa"
	writeResult(t, dir, askJob(a, "", "q1", "a1"))
	writeResult(t, dir, askJob(b, a, "q2", "a2\n"))
	cur := askJob(c, b, "q3", "a3")

	want := []AskTurn{{"q1", "a1"}, {"q2", "a2"}, {"q3", "a3"}}
	if got := AskChain(dir, cur); !slices.Equal(got, want) {
		t.Fatalf("chain %v, want %v", got, want)
	}

	if err := os.RemoveAll(filepath.Join(dir, a)); err != nil {
		t.Fatal(err)
	}
	if got := AskChain(dir, cur); !slices.Equal(got, want[1:]) {
		t.Fatalf("with the root pruned: %v", got)
	}
}

func TestAskChainSurvivesALoopAndAJobWithoutAskRecord(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	a, b := "j-20261107-000001-aaaa", "j-20261107-000002-aaaa"
	writeResult(t, dir, askJob(a, b, "q1", "a1"))
	writeResult(t, dir, askJob(b, a, "q2", "a2"))
	if got := AskChain(dir, askJob(b, a, "q2", "a2")); len(got) != 2 {
		t.Fatalf("loop: %v", got)
	}
	writeResult(t, dir, Result{ID: a, Status: JobDone})
	if got := AskChain(dir, askJob(b, a, "q2", "a2")); len(got) != 1 {
		t.Fatalf("no ask record: %v", got)
	}
	if got := AskChain(dir, askJob(b, "../../etc", "q2", "a2")); len(got) != 1 {
		t.Fatalf("bad id followed: %v", got)
	}
}

func TestAskHistoryDropsTheOldestTurnsFirst(t *testing.T) {
	t.Parallel()
	turns := []AskTurn{{"q1", strings.Repeat("a", 100)}, {"q2", strings.Repeat("b", 100)}, {"q3", strings.Repeat("c", 100)}}
	all := AskHistory(turns, 10000)
	if strings.Index(all, "q1") > strings.Index(all, "q2") || strings.Index(all, "q2") > strings.Index(all, "q3") {
		t.Fatalf("not oldest first: %q", all)
	}

	// Room for two turns, not three.
	two := AskHistory(turns, len([]rune(AskHistory(turns[1:], 10000)))+1)
	if strings.Contains(two, "q1") || !strings.Contains(two, "q2") || !strings.Contains(two, "q3") {
		t.Fatalf("kept the wrong turns: %q", two)
	}

	one := AskHistory(turns, 150)
	if strings.Contains(one, "q1") || strings.Contains(one, "q2") || !strings.Contains(one, "q3") || len([]rune(one)) > 150 {
		t.Fatalf("a single long turn: %d chars %q", len([]rune(one)), one)
	}
}

func TestAskHistoryCapsAtTwentyThousandCharacters(t *testing.T) {
	t.Parallel()
	var turns []AskTurn
	for i := 0; i < 30; i++ {
		turns = append(turns, AskTurn{Question: "question " + strings.Repeat("é", 10), Answer: strings.Repeat("ü", 2000)})
	}
	if n := len([]rune(AskHistory(turns, AskHistoryMax))); n > AskHistoryMax || n < AskHistoryMax-3000 {
		t.Fatalf("%d characters", n)
	}
	if n := len([]rune(AskHistory(turns[:1], AskHistoryMax))); n > AskHistoryMax {
		t.Fatalf("%d characters", n)
	}
}

func TestAskBriefAfterStartsWithTheEarlierTurns(t *testing.T) {
	t.Parallel()
	withHistory := AskBriefAfter("and prod?", []string{SourceKB}, "Question: q1\n\nAnswer:\na1")
	if !strings.HasPrefix(withHistory, "This continues an earlier conversation") ||
		strings.Index(withHistory, "q1") > strings.Index(withHistory, "and prod?") || !strings.Contains(withHistory, "Sources:") {
		t.Fatalf("brief %q", withHistory)
	}
	if got := AskBriefAfter("q", []string{SourceKB}, ""); got != AskBrief("q", []string{SourceKB}) {
		t.Fatal("no history must be the plain brief")
	}
}

func TestBuildLaunchResumesTheSession(t *testing.T) {
	t.Parallel()
	h := &Headless{Task: "task", LastMessage: "/last"}
	claude := BuildLaunch(LaunchInput{Agent: "claude", MCPConfigPath: "/m.json", Resume: "s1", Headless: h}).Argv
	if at := slices.Index(claude, "--resume"); at < 0 || claude[at+1] != "s1" {
		t.Errorf("claude %v", claude)
	}
	cursor := BuildLaunch(LaunchInput{Agent: "agent", Resume: "s1", Headless: h}).Argv
	if at := slices.Index(cursor, "--resume"); at < 0 || cursor[at+1] != "s1" {
		t.Errorf("cursor %v", cursor)
	}
	codex := BuildLaunch(LaunchInput{Agent: "codex", Model: "m", ModeArgs: []string{"--sandbox", "read-only"}, Resume: "s1", Headless: h}).Argv
	if !slices.Equal(codex[:4], []string{"codex", "exec", "resume", "s1"}) || codex[len(codex)-1] != "task" {
		t.Errorf("codex %v", codex)
	}
	if slices.Contains(codex, "--sandbox") || !slices.Contains(codex, `sandbox_mode="read-only"`) {
		t.Errorf("resume must set the sandbox with -c, not --sandbox: %v", codex)
	}
	for _, agent := range []string{"claude", "codex", "agent"} {
		if argv := BuildLaunch(LaunchInput{Agent: agent, MCPConfigPath: "/m.json", Headless: h}).Argv; slices.Contains(argv, "--resume") || slices.Contains(argv, "resume") {
			t.Errorf("%s resumed without a session: %v", agent, argv)
		}
	}
}

func TestCodexResumeFlags(t *testing.T) {
	t.Parallel()
	got := codexResumeFlags([]string{"--sandbox", "read-only", "--skip-git-repo-check", "-c", "a=b", "-s", "workspace-write"})
	want := []string{"-c", `sandbox_mode="read-only"`, "--skip-git-repo-check", "-c", "a=b", "-c", `sandbox_mode="workspace-write"`}
	if !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestAppendLedgerLeavesTheAskRecordInTheResult(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), LedgerFile)
	r := askJob("j-20261107-000001-aaaa", "j-20261107-000000-aaaa", "secret question", "answer")
	r.Resumed = ResumedSession
	if err := AppendLedger(path, r); err != nil {
		t.Fatal(err)
	}
	got, err := ReadLedger(path)
	if err != nil || len(got) != 1 || got[0].Ask != nil || got[0].Last != "" || got[0].Continues != r.Continues || got[0].Resumed != ResumedSession {
		t.Fatalf("ledger %+v %v", got, err)
	}
}
