package codegraphadapter

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"
)

const (
	maxSourceSpanLines = 200
	maxSourceSpanBytes = 8 << 10
)

// Result is one bounded, non-authoritative CodeGraph candidate package.
type Result struct {
	Contract            string               `json:"contract"`
	AdapterVersion      string               `json:"adapter_version"`
	ProviderRelease     string               `json:"provider_release"`
	Repository          RepositoryCoordinate `json:"repository"`
	Query               Query                `json:"query"`
	Definitions         []Definition         `json:"definitions"`
	CallerCandidates    []CallerCandidate    `json:"caller_candidates"`
	Coverage            Coverage             `json:"coverage"`
	StableCandidateHash string               `json:"stable_candidate_hash"`
	RawProvider         RawProviderAudit     `json:"raw_provider"`
	Limitations         []string             `json:"limitations"`
}

// RepositoryCoordinate binds candidates to one exact Git revision.
type RepositoryCoordinate struct {
	RepositoryID string `json:"repository_id"`
	CommitSHA    string `json:"commit_sha"`
}

// Query records the exact bounded lookup requested from CodeGraph.
type Query struct {
	Symbol         string `json:"symbol"`
	Kind           string `json:"kind"`
	Limit          int    `json:"limit"`
	IncludeCallers bool   `json:"include_callers"`
}

// Definition is one provider candidate anchored to immutable source bytes.
type Definition struct {
	CandidateID   string       `json:"candidate_id"`
	Kind          string       `json:"kind"`
	Name          string       `json:"name"`
	QualifiedName string       `json:"qualified_name"`
	Language      string       `json:"language"`
	Signature     string       `json:"signature"`
	Source        SourceAnchor `json:"source"`
}

// CallerCandidate is a provider-derived caller with a declaration anchor.
type CallerCandidate struct {
	CandidateID string       `json:"candidate_id"`
	Name        string       `json:"name"`
	Kind        string       `json:"kind"`
	Source      SourceAnchor `json:"source"`
}

// SourceAnchor contains exact lines read from the configured Git commit.
type SourceAnchor struct {
	FilePath    string `json:"file_path"`
	StartLine   int    `json:"start_line"`
	EndLine     int    `json:"end_line"`
	StartColumn int    `json:"start_column,omitempty"`
	EndColumn   int    `json:"end_column,omitempty"`
	Content     string `json:"content"`
	ContentHash string `json:"content_hash"`
}

// Coverage states the provider's bounded and incomplete search surface.
type Coverage struct {
	SearchedSurface               string `json:"searched_surface"`
	DefinitionLimit               int    `json:"definition_limit"`
	DefinitionCount               int    `json:"definition_count"`
	CallerLimit                   int    `json:"caller_limit"`
	CallerCount                   int    `json:"caller_count"`
	DefinitionResultAtLimit       bool   `json:"definition_result_at_limit"`
	CallerResultAtLimit           bool   `json:"caller_result_at_limit"`
	SearchCompleteWithinSurface   bool   `json:"search_complete_within_surface"`
	CompletionReason              string `json:"completion_reason"`
	GlobalAbsenceInferenceAllowed bool   `json:"global_absence_inference_allowed"`
}

// RawProviderAudit preserves exact provider bytes outside stable equivalence.
type RawProviderAudit struct {
	QueryBase64   string `json:"query_base64"`
	QueryHash     string `json:"query_hash"`
	CallersBase64 string `json:"callers_base64,omitempty"`
	CallersHash   string `json:"callers_hash,omitempty"`
}

type providerSearchItem struct {
	Node  providerNode `json:"node"`
	Score float64      `json:"score"`
}

type providerNode struct {
	ID            string  `json:"id"`
	Kind          string  `json:"kind"`
	Name          string  `json:"name"`
	QualifiedName string  `json:"qualifiedName"`
	FilePath      string  `json:"filePath"`
	Language      string  `json:"language"`
	StartLine     int     `json:"startLine"`
	EndLine       int     `json:"endLine"`
	StartColumn   int     `json:"startColumn"`
	EndColumn     int     `json:"endColumn"`
	Signature     string  `json:"signature"`
	Visibility    *string `json:"visibility"`
	IsExported    bool    `json:"isExported"`
	IsAsync       bool    `json:"isAsync"`
	IsStatic      bool    `json:"isStatic"`
	IsAbstract    bool    `json:"isAbstract"`
	ReturnType    string  `json:"returnType"`
	UpdatedAt     int64   `json:"updatedAt"`
}

