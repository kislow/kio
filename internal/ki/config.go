package ki

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/goccy/go-yaml"
)

// A configuration directory holds everything that is yours rather than the
// harness's: the projects file, the model settings, the aliases, the brains
// map the MCP server serves, and config.yaml for settings that are not
// projects. The harness checkout carries one under deploy/config as the
// example and as the fallback for machines that never linked their own.
const (
	ConfigFile    = "config.yaml"
	ConfigLink    = "config" // under the ki config directory, like "harness"
	ProjectsFile  = "projects.yaml"
	ModelsFile    = "models.env"
	AliasesFile   = "aliases.yaml"
	MCPConfigFile = "mcp-config.json"
)

// ConfigFiles are the files a configuration directory must hold.
var ConfigFiles = []string{ProjectsFile, ModelsFile, AliasesFile, MCPConfigFile, ConfigFile}

// Where a configuration directory came from.
const (
	ConfigFromEnv     = "env"     // KI_CONFIG
	ConfigFromLink    = "link"    // ~/.config/ki/config, written by ki setup config
	ConfigFromHarness = "harness" // <harness>/deploy/config, the layout every machine started with
)

// Settings is config.yaml: the settings that are not projects.
type Settings struct {
	KnowledgeBase struct {
		URL string `yaml:"url"`
	} `yaml:"knowledge_base"`
}

type rawSettings struct {
	KnowledgeBase *struct {
		URL string `yaml:"url"`
	} `yaml:"knowledge_base"`
}

// LoadSettings reads config.yaml. A missing file is an empty Settings, so
// the fallback layout keeps working without one; unknown keys are errors.
func LoadSettings(path string) (Settings, error) {
	var s Settings
	data, err := os.ReadFile(path) // #nosec G304 -- a file of the user's configuration directory
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return s, fmt.Errorf("reading %s: %w", path, err)
	}
	var raw rawSettings
	if err := yaml.UnmarshalWithOptions(data, &raw, yaml.Strict()); err != nil {
		return s, fmt.Errorf("%s: %w", path, err)
	}
	if raw.KnowledgeBase != nil {
		u := strings.TrimSpace(raw.KnowledgeBase.URL)
		if u != "" && !strings.HasPrefix(u, "https://") && !strings.HasPrefix(u, "http://") {
			return s, fmt.Errorf("%s: knowledge_base.url must start with https:// or http://, got %q", path, u)
		}
		s.KnowledgeBase.URL = strings.TrimRight(u, "/")
	}
	return s, nil
}

// MissingConfigFiles lists the required files absent from dir.
func MissingConfigFiles(dir string) []string {
	var missing []string
	for _, f := range ConfigFiles {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			missing = append(missing, f)
		}
	}
	return missing
}
