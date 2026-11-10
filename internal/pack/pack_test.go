package pack

import (
	"strings"
	"testing"
	"time"

	"github.com/kislow/kio/internal/kb"
)

func TestBuildStructureAndSupersession(t *testing.T) {
	old := time.Now()
	facts := []kb.Fact{
		{Brain: "acme", Kind: kb.KindDecision, Fact: "Use chi for routing", Score: 0.9},
		{Brain: "acme", Kind: kb.KindDecision, Fact: "Use gorilla/mux", Score: 0.95, InvalidAt: &old},
		{Brain: "cortex", Kind: kb.KindConvention, Fact: "Errors wrap with %w", Score: 0.8},
		{Brain: "cortex", Kind: kb.KindConvention, Fact: "Errors wrap with %w", Score: 0.7},
	}
	out := Build("add a handler", []string{"acme", "cortex"}, facts, 3000)

	want := []string{"## Conventions", "## Active decisions", "## Current state", "## Pointers"}
	last := -1
	for _, h := range want {
		i := strings.Index(out, h)
		if i <= last {
			t.Fatalf("heading %q missing or out of order:\n%s", h, out)
		}
		last = i
	}
	if strings.Contains(out, "gorilla") {
		t.Fatal("superseded decision must not appear")
	}
	if strings.Count(out, "Errors wrap") != 1 {
		t.Fatal("duplicate facts must be collapsed")
	}
}

func TestBuildRespectsBudget(t *testing.T) {
	var facts []kb.Fact
	for i := 0; i < 500; i++ {
		facts = append(facts, kb.Fact{Kind: kb.KindPointer, Fact: strings.Repeat("x", 80) + string(rune('a'+i%26)), Score: 1})
	}
	facts = append(facts, kb.Fact{Kind: kb.KindDecision, Fact: "the one decision", Score: 0.1})
	out := Build("t", []string{"cortex"}, facts, 400)
	if EstimateTokens(out) > 400 {
		t.Fatalf("pack exceeds budget: %d", EstimateTokens(out))
	}
	if !strings.Contains(out, "the one decision") {
		t.Fatal("decisions must not be starved by other kinds")
	}
}
