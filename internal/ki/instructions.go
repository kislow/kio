package ki

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"strings"
)

// InstructionsFile is the agent instructions file, under agent/ in the harness
// checkout and, optionally, in a configuration directory.
var InstructionsFile = filepath.Join("agent", "instructions.md")

// KnowledgeBaseFile is the part of the agent instructions about the knowledge
// base, under agent/ in the harness checkout. It is included only when a
// knowledge base is configured.
var KnowledgeBaseFile = filepath.Join("agent", "knowledge-base.md")

// CombineInstructions is the harness instructions followed by the configuration
// directory's, a blank line between. With no configuration text it returns the
// harness text untouched, so a machine without that file sees no change.
func CombineInstructions(harness, config string) string {
	config = strings.TrimSpace(config)
	switch {
	case config == "":
		return harness
	case strings.TrimSpace(harness) == "":
		return config + "\n"
	}
	return strings.TrimRight(harness, "\n") + "\n\n" + config + "\n"
}

// InstructionsRunFile names the run-directory file holding text. The name
// carries a hash of the text, so sessions with the same text share one file and
// a session never reads a file another one is rewriting.
func InstructionsRunFile(runDir, text string) string {
	sum := sha256.Sum256([]byte(text))
	return filepath.Join(runDir, "instructions-"+hex.EncodeToString(sum[:8])+".md")
}

// PromptInstructions is the task with the instructions in front, for agents that
// take no system-prompt file in headless mode.
func PromptInstructions(instructions, task string) string {
	if strings.TrimSpace(instructions) == "" {
		return task
	}
	return strings.TrimSpace(instructions) + "\n\n---\n\n" + task
}
