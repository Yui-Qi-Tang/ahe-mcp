package evidencegraph

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
)

// Extension carries optional domain or experiment data without adding fields
// to EvidenceNode, EvidenceEdge, or the policy-visible Graph contract.
type Extension struct {
	Namespace string          `json:"namespace"`
	Version   string          `json:"version"`
	Data      json.RawMessage `json:"data"`
}

// Artifact is the persisted evidence graph plus optional, policy-invisible
// extensions. Graph queries operate only on Graph.
type Artifact struct {
	Graph
	Extensions []Extension `json:"extensions,omitempty"`
}

// Validate checks the base graph and extension envelopes independently.
func (a Artifact) Validate() error {
	if err := a.Graph.Validate(); err != nil {
		return err
	}
	seen := map[string]bool{}
	for i, extension := range a.Extensions {
		if err := extension.Validate(); err != nil {
			return fmt.Errorf("extension %d: %w", i, err)
		}
		key := extension.Namespace + "@" + extension.Version
		if seen[key] {
			return fmt.Errorf("duplicate extension %q", key)
		}
		seen[key] = true
	}
	return nil
}

// Validate checks that an extension is namespaced, versioned, and valid JSON.
func (e Extension) Validate() error {
	if strings.TrimSpace(e.Namespace) == "" {
		return fmt.Errorf("namespace is required")
	}
	if strings.TrimSpace(e.Version) == "" {
		return fmt.Errorf("version is required")
	}
	if len(e.Data) == 0 || !json.Valid(e.Data) {
		return fmt.Errorf("data must be valid JSON")
	}
	return nil
}

// LoadArtifact reads and strictly validates one evidence graph artifact.
func LoadArtifact(path string) (Artifact, error) {
	file, err := os.Open(path)
	if err != nil {
		return Artifact{}, fmt.Errorf("opening evidence graph: %w", err)
	}
	defer file.Close()

	artifact, err := DecodeArtifact(file)
	if err != nil {
		return Artifact{}, fmt.Errorf("parsing evidence graph: %w", err)
	}
	return artifact, nil
}

// DecodeArtifact rejects undeclared base fields while allowing extension data
// only inside the explicit extension envelope.
func DecodeArtifact(r io.Reader) (Artifact, error) {
	var artifact Artifact
	decoder := json.NewDecoder(r)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&artifact); err != nil {
		return Artifact{}, err
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err != nil {
			return Artifact{}, err
		}
		return Artifact{}, fmt.Errorf("trailing JSON values")
	}
	if err := artifact.Validate(); err != nil {
		return Artifact{}, err
	}
	return artifact, nil
}
