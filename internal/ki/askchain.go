package ki

import (
	"strings"
)

// How a follow-up reached the earlier conversation.
const (
	ResumedSession    = "session"
	ResumedTranscript = "transcript"
)

// AskHistoryMax caps the earlier turns a transcript carries, in characters.
const AskHistoryMax = 20000

// maxChain bounds the walk back through a conversation.
const maxChain = 1000

// AskState is what an ask job records so a follow-up keeps its scope: the
// question for the transcript, the identity, brains and sources it ran with,
// and the scratch folder the agent ran in, which an agent's session is
// filed under.
type AskState struct {
	Question string   `json:"question"`
	Identity string   `json:"identity"`
	Brains   []string `json:"brains"`
	Sources  []string `json:"sources"`
	Scratch  string   `json:"scratch"`
}

// AskTurn is one question of a conversation and the answer it got.
type AskTurn struct {
	Question, Answer string
}

// AskChain is the turns of the conversation prev ends, oldest first. It
// follows Continues links through the jobs folder and stops at the first job
// that is gone or has no ask record, so a pruned root only shortens it.
func AskChain(jobsDir string, prev Result) []AskTurn {
	var turns []AskTurn
	seen := map[string]bool{}
	for cur, n := prev, 0; n < maxChain && cur.Ask != nil && !seen[cur.ID]; n++ {
		seen[cur.ID] = true
		turns = append([]AskTurn{{Question: cur.Ask.Question, Answer: strings.TrimSpace(cur.Last)}}, turns...)
		if !ValidJobID(cur.Continues) {
			break
		}
		next, err := ReadResult(jobsDir, cur.Continues)
		if err != nil {
			break
		}
		cur = next
	}
	return turns
}

// AskHistory renders turns for a prompt, newest last, within max characters:
// the oldest turns are dropped first, and a single turn still too long is cut.
func AskHistory(turns []AskTurn, max int) string {
	parts := make([]string, len(turns))
	for i, t := range turns {
		parts[i] = "Question: " + t.Question + "\n\nAnswer:\n" + t.Answer
	}
	const sep = "\n\n---\n\n"
	from := 0
	for from < len(parts)-1 && len([]rune(strings.Join(parts[from:], sep))) > max {
		from++
	}
	out := []rune(strings.Join(parts[from:], sep))
	if len(out) > max {
		out = out[:max]
	}
	return string(out)
}
