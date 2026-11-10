package ki

import (
	"fmt"
	"strings"

	"github.com/goccy/go-yaml"
	"github.com/goccy/go-yaml/ast"
	"github.com/goccy/go-yaml/parser"
)

// machineSection is a top-level block of the machine file owned by a feature
// that lives in its own files and registers itself from init(): without those
// files the key is an unknown field like any other.
type machineSection struct {
	key string
	// parse validates the block; its result is kept in Machine.Sections.
	parse func(path string, node ast.Node) (any, error)
	// render writes the block the way parse reads it.
	render func(b *strings.Builder, v any)
}

var machineSections []machineSection

func registerMachineSection(s machineSection) { machineSections = append(machineSections, s) }

func findMachineSection(key string) (machineSection, bool) {
	for _, s := range machineSections {
		if s.key == key {
			return s, true
		}
	}
	return machineSection{}, false
}

// splitSections decodes the machine file strictly, leaving the registered
// sections out of the strict decode and returning their nodes by key.
func splitSections(path string, data []byte, raw *rawMachine) (map[string]ast.Node, error) {
	file, err := parser.ParseBytes(data, 0)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	var body *ast.MappingNode
	if len(file.Docs) > 0 {
		body, _ = file.Docs[0].Body.(*ast.MappingNode)
	}
	if body == nil {
		if err := yaml.UnmarshalWithOptions(data, raw, yaml.Strict()); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		return nil, nil
	}
	sections := map[string]ast.Node{}
	keep := body.Values[:0:0]
	for _, v := range body.Values {
		if _, ok := findMachineSection(v.Key.String()); !ok {
			keep = append(keep, v)
			continue
		}
		if _, null := v.Value.(*ast.NullNode); !null {
			sections[v.Key.String()] = v.Value
		}
	}
	body.Values = keep
	if err := yaml.NodeToValue(body, raw, yaml.Strict()); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return sections, nil
}

func parseSections(path string, nodes map[string]ast.Node) (map[string]any, error) {
	var out map[string]any
	for _, s := range machineSections {
		node, ok := nodes[s.key]
		if !ok {
			continue
		}
		v, err := s.parse(path, node)
		if err != nil {
			return nil, err
		}
		if out == nil {
			out = map[string]any{}
		}
		out[s.key] = v
	}
	return out, nil
}

func renderSections(b *strings.Builder, values map[string]any) {
	for _, s := range machineSections {
		if v, ok := values[s.key]; ok {
			s.render(b, v)
		}
	}
}