type providerCallers struct {
	Symbol  string           `json:"symbol"`
	Callers []providerCaller `json:"callers"`
}

type providerCaller struct {
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	FilePath  string `json:"filePath"`
	StartLine int    `json:"startLine"`
}

type sourceLoader func(context.Context, string) ([]byte, error)

func normalizeResult(
	ctx context.Context,
	config Config,
	input queryFunctionInput,
	queryRaw, callersRaw []byte,
	load sourceLoader,
) (Result, error) {
	if load == nil {
		return Result{}, errors.New("CodeGraph source loader is required")
	}
	var search []providerSearchItem
	if err := decodeProviderJSON(queryRaw, &search); err != nil {
		return Result{}, fmt.Errorf("decoding CodeGraph query output: %w", err)
	}
	callers := providerCallers{Symbol: input.Symbol}
	if input.IncludeCallers {
		if err := decodeProviderJSON(callersRaw, &callers); err != nil {
			return Result{}, fmt.Errorf("decoding CodeGraph callers output: %w", err)
		}
		if callers.Symbol != input.Symbol {
			return Result{}, fmt.Errorf(
				"CodeGraph callers symbol %q does not match requested %q",
				callers.Symbol,
				input.Symbol,
			)
		}
	}

	cache := make(map[string][]byte)
	loadCached := func(path string) ([]byte, error) {
		if value, ok := cache[path]; ok {
			return value, nil
		}
		value, err := load(ctx, path)
		if err != nil {
			return nil, err
		}
		cache[path] = value
		return value, nil
	}
	definitions := make([]Definition, 0, len(search))
	for _, item := range search {
		definition, err := normalizeDefinition(config, item.Node, loadCached)
		if err != nil {
			return Result{}, err
		}
		definitions = append(definitions, definition)
	}
	callerCandidates := make([]CallerCandidate, 0, len(callers.Callers))
	for _, caller := range callers.Callers {
		candidate, err := normalizeCaller(config, caller, loadCached)
		if err != nil {
			return Result{}, err
		}
		callerCandidates = append(callerCandidates, candidate)
	}
	slices.SortFunc(definitions, func(left, right Definition) int {
		return strings.Compare(definitionSortKey(left), definitionSortKey(right))
	})
	slices.SortFunc(callerCandidates, func(left, right CallerCandidate) int {
		return strings.Compare(callerSortKey(left), callerSortKey(right))
	})

	limitations := []string{
		"CodeGraph output is a non-authoritative indexed candidate and does not create evidence, a proposal, or a canonical relation.",
		"Each lookup rebuilds the index from the configured clean Git HEAD and anchors returned source lines to immutable Git object bytes.",
		"Caller locations identify caller declarations reported by CodeGraph, not exact call sites.",
		"CodeGraph v1.5.0 does not report a total result count, so bounded lookup cannot establish completeness or global absence.",
		"Provider scores and updatedAt fields remain in raw audit but are excluded from stable candidate equivalence.",
	}
	coverage := Coverage{
		SearchedSurface:               "codegraph-v1.5.0-function-index",
		DefinitionLimit:               input.Limit,
		DefinitionCount:               len(definitions),
		CallerLimit:                   input.Limit,
		CallerCount:                   len(callerCandidates),
		DefinitionResultAtLimit:       len(definitions) == input.Limit,
		CallerResultAtLimit:           input.IncludeCallers && len(callerCandidates) == input.Limit,
		SearchCompleteWithinSurface:   false,
		CompletionReason:              "provider_total_count_unavailable",
		GlobalAbsenceInferenceAllowed: false,
	}
	result := Result{
		Contract:        ResultContract,
		AdapterVersion:  AdapterVersion,
		ProviderRelease: ProviderRelease,
		Repository: RepositoryCoordinate{
			RepositoryID: config.RepositoryID,
			CommitSHA:    config.CommitSHA,
		},
		Query: Query{
			Symbol:         input.Symbol,
			Kind:           "function",
			Limit:          input.Limit,
			IncludeCallers: input.IncludeCallers,
		},
		Definitions:      definitions,
		CallerCandidates: callerCandidates,
		Coverage:         coverage,
		RawProvider: RawProviderAudit{
			QueryBase64: base64.StdEncoding.EncodeToString(queryRaw),
			QueryHash:   contentHash(queryRaw),
		},
		Limitations: limitations,
	}
	if input.IncludeCallers {
		result.RawProvider.CallersBase64 = base64.StdEncoding.EncodeToString(callersRaw)
		result.RawProvider.CallersHash = contentHash(callersRaw)
	}
	hash, err := stableCandidateHash(result)
	if err != nil {
		return Result{}, err
	}
	result.StableCandidateHash = hash
	return result, nil
}

