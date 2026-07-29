package evidenceingestion

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestExtractGoParserFileDeclarations(t *testing.T) {
	source := []byte(strings.Join([]string{
		"package refund",
		"",
		"type RefundService struct{}",
		"",
		`const refundRoute = "POST /refunds"`,
		"",
		"var defaultLimit = 7",
		"",
		"func NewRefundService() *RefundService {",
		"\treturn &RefundService{}",
		"}",
		"",
		"func (s *RefundService) Create(orderID string) error {",
		"\treturn nil",
		"}",
		"",
	}, "\n"))

	extraction, err := ExtractGoParserFile(GoParserFileInput{
		RepoID:    "ahe-wrap",
		CommitSHA: "abc123",
		Path:      "internal/refund/service.go",
		Source:    source,
	})
	if err != nil {
		t.Fatalf("ExtractGoParserFile() error = %v", err)
	}

	if extraction.ExtractorDefinition.Name != ExtractorGoParserCodeFact || extraction.ExtractorDefinition.Version != ExtractorGoParserCodeFactVersion {
		t.Fatalf("extractor definition = %+v, want go-parser-code-fact/v1", extraction.ExtractorDefinition)
	}
	if got := len(extraction.Declarations); got != 5 {
		t.Fatalf("len(Declarations) = %d, want 5: %+v", got, extraction.Declarations)
	}
	packageClause := extraction.PackageClause
	if packageClause.Name != "refund" || packageClause.StatementText != "Go package refund is declared in internal/refund/service.go:1." {
		t.Fatalf("package clause = %+v", packageClause)
	}
	if packageClause.CodeFact.SchemaVersion != CodeFactSchemaV2 || packageClause.CodeFact.FactKind != CodeFactKindPackage || packageClause.CodeFact.SymbolKind != CodeFactKindPackage {
		t.Fatalf("package code fact = %+v", packageClause.CodeFact)
	}
	if got := string(source[packageClause.StartByte:packageClause.EndByte]); got != "refund" {
		t.Fatalf("package quoted bytes = %q, want refund", got)
	}
	gotStatements := make([]string, 0, len(extraction.Output.Proposals))
	for _, proposal := range extraction.Output.Proposals {
		gotStatements = append(gotStatements, proposal.StatementText)
	}
	wantStatements := []string{
		"Go type refund.RefundService is declared in internal/refund/service.go:3.",
		"Go const refund.refundRoute is declared in internal/refund/service.go:5.",
		"Go var refund.defaultLimit is declared in internal/refund/service.go:7.",
		"Go function refund.NewRefundService is declared in internal/refund/service.go:9.",
		"Go method refund.RefundService.Create is declared in internal/refund/service.go:13.",
	}
	if !reflect.DeepEqual(gotStatements, wantStatements) {
		t.Fatalf("statements = %#v, want %#v", gotStatements, wantStatements)
	}

	method := extraction.Declarations[4]
	if method.SymbolRef != "symbol:ahe-wrap:abc123:internal/refund/service.go:refund.RefundService.Create" {
		t.Fatalf("method symbol ref = %q", method.SymbolRef)
	}
	if !reflect.DeepEqual(method.EvidenceRefs, []string{"span:S8"}) {
		t.Fatalf("method evidence refs = %#v, want S8", method.EvidenceRefs)
	}
	if method.CodeFact.SymbolRef != method.SymbolRef || method.CodeFact.QualifiedName != "refund.RefundService.Create" {
		t.Fatalf("method code fact identity = %+v", method.CodeFact)
	}
	if got := string(source[method.CodeFact.StartByte:method.CodeFact.EndByte]); got != "Create" {
		t.Fatalf("method code fact quoted bytes = %q, want Create", got)
	}
	if method.CodeFact.QuotedTextHash != contentHash([]byte("Create")) {
		t.Fatalf("method code fact quote hash = %q", method.CodeFact.QuotedTextHash)
	}
	if extraction.Output.Proposals[3].ProposalLocalID != "go-decl-function-refund.NewRefundService-L9" {
		t.Fatalf("function local ID = %q", extraction.Output.Proposals[3].ProposalLocalID)
	}
}

