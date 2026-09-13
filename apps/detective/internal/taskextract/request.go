// Package taskextract selects task-scoped, verbatim candidates from frozen
// source text. It has no source, filesystem, MCP, database or review authority.
package taskextract

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	// Version identifies the request, result and selection semantics.
	Version = "detective-task-extraction/v2"
	// ProjectionVersion identifies exact blank-line paragraph boundaries.
	ProjectionVersion = "detective-task-paragraphs/v1"
	// ModelInputVersion identifies the selector-only input projection, separate
	// from paragraph boundaries and the complete controller-owned request.
	ModelInputVersion = "detective-task-model-input/v1"
	// PromptVersion identifies the tool-free task selection instruction.
	PromptVersion = "detective-task-selector/v3"
	// MaxSourceBytes bounds all supplied part bodies, including unread parts.
	MaxSourceBytes = 64 << 10
	// MaxProvidedBytes bounds source text actually offered to the model.
	MaxProvidedBytes = 32 << 10
	// MaxParts bounds supplied and requested source parts independently.
	MaxParts = 16
	// MaxUnits bounds the entire offered paragraph catalog, without truncation.
	MaxUnits = 128
	// MaxCandidates bounds a complete selection, never a silent first-N subset.
	MaxCandidates = 16
)

// Task fixes one user-selected object, reading purpose and source-part scope.
// ID and Revision are caller labels, not sufficient execution/replay identity.
type Task struct {
	ID        string   `json:"id"`
	Revision  int      `json:"revision"`
	Objective string   `json:"objective"`
	SourceID  string   `json:"source_id"`
	Parts     []string `json:"parts"`
}

// SourcePart preserves the exact decoded UTF-8 text of one collected field.
type SourcePart struct {
	Name string `json:"name"`
	Text string `json:"text"`
}

// Source contains caller-declared provenance, not provider-authenticated intake.
// An unknown revision remains unknown; this package cannot qualify it for AHE.
type Source struct {
	ID          string       `json:"id"`
	Revision    string       `json:"revision"`
	Title       string       `json:"title"`
	Location    string       `json:"location"`
	Coverage    string       `json:"coverage"`
	Limitations []string     `json:"limitations"`
	Parts       []SourcePart `json:"parts"`
}

// Unit is a paragraph occurrence, with half-open offsets in its part's text.
// IDs locate occurrences, not facts; identical text at two positions stays distinct.
type Unit struct {
	ID        string `json:"id"`
	Part      string `json:"part"`
	StartByte int    `json:"start_byte"`
	EndByte   int    `json:"end_byte"`
	Text      string `json:"text"`
}

// Scope records supply and selection, never proof that a model read every fact.
type Scope struct {
	RequestedParts           []string `json:"requested_parts"`
	ProvidedParts            []string `json:"provided_parts"`
	NotCollectedParts        []string `json:"not_collected_parts"`
	NotProvidedParts         []string `json:"not_provided_parts"`
	UnselectedUnitIDs        []string `json:"unselected_unit_ids"`
	FactCompletenessAssessed bool     `json:"fact_completeness_assessed"`
}

// Request is an immutable in-memory snapshot. It is not an AHE extraction run
// or a durable attempt receipt. Construct it with Prepare before opening a model.
type Request struct {
	id     string
	task   Task
	source Source
	model  string
	units  []Unit
	scope  Scope
	prompt string
}

// Prepare freezes task/source inputs and rejects oversized inputs as a whole.
// A request with no readable supplied units remains inspectable via Scope;
// Extract rejects it before calling a model.
func Prepare(task Task, source Source, modelName string) (*Request, error) {
	if err := validateInput(task, source, modelName); err != nil {
		return nil, err
	}
	task.Parts = slices.Clone(task.Parts)
	source.Parts = slices.Clone(source.Parts)
	source.Limitations = slices.Clone(source.Limitations)
	r := &Request{task: task, source: source, model: modelName, units: []Unit{}}
	r.scope = Scope{
		RequestedParts: slices.Clone(task.Parts), ProvidedParts: []string{},
		NotCollectedParts: []string{}, NotProvidedParts: []string{}, UnselectedUnitIDs: []string{},
	}
	available := make(map[string]bool, len(source.Parts))
	providedBytes := 0
	for _, part := range source.Parts {
		available[part.Name] = true
		if !slices.Contains(task.Parts, part.Name) {
			r.scope.NotProvidedParts = append(r.scope.NotProvidedParts, part.Name)
			continue
		}
		providedBytes += len(part.Text)
		if providedBytes > MaxProvidedBytes {
			return nil, errors.New("task source exceeds provided text limit")
		}
		r.scope.ProvidedParts = append(r.scope.ProvidedParts, part.Name)
		r.units = appendParagraphs(r.units, part)
		if len(r.units) > MaxUnits {
			return nil, errors.New("task source exceeds paragraph limit")
		}
	}
	for _, part := range task.Parts {
		if !available[part] {
			r.scope.NotCollectedParts = append(r.scope.NotCollectedParts, part)
		}
	}
	for i := range r.units {
		r.units[i].ID = fmt.Sprintf("u%03d", i+1)
		r.scope.UnselectedUnitIDs = append(r.scope.UnselectedUnitIDs, r.units[i].ID)
	}
	// Labels alone cannot bind a new question to its correct source and policy.
	// Include exact content, provenance, purpose, scope, model and all versions.
	binding := struct {
		Version, ProjectionVersion, ModelInputVersion, PromptVersion, Instruction string
		Task                                                                      Task
		Source                                                                    Source
		Model                                                                     string
	}{
		Version: Version, ProjectionVersion: ProjectionVersion, ModelInputVersion: ModelInputVersion,
		PromptVersion: PromptVersion, Instruction: instruction, Task: task, Source: source, Model: modelName,
	}
	raw, err := json.Marshal(binding)
	if err != nil {
		return nil, errors.New("task input could not be encoded")
	}
	r.id = "task-input:sha256:" + hashText(string(raw))
	prompt, err := r.modelInput()
	if err != nil {
		return nil, err
	}
	r.prompt = prompt
	return r, nil
}

