package evidenceingestion

import (
	"context"
	"testing"
)

func TestMaterializeCodeFactVerifiesSourceBoundIdentity(t *testing.T) {
	ctx, output := mustCodeAttemptAndOutput(t, "code-fact-run")
	batch, err := materializeBatch(ctx, output)
	if err != nil {
		t.Fatalf("materializeBatch() error = %v", err)
	}
	if len(batch.Occurrences) != 1 {
		t.Fatalf("len(Occurrences) = %d, want 1", len(batch.Occurrences))
	}

	occurrence := batch.Occurrences[0]
	if occurrence.ProposalFingerprintVersion != ProposalFingerprintCodeFactV1 {
		t.Fatalf("fingerprint version = %q, want %q", occurrence.ProposalFingerprintVersion, ProposalFingerprintCodeFactV1)
	}
	if occurrence.CodeFact == nil {
		t.Fatal("resolved code fact is nil")
	}
	if occurrence.CodeFact.SymbolRef != "symbol:ahe-wrap:abc123:internal/refund/service.go:refund.Build" {
		t.Fatalf("symbol ref = %q", occurrence.CodeFact.SymbolRef)
	}
	if occurrence.CodeFact.QuotedText != "Build" || occurrence.CodeFact.StartLine != 3 || occurrence.CodeFact.EndLine != 3 {
		t.Fatalf("resolved code fact = %+v", occurrence.CodeFact)
	}
	payloadFact, ok := occurrence.ProposedPayload["code_fact"].(ResolvedCodeFact)
	if !ok || payloadFact.SymbolRef != occurrence.CodeFact.SymbolRef {
		t.Fatalf("proposed payload code fact = %#v", occurrence.ProposedPayload["code_fact"])
	}

	secondCtx, secondOutput := mustCodeAttemptAndOutput(t, "code-fact-comparison-run")
	secondBatch, err := materializeBatch(secondCtx, secondOutput)
	if err != nil {
		t.Fatalf("materializeBatch(second run) error = %v", err)
	}
	if occurrence.ProposalFingerprint != secondBatch.Occurrences[0].ProposalFingerprint {
		t.Fatalf("same verified code fact produced fingerprints %q and %q", occurrence.ProposalFingerprint, secondBatch.Occurrences[0].ProposalFingerprint)
	}
	if occurrence.ID == secondBatch.Occurrences[0].ID {
		t.Fatalf("distinct code extraction attempts produced occurrence %q", occurrence.ID)
	}
}

func TestMaterializePackageCodeFactVerifiesPackageClause(t *testing.T) {
	input := testCodeSourceInput("package-code-fact")
	sourceCtx, err := buildManualSourceContext(input)
	if err != nil {
		t.Fatalf("buildManualSourceContext() error = %v", err)
	}
	extraction, err := ExtractGoParserFile(GoParserFileInput{
		RepoID:    input.OriginMetadata["repo_id"],
		CommitSHA: input.OriginMetadata["commit_sha"],
		Path:      input.OriginMetadata["path"],
		Source:    input.Raw,
		Spans:     sourceCtx.extractorInput().Spans,
	})
	if err != nil {
		t.Fatalf("ExtractGoParserFile() error = %v", err)
	}
	packageFact := extraction.PackageClause.CodeFact
	output := FrozenExtractorOutput{Proposals: []ExtractorProposalOutput{{
		ProposalLocalID: extraction.PackageClause.ProposalLocalID,
		StatementText:   extraction.PackageClause.StatementText,
		EvidenceRefs:    append([]string(nil), extraction.PackageClause.EvidenceRefs...),
		CodeFact:        &packageFact,
	}}}
	ctx, err := buildAttemptContextFromSourceWithDefinition(sourceCtx, input.RequestID, 1, goParserExtractorDefinition())
	if err != nil {
		t.Fatalf("buildAttemptContextFromSourceWithDefinition() error = %v", err)
	}
	batch, err := materializeBatch(ctx, output)
	if err != nil {
		t.Fatalf("materializeBatch() error = %v", err)
	}
	if len(batch.Occurrences) != 1 || batch.Occurrences[0].CodeFact == nil {
		t.Fatalf("package occurrences = %+v", batch.Occurrences)
	}
	if batch.Occurrences[0].ProposalFingerprintVersion != ProposalFingerprintCodeFactV1 {
		t.Fatalf("package fingerprint version = %q, want %q", batch.Occurrences[0].ProposalFingerprintVersion, ProposalFingerprintCodeFactV1)
	}
	resolved := batch.Occurrences[0].CodeFact
	if resolved.SchemaVersion != CodeFactSchemaV2 || resolved.FactKind != CodeFactKindPackage || resolved.QualifiedName != "refund" || resolved.QuotedText != "refund" {
		t.Fatalf("resolved package fact = %+v", resolved)
	}

	tampered := cloneExtractorOutput(output)
	tampered.Proposals[0].CodeFact.QualifiedName = "other"
	tampered.Proposals[0].CodeFact.SymbolRef = codeSymbolRef(input.OriginMetadata["repo_id"], input.OriginMetadata["commit_sha"], input.OriginMetadata["path"], "other")
	tampered.Proposals[0].StatementText = codePackageStatement("other", input.OriginMetadata["path"], 1, 1)
	_, err = materializeBatch(ctx, tampered)
	assertKind(t, err, ErrorInvalidExtractorOutput)
}

