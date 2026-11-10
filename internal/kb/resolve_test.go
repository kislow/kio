package kb

import "testing"

func decision(brain, fact, source string) Fact {
	return Fact{Brain: brain, Kind: KindDecision, Fact: fact, Source: source}
}

func TestResolveDropsRepeatedText(t *testing.T) {
	const dup = "models.execution and effort.execution now apply through ki delegate only."
	facts := []Fact{
		decision("cortex", dup, "decisions/2026-10-25-ki-delegate.md"),
		decision("cortex", dup, "decisions/2026-10-25-ki-delegate.md"),
		decision("cortex", " "+dup+" ", "decisions/2026-10-26-models.md"),
		decision("cortex", "--mode edits without an allow-list is refused.", "decisions/2026-10-26-ki-edits-mode.md"),
	}
	got := Resolve(facts)
	if len(got) != 2 {
		t.Fatalf("want 2 facts, got %d: %+v", len(got), got)
	}
	if got[0].Source != facts[0].Source || got[1].Fact != facts[3].Fact {
		t.Fatalf("order or first occurrence not kept: %+v", got)
	}
}

func TestResolveKeepsSimilarFactsOnTheSameTopic(t *testing.T) {
	facts := []Fact{
		decision("cortex", "ki delegate ls read the ledger, so it listed finished jobs only.", "decisions/2026-10-25-ki-delegate.md"),
		decision("cortex", "ki delegate ls lists RUNNING jobs from result.json first.", "decisions/2026-10-26-delegate-streaming.md"),
	}
	got := Resolve(facts)
	if len(got) != 2 {
		t.Fatalf("want 2 facts, got %d", len(got))
	}
	for _, f := range got {
		if !f.Current() {
			t.Fatalf("fact wrongly superseded: %+v", f)
		}
	}
}