// InputID binds the complete frozen input and policy, not an invocation count.
func (r *Request) InputID() string { return r.id }

// Task returns the frozen purpose and source scope, detached from the request.
func (r *Request) Task() Task {
	task := r.task
	task.Parts = slices.Clone(task.Parts)
	return task
}

// Source returns a detached copy of the frozen source and all declared metadata.
// Future handoff/persistence must retain this context, not only candidate text.
func (r *Request) Source() Source {
	source := r.source
	source.Parts = slices.Clone(source.Parts)
	source.Limitations = slices.Clone(source.Limitations)
	return source
}

// Units returns a detached paragraph catalog, preserving the original text.
func (r *Request) Units() []Unit { return slices.Clone(r.units) }

// Scope returns detached supply metadata. Unselected does not mean irrelevant.
func (r *Request) Scope() Scope { return cloneScope(r.scope) }

func cloneScope(s Scope) Scope {
	s.RequestedParts = slices.Clone(s.RequestedParts)
	s.ProvidedParts = slices.Clone(s.ProvidedParts)
	s.NotCollectedParts = slices.Clone(s.NotCollectedParts)
	s.NotProvidedParts = slices.Clone(s.NotProvidedParts)
	s.UnselectedUnitIDs = slices.Clone(s.UnselectedUnitIDs)
	return s
}

func validateInput(t Task, s Source, modelName string) error {
	bad := errors.New("invalid task source, scope or model")
	if !validText(t.ID, 200, false) || t.Revision < 1 || !validText(t.Objective, 2048, true) ||
		!validText(t.SourceID, 512, false) || t.SourceID != s.ID || !validText(modelName, 200, false) ||
		strings.TrimSpace(modelName) != modelName || len(t.Parts) == 0 || len(t.Parts) > MaxParts ||
		!validText(s.Revision, 512, false) || !validText(s.Title, 1024, false) ||
		!validText(s.Location, 2048, false) || len(s.Parts) == 0 || len(s.Parts) > MaxParts ||
		s.Limitations == nil || len(s.Limitations) > MaxParts {
		return bad
	}
	switch s.Coverage {
	case "full_document":
		if len(s.Limitations) != 0 {
			return bad
		}
	case "exact_excerpt", "truncated_document":
		if len(s.Limitations) == 0 {
			return bad
		}
	default:
		return bad
	}
	for _, limitation := range s.Limitations {
		if !validText(limitation, 1024, false) {
			return bad
		}
	}
	seen := map[string]bool{}
	for _, part := range t.Parts {
		if !validText(part, 128, false) || seen[part] {
			return bad
		}
		seen[part] = true
	}
	seen = map[string]bool{}
	total := 0
	for _, part := range s.Parts {
		if !validText(part.Name, 128, false) || seen[part.Name] || !utf8.ValidString(part.Text) || strings.ContainsRune(part.Text, 0) {
			return bad
		}
		total += len(part.Text)
		if total > MaxSourceBytes {
			return errors.New("task source exceeds total text limit")
		}
		seen[part.Name] = true
	}
	return nil
}

func validText(text string, limit int, multiline bool) bool {
	if len(text) > limit || strings.TrimSpace(text) == "" || !utf8.ValidString(text) {
		return false
	}
	for _, r := range text {
		if unicode.IsControl(r) && !(multiline && (r == '\n' || r == '\r' || r == '\t')) {
			return false
		}
	}
	return true
}

func hashText(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

// appendParagraphs separates only blank physical lines. It never normalizes
// CRLF, interprets HTML, trims paragraph text or guesses sentence boundaries.
// Headings/fences can need adjacent context; these boundaries do not prove SVO.
func appendParagraphs(units []Unit, part SourcePart) []Unit {
	start, end := -1, 0
	appendUnit := func() {
		if start >= 0 {
			units = append(units, Unit{Part: part.Name, StartByte: start, EndByte: end, Text: part.Text[start:end]})
			start = -1
		}
	}
	for offset := 0; offset < len(part.Text); {
		lineStart := offset
		for offset < len(part.Text) && part.Text[offset] != '\r' && part.Text[offset] != '\n' {
			offset++
		}
		lineEnd := offset
		if offset < len(part.Text) {
			if part.Text[offset] == '\r' && offset+1 < len(part.Text) && part.Text[offset+1] == '\n' {
				offset++
			}
			offset++
		}
		if strings.TrimSpace(part.Text[lineStart:lineEnd]) == "" {
			appendUnit()
		} else {
			if start < 0 {
				start = lineStart
			}
			end = lineEnd
		}
	}
	appendUnit()
	return units
}