func TestMaterializeCodeFactRejectsUnverifiedFields(t *testing.T) {
	ctx, valid := mustCodeAttemptAndOutput(t, "code-fact-validation")
	tests := []struct {
		name   string
		mutate func(*ExtractorProposalOutput)
		kind   ErrorKind
	}{
		{
			name: "schema version",
			mutate: func(proposal *ExtractorProposalOutput) {
				proposal.CodeFact.SchemaVersion = "code-fact-v2"
			},
			kind: ErrorInvalidExtractorOutput,
		},
		{
			name: "origin path",
			mutate: func(proposal *ExtractorProposalOutput) {
				proposal.CodeFact.Path = "other.go"
			},
			kind: ErrorInvalidExtractorOutput,
		},
		{
			name: "file content hash",
			mutate: func(proposal *ExtractorProposalOutput) {
				proposal.CodeFact.FileContentHash = "sha256:bad"
			},
			kind: ErrorQuotedHashMismatch,
		},
		{
			name: "out of bounds",
			mutate: func(proposal *ExtractorProposalOutput) {
				proposal.CodeFact.EndByte = len(ctx.ExtractionView.Rendered) + 1
			},
			kind: ErrorSpanOutOfBounds,
		},
		{
			name: "quoted text hash",
			mutate: func(proposal *ExtractorProposalOutput) {
				proposal.CodeFact.QuotedTextHash = "sha256:bad"
			},
			kind: ErrorQuotedHashMismatch,
		},
		{
			name: "uncovered exact span",
			mutate: func(proposal *ExtractorProposalOutput) {
				proposal.EvidenceRefs = []string{"span:S1"}
			},
			kind: ErrorUnknownSpan,
		},
		{
			name: "symbol ref",
			mutate: func(proposal *ExtractorProposalOutput) {
				proposal.CodeFact.SymbolRef = "symbol:unverified"
			},
			kind: ErrorInvalidExtractorOutput,
		},
		{
			name: "derived statement",
			mutate: func(proposal *ExtractorProposalOutput) {
				proposal.StatementText = "Build might be declared somewhere."
			},
			kind: ErrorInvalidExtractorOutput,
		},
		{
			name: "self consistent wrong declaration kind",
			mutate: func(proposal *ExtractorProposalOutput) {
				proposal.CodeFact.SymbolKind = "var"
				proposal.StatementText = codeDeclarationStatement(
					proposal.CodeFact.SymbolKind,
					proposal.CodeFact.QualifiedName,
					proposal.CodeFact.Path,
					3,
					3,
				)
			},
			kind: ErrorInvalidExtractorOutput,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			output := cloneExtractorOutput(valid)
			tt.mutate(&output.Proposals[0])
			_, err := materializeBatch(ctx, output)
			assertKind(t, err, tt.kind)
		})
	}
}

func TestMaterializeCodeFactRequiresCodeSourceAuthority(t *testing.T) {
	_, output := mustCodeAttemptAndOutput(t, "code-fact-source-kind")
	input := testCodeSourceInput("manual-source-kind")
	input.SourceSystem = SourceSystemManualText
	ctx := mustAttemptContext(t, input)

	_, err := materializeBatch(ctx, output)
	assertKind(t, err, ErrorInvalidExtractorOutput)
}

func mustCodeAttemptAndOutput(t *testing.T, requestID string) (attemptContext, FrozenExtractorOutput) {
	t.Helper()
	input := testCodeSourceInput(requestID)
	sourceCtx, err := buildManualSourceContext(input)
	if err != nil {
		t.Fatalf("buildManualSourceContext() error = %v", err)
	}
	runner, err := NewGoParserExtractorRunner(GoParserExtractorConfig{
		RepoID:    input.OriginMetadata["repo_id"],
		CommitSHA: input.OriginMetadata["commit_sha"],
		Path:      input.OriginMetadata["path"],
	})
	if err != nil {
		t.Fatalf("NewGoParserExtractorRunner() error = %v", err)
	}
	data, err := runner.Run(context.Background(), sourceCtx.extractorInput())
	if err != nil {
		t.Fatalf("runner.Run() error = %v", err)
	}
	output, err := decodeTrustedExtractorOutput(data)
	if err != nil {
		t.Fatalf("decodeTrustedExtractorOutput() error = %v", err)
	}
	ctx, err := buildAttemptContextFromSourceWithDefinition(sourceCtx, requestID, 1, runner.ExtractorDefinition())
	if err != nil {
		t.Fatalf("buildAttemptContextFromSourceWithDefinition() error = %v", err)
	}
	return ctx, output
}

func testCodeSourceInput(requestID string) ManualTextInput {
	return ManualTextInput{
		SourceSystem:  SourceSystemCodeFile,
		SourceID:      "repo:ahe-wrap:internal/refund/service.go",
		SourceVersion: "abc123",
		Raw:           []byte("package refund\n\nfunc Build() {}\n"),
		OriginMetadata: map[string]string{
			"repo_id":    "ahe-wrap",
			"commit_sha": "abc123",
			"path":       "internal/refund/service.go",
		},
		RequestID:     requestID,
		AttemptNumber: 1,
	}
}

func cloneExtractorOutput(output FrozenExtractorOutput) FrozenExtractorOutput {
	clone := FrozenExtractorOutput{Proposals: make([]ExtractorProposalOutput, len(output.Proposals))}
	if output.RepositoryGoplsCoverage != nil {
		coverage := *output.RepositoryGoplsCoverage
		clone.RepositoryGoplsCoverage = &coverage
	}
	for i, proposal := range output.Proposals {
		clone.Proposals[i] = proposal
		clone.Proposals[i].EvidenceRefs = append([]string(nil), proposal.EvidenceRefs...)
		if proposal.CodeFact != nil {
			fact := *proposal.CodeFact
			clone.Proposals[i].CodeFact = &fact
		}
		if proposal.CodeRelation != nil {
			relation := *proposal.CodeRelation
			clone.Proposals[i].CodeRelation = &relation
		}
	}
	return clone
}