func TestExtractGoParserFileUsesProvidedLineSpans(t *testing.T) {
	source := []byte("package main\nfunc Build() {}\n")
	extraction, err := ExtractGoParserFile(GoParserFileInput{
		RepoID:    "repo",
		CommitSHA: "commit",
		Path:      "build.go",
		Source:    source,
		Spans: []ExtractorInputSpan{{
			SpanID:      "span:custom",
			DisplayLine: 2,
		}},
	})
	if err != nil {
		t.Fatalf("ExtractGoParserFile() error = %v", err)
	}
	if len(extraction.Output.Proposals) != 1 {
		t.Fatalf("len(proposals) = %d, want 1", len(extraction.Output.Proposals))
	}
	if !reflect.DeepEqual(extraction.Output.Proposals[0].EvidenceRefs, []string{"span:custom"}) {
		t.Fatalf("evidence refs = %#v, want custom span", extraction.Output.Proposals[0].EvidenceRefs)
	}
	if len(extraction.PackageClause.EvidenceRefs) != 0 {
		t.Fatalf("package evidence refs = %#v, want none for omitted package line", extraction.PackageClause.EvidenceRefs)
	}
}

func TestExtractGoParserFileRejectsInvalidInput(t *testing.T) {
	t.Run("invalid utf8", func(t *testing.T) {
		_, err := ExtractGoParserFile(GoParserFileInput{
			RepoID:    "repo",
			CommitSHA: "commit",
			Path:      "bad.go",
			Source:    []byte{0xff, 0xfe},
		})
		assertKind(t, err, ErrorInvalidUTF8)
	})

	t.Run("parse error", func(t *testing.T) {
		_, err := ExtractGoParserFile(GoParserFileInput{
			RepoID:    "repo",
			CommitSHA: "commit",
			Path:      "bad.go",
			Source:    []byte("package main\nfunc {}\n"),
		})
		assertKind(t, err, ErrorInvalidInput)
	})

	t.Run("missing repo", func(t *testing.T) {
		_, err := ExtractGoParserFile(GoParserFileInput{
			CommitSHA: "commit",
			Path:      "bad.go",
			Source:    []byte("package main\n"),
		})
		assertKind(t, err, ErrorInvalidInput)
	})
}

func TestGoParserExtractorRunnerProducesStrictOutput(t *testing.T) {
	rendered := "package app\nfunc NewRouter() {}\n"
	renderedHash := contentHash([]byte(rendered))
	runner, err := NewGoParserExtractorRunner(GoParserExtractorConfig{
		RepoID:    "repo",
		CommitSHA: "commit",
		Path:      "router.go",
	})
	if err != nil {
		t.Fatalf("NewGoParserExtractorRunner() error = %v", err)
	}
	data, err := runner.Run(context.Background(), ExtractorInput{
		SourceSystem:        SourceSystemCodeFile,
		RawContentHash:      renderedHash,
		RenderedContentHash: renderedHash,
		RenderedText:        rendered,
		SpanCatalogVersion:  SpanCatalogCodeLineV1,
		Spans: []ExtractorInputSpan{{
			SpanID:      "span:S2",
			DisplayLine: 2,
		}},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	var output FrozenExtractorOutput
	if err := json.Unmarshal(data, &output); err != nil {
		t.Fatalf("Unmarshal output: %v", err)
	}
	if len(output.Proposals) != 1 {
		t.Fatalf("len(proposals) = %d, want 1", len(output.Proposals))
	}
	if output.Proposals[0].StatementText != "Go function app.NewRouter is declared in router.go:2." {
		t.Fatalf("statement = %q", output.Proposals[0].StatementText)
	}
	if output.Proposals[0].CodeFact == nil || output.Proposals[0].CodeFact.QualifiedName != "app.NewRouter" {
		t.Fatalf("code fact = %+v", output.Proposals[0].CodeFact)
	}

	decoded, err := decodeTrustedExtractorOutput(data)
	if err != nil {
		t.Fatalf("decodeTrustedExtractorOutput() error = %v", err)
	}
	if !reflect.DeepEqual(decoded, output) {
		t.Fatalf("decoded output = %#v, want %#v", decoded, output)
	}
}