func decodeProviderJSON(raw []byte, destination any) error {
	if len(raw) == 0 || len(raw) > maxCommandOutput {
		return fmt.Errorf("provider JSON must contain 1 to %d bytes", maxCommandOutput)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("provider output must contain one JSON value")
	}
	return nil
}

func normalizeDefinition(
	config Config,
	node providerNode,
	load func(string) ([]byte, error),
) (Definition, error) {
	path, err := repositorySourcePath(config, node.FilePath)
	if err != nil {
		return Definition{}, err
	}
	if strings.TrimSpace(node.ID) == "" ||
		strings.TrimSpace(node.Kind) == "" ||
		strings.TrimSpace(node.Name) == "" ||
		strings.TrimSpace(node.Language) == "" {
		return Definition{}, fmt.Errorf("CodeGraph definition at %q has missing identity fields", path)
	}
	source, err := sourceAnchor(load, path, node.StartLine, node.EndLine, node.StartColumn, node.EndColumn)
	if err != nil {
		return Definition{}, err
	}
	definition := Definition{
		Kind:          strings.TrimSpace(node.Kind),
		Name:          strings.TrimSpace(node.Name),
		QualifiedName: strings.TrimSpace(node.QualifiedName),
		Language:      strings.TrimSpace(node.Language),
		Signature:     strings.TrimSpace(node.Signature),
		Source:        source,
	}
	definition.CandidateID, err = candidateID("definition", config, definition)
	if err != nil {
		return Definition{}, err
	}
	return definition, nil
}

func normalizeCaller(
	config Config,
	caller providerCaller,
	load func(string) ([]byte, error),
) (CallerCandidate, error) {
	path, err := repositorySourcePath(config, caller.FilePath)
	if err != nil {
		return CallerCandidate{}, err
	}
	if strings.TrimSpace(caller.Name) == "" || strings.TrimSpace(caller.Kind) == "" {
		return CallerCandidate{}, fmt.Errorf("CodeGraph caller at %q has missing identity fields", path)
	}
	source, err := sourceAnchor(load, path, caller.StartLine, caller.StartLine, 0, 0)
	if err != nil {
		return CallerCandidate{}, err
	}
	candidate := CallerCandidate{
		Name:   strings.TrimSpace(caller.Name),
		Kind:   strings.TrimSpace(caller.Kind),
		Source: source,
	}
	candidate.CandidateID, err = candidateID("caller", config, candidate)
	if err != nil {
		return CallerCandidate{}, err
	}
	return candidate, nil
}

func cleanSourcePath(path string) (string, error) {
	path = filepath.ToSlash(strings.TrimSpace(path))
	clean := filepath.ToSlash(filepath.Clean(path))
	if clean == "." || clean == "" || filepath.IsAbs(path) || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("CodeGraph source path %q is outside the repository", path)
	}
	return clean, nil
}

func repositorySourcePath(config Config, providerPath string) (string, error) {
	clean, err := cleanSourcePath(providerPath)
	if err != nil {
		return "", err
	}
	if config.sourcePrefix == "" {
		return clean, nil
	}
	return cleanSourcePath(config.sourcePrefix + "/" + clean)
}

