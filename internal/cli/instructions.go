package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/kislow/kio/internal/ki"
)

// instructionsText is what every agent gets: the harness's instructions (the
// checkout's, else the embedded ones), the
// knowledge-base part when a knowledge base is configured, then the
// configuration directory's when it has an agent/instructions.md.
func (h *here) instructionsText() (string, error) {
	text, err := readData(h.data, ki.InstructionsFile)
	if err != nil {
		return "", err
	}
	if h.url != "" {
		kb, err := readData(h.data, ki.KnowledgeBaseFile)
		if err != nil {
			return "", err
		}
		text = ki.CombineInstructions(text, kb)
	}
	config, err := readOptional(filepath.Join(h.config, ki.InstructionsFile))
	if err != nil {
		return "", err
	}
	return ki.CombineInstructions(text, config), nil
}

// configInstructions is the configuration directory's instructions file, "" when it has none.
func (h *here) configInstructions() string {
	path := filepath.Join(h.config, ki.InstructionsFile)
	if _, err := os.Stat(path); err != nil {
		return ""
	}
	return path
}

// useInstructions sets what the launch passes as instructions: the text for
// agents that take it in the prompt, and the file Claude Code appends to its
// system prompt. When the text is the checkout's file as it is, that file is
// passed; with the knowledge-base part or a configuration file added, or from
// the embedded copy, it is a private run file with the combined text, since
// the flag takes only one file.
func (a *App) useInstructions(p *prepared) error {
	text, err := p.here.instructionsText()
	if err != nil {
		return err
	}
	p.instructionsText = text
	own, err := readData(p.here.data, ki.InstructionsFile)
	if err != nil {
		return err
	}
	if harness := p.here.harness; harness != "" && text == own {
		p.instructions = filepath.Join(harness, ki.InstructionsFile)
		return nil
	}
	a.pruneRunFiles()
	p.instructions = ki.InstructionsRunFile(filepath.Join(a.confDir(), "run"), text)
	if err := ki.WritePrivateBytes(p.instructions, []byte(text)); err != nil {
		return fmt.Errorf("writing the instructions file: %w", err)
	}
	return nil
}

func (a *App) pruneRunFiles() {
	if _, err := ki.PruneRunFiles(filepath.Join(a.confDir(), "run"), runFileMaxAge, time.Now()); err != nil {
		fmt.Fprintln(a.Stderr, "ki: warning:", err)
	}
}
