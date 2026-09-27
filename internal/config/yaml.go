package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"go.yaml.in/yaml/v3"
)

const (
	maxYAMLConfigBytes = 1 << 20
	maxYAMLDepth       = 32
)

// DecodeYAML parses one bounded YAML document, rejects YAML features that can
// obscure the effective configuration, converts the result to JSON, and then
// reuses DecodeJSON so one typed validation path remains authoritative.
func DecodeYAML(r io.Reader) (Config, error) {
	b, err := io.ReadAll(io.LimitReader(r, maxYAMLConfigBytes+1))
	if err != nil {
		return Config{}, err
	}
	if len(b) > maxYAMLConfigBytes {
		return Config{}, errors.New("config exceeds 1 MiB limit")
	}

	dec := yaml.NewDecoder(bytes.NewReader(b))
	var doc yaml.Node
	if err := dec.Decode(&doc); err != nil {
		return Config{}, err
	}
	if len(doc.Content) != 1 {
		return Config{}, errors.New("config must contain one YAML document")
	}
	if err := validateYAMLNode(&doc, 0); err != nil {
		return Config{}, err
	}
	var extra yaml.Node
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return Config{}, errors.New("multiple YAML documents are not allowed")
		}
		return Config{}, err
	}

	var raw map[string]any
	if err := doc.Content[0].Decode(&raw); err != nil {
		return Config{}, err
	}
	j, err := json.Marshal(raw)
	if err != nil {
		return Config{}, err
	}
	return DecodeJSON(bytes.NewReader(j))
}

func validateYAMLNode(n *yaml.Node, depth int) error {
	if depth > maxYAMLDepth {
		return errors.New("YAML nesting exceeds limit")
	}
	if n.Anchor != "" || n.Kind == yaml.AliasNode {
		return errors.New("YAML anchors and aliases are not allowed")
	}
	if strings.HasPrefix(n.Tag, "!") && !strings.HasPrefix(n.Tag, "!!") {
		return errors.New("custom YAML tags are not allowed")
	}
	switch n.Kind {
	case yaml.DocumentNode, yaml.SequenceNode:
		for _, child := range n.Content {
			if err := validateYAMLNode(child, depth+1); err != nil {
				return err
			}
		}
	case yaml.MappingNode:
		if len(n.Content)%2 != 0 {
			return errors.New("invalid YAML mapping")
		}
		seen := make(map[string]struct{}, len(n.Content)/2)
		for i := 0; i < len(n.Content); i += 2 {
			key, value := n.Content[i], n.Content[i+1]
			if key.Kind != yaml.ScalarNode || key.Value == "" {
				return errors.New("YAML mapping keys must be non-empty scalars")
			}
			if key.Value == "<<" {
				return errors.New("YAML merge keys are not allowed")
			}
			if _, ok := seen[key.Value]; ok {
				return fmt.Errorf("duplicate YAML key: %s", key.Value)
			}
			seen[key.Value] = struct{}{}
			if err := validateYAMLNode(key, depth+1); err != nil {
				return err
			}
			if err := validateYAMLNode(value, depth+1); err != nil {
				return err
			}
		}
	}
	return nil
}