func sourceAnchor(
	load func(string) ([]byte, error),
	path string,
	startLine, endLine, startColumn, endColumn int,
) (SourceAnchor, error) {
	if startLine < 1 || endLine < startLine || endLine-startLine+1 > maxSourceSpanLines {
		return SourceAnchor{}, fmt.Errorf("CodeGraph source span %s:%d-%d is invalid", path, startLine, endLine)
	}
	content, err := load(path)
	if err != nil {
		return SourceAnchor{}, err
	}
	if !utf8.Valid(content) {
		return SourceAnchor{}, fmt.Errorf("CodeGraph source file %q is not valid UTF-8", path)
	}
	lines := bytes.Split(content, []byte("\n"))
	if endLine > len(lines) {
		return SourceAnchor{}, fmt.Errorf(
			"CodeGraph source span %s:%d-%d exceeds %d lines",
			path,
			startLine,
			endLine,
			len(lines),
		)
	}
	span := bytes.Join(lines[startLine-1:endLine], []byte("\n"))
	if len(span) > maxSourceSpanBytes {
		return SourceAnchor{}, fmt.Errorf(
			"CodeGraph source span %s:%d-%d exceeds %d bytes",
			path,
			startLine,
			endLine,
			maxSourceSpanBytes,
		)
	}
	return SourceAnchor{
		FilePath:    path,
		StartLine:   startLine,
		EndLine:     endLine,
		StartColumn: startColumn,
		EndColumn:   endColumn,
		Content:     string(span),
		ContentHash: contentHash(span),
	}, nil
}

func stableCandidateHash(result Result) (string, error) {
	stable := struct {
		Contract         string               `json:"contract"`
		AdapterVersion   string               `json:"adapter_version"`
		ProviderRelease  string               `json:"provider_release"`
		Repository       RepositoryCoordinate `json:"repository"`
		Query            Query                `json:"query"`
		Definitions      []Definition         `json:"definitions"`
		CallerCandidates []CallerCandidate    `json:"caller_candidates"`
		Coverage         Coverage             `json:"coverage"`
		Limitations      []string             `json:"limitations"`
	}{
		Contract:         result.Contract,
		AdapterVersion:   result.AdapterVersion,
		ProviderRelease:  result.ProviderRelease,
		Repository:       result.Repository,
		Query:            result.Query,
		Definitions:      result.Definitions,
		CallerCandidates: result.CallerCandidates,
		Coverage:         result.Coverage,
		Limitations:      result.Limitations,
	}
	raw, err := json.Marshal(stable)
	if err != nil {
		return "", fmt.Errorf("encoding stable CodeGraph candidate: %w", err)
	}
	return contentHash(raw), nil
}

func candidateID(kind string, config Config, value any) (string, error) {
	raw, err := json.Marshal(struct {
		Kind         string `json:"kind"`
		RepositoryID string `json:"repository_id"`
		CommitSHA    string `json:"commit_sha"`
		Value        any    `json:"value"`
	}{
		Kind:         kind,
		RepositoryID: config.RepositoryID,
		CommitSHA:    config.CommitSHA,
		Value:        value,
	})
	if err != nil {
		return "", fmt.Errorf("encoding CodeGraph candidate identity: %w", err)
	}
	return "codegraph-candidate:" + strings.TrimPrefix(contentHash(raw), "sha256:"), nil
}

func contentHash(value []byte) string {
	sum := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func definitionSortKey(value Definition) string {
	return fmt.Sprintf(
		"%s\x00%09d\x00%09d\x00%s\x00%s\x00%s",
		value.Source.FilePath,
		value.Source.StartLine,
		value.Source.EndLine,
		value.Kind,
		value.Name,
		value.Signature,
	)
}

func callerSortKey(value CallerCandidate) string {
	return fmt.Sprintf(
		"%s\x00%09d\x00%s\x00%s",
		value.Source.FilePath,
		value.Source.StartLine,
		value.Kind,
		value.Name,
	)
}
