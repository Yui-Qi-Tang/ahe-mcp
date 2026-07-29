package evidenceingestion

import "testing"

func TestRepositoryGoSyntaxCountsIgnoreBlankIdentifierDeclaration(t *testing.T) {
	input := repositoryDeltaTestInput(t, "b", map[string]string{
		"sample.go": "package sample\n\ntype Runner interface{}\ntype implementation struct{}\nvar _ Runner = (*implementation)(nil)\n",
	})
	counts, err := repositoryGoSyntaxCounts(input)
	if err != nil {
		t.Fatalf("repositoryGoSyntaxCounts() error = %v", err)
	}
	if counts.declarations != 2 {
		t.Fatalf("grounded declarations = %d, want 2 addressable declarations", counts.declarations)
	}
}

func TestRepositoryGoplsCoverageReconcilesWithoutDuplicateProposals(t *testing.T) {
	input, proposal := repositoryCodeDefinitionTestFixture(t)
	output := repositoryGoplsTestOutput(t, input, []ExtractorProposalOutput{proposal}, []ExtractorProposalOutput{proposal})
	coverage := output.RepositoryGoplsCoverage
	if coverage == nil {
		t.Fatal("repository gopls coverage is nil")
	}
	if coverage.SupportedCrossFileDefinitionCount != 1 || coverage.SupportedCrossFileReferenceCount != 1 || coverage.CorroboratedRelationCount != 1 {
		t.Fatalf("reconciled coverage = %+v", coverage)
	}
	if coverage.DefinitionOnlyRelationCount != 0 || coverage.ReferenceOnlyRelationCount != 0 || coverage.EmittedRelationCount != 1 {
		t.Fatalf("deduplicated coverage = %+v", coverage)
	}
	if coverage.SupportedPackageFactCount != len(input.Files) || coverage.EmittedPackageFactCount != len(input.Files) || len(output.Proposals) != len(input.Files)+1 {
		t.Fatalf("package/relation proposals = coverage %+v proposals %d", coverage, len(output.Proposals))
	}
}

func TestRepositoryGoplsCoverageAllowsUnselectedDefinitionDiagnostics(t *testing.T) {
	input, proposal := repositoryCodeDefinitionTestFixture(t)
	output := repositoryGoplsTestOutput(t, input, []ExtractorProposalOutput{proposal}, []ExtractorProposalOutput{proposal})
	coverage := output.RepositoryGoplsCoverage
	coverage.DefinitionResultLocationCount++
	coverage.DefinitionUnselectedLocationCount++
	coverage.ReferenceResultLocationCount++
	coverage.ReferenceUnselectedLocationCount++

	batch, err := materializeRepositoryGoplsTestOutput(t, input, output)
	if err != nil {
		t.Fatalf("materializeRepositoryGoplsTestOutput() error = %v", err)
	}
	if len(batch.Occurrences) != len(input.Files)+1 || batch.RepositoryGoplsCoverage == nil || batch.RepositoryGoplsCoverage.DefinitionUnselectedLocationCount != 1 || batch.RepositoryGoplsCoverage.ReferenceUnselectedLocationCount != 1 {
		t.Fatalf("unselected definition diagnostics = %+v", batch)
	}
}

func TestRepositoryGoplsCoverageAllowsAmbiguousRelationDiagnostics(t *testing.T) {
	input, proposal := repositoryCodeDefinitionTestFixture(t)
	output := repositoryGoplsTestOutput(
		t,
		input,
		[]ExtractorProposalOutput{proposal},
		[]ExtractorProposalOutput{proposal},
	)
	coverage := output.RepositoryGoplsCoverage
	coverage.DefinitionAmbiguousUsageCount = 1
	coverage.DefinitionResultLocationCount += 2
	coverage.ReferenceAmbiguousUsageCount = 1
	coverage.ReferenceResultLocationCount += 2

	batch, err := materializeRepositoryGoplsTestOutput(t, input, output)
	if err != nil {
		t.Fatalf("materializeRepositoryGoplsTestOutput() error = %v", err)
	}
	if batch.RepositoryGoplsCoverage == nil ||
		batch.RepositoryGoplsCoverage.DefinitionAmbiguousUsageCount != 1 ||
		batch.RepositoryGoplsCoverage.ReferenceAmbiguousUsageCount != 1 {
		t.Fatalf("ambiguous relation diagnostics = %+v", batch.RepositoryGoplsCoverage)
	}
}

func TestRepositoryGoplsCoverageAllowsUnselectedCallDiagnostics(t *testing.T) {
	input, proposal := repositoryCodeCallTestFixture(t)
	output := repositoryGoplsTestOutputWithCalls(t, input, []ExtractorProposalOutput{proposal}, nil, []ExtractorProposalOutput{proposal})
	coverage := output.RepositoryGoplsCoverage
	coverage.OutgoingCallResultCount++
	coverage.OutgoingCallSiteCount++
	coverage.OutgoingUnselectedCallResultCount++
	coverage.OutgoingUnselectedCallSiteCount++
	coverage.IncomingCallResultCount++
	coverage.IncomingCallSiteCount++
	coverage.IncomingUnselectedCallResultCount++
	coverage.IncomingUnselectedCallSiteCount++

	batch, err := materializeRepositoryGoplsTestOutput(t, input, output)
	if err != nil {
		t.Fatalf("materializeRepositoryGoplsTestOutput() error = %v", err)
	}
	if len(batch.Occurrences) != len(input.Files)+1 || batch.RepositoryGoplsCoverage == nil || batch.RepositoryGoplsCoverage.OutgoingUnselectedCallResultCount != 1 || batch.RepositoryGoplsCoverage.IncomingUnselectedCallResultCount != 1 {
		t.Fatalf("unselected call diagnostics = %+v", batch)
	}
}

func TestMaterializeRepositoryGoplsCoverageRejectsTampering(t *testing.T) {
	input, proposal := repositoryCodeDefinitionTestFixture(t)
	tests := []struct {
		name   string
		mutate func(*RepositoryGoplsCoverage)
	}{
		{
			name: "schema",
			mutate: func(coverage *RepositoryGoplsCoverage) {
				coverage.SchemaVersion = "changed"
			},
		},
		{
			name: "selected files",
			mutate: func(coverage *RepositoryGoplsCoverage) {
				coverage.SelectedFileCount++
			},
		},
		{
			name: "package completed requests",
			mutate: func(coverage *RepositoryGoplsCoverage) {
				coverage.PackageCompletedRequestCount--
			},
		},
		{
			name: "emitted package facts",
			mutate: func(coverage *RepositoryGoplsCoverage) {
				coverage.EmittedPackageFactCount--
			},
		},
		{
			name: "definition requests",
			mutate: func(coverage *RepositoryGoplsCoverage) {
				coverage.DefinitionRequestCount++
			},
		},
		{
			name: "definition completed requests",
			mutate: func(coverage *RepositoryGoplsCoverage) {
				coverage.DefinitionCompletedRequestCount--
			},
		},
		{
			name: "definition unselected locations",
			mutate: func(coverage *RepositoryGoplsCoverage) {
				coverage.DefinitionUnselectedLocationCount++
			},
		},
		{
			name: "definition self locations",
			mutate: func(coverage *RepositoryGoplsCoverage) {
				coverage.DefinitionSelfDeclarationLocationCount++
			},
		},
		{
			name: "definition ambiguous usages",
			mutate: func(coverage *RepositoryGoplsCoverage) {
				coverage.DefinitionAmbiguousUsageCount++
			},
		},
		{
			name: "definition location partition overflow",
			mutate: func(coverage *RepositoryGoplsCoverage) {
				maxInt := int(^uint(0) >> 1)
				coverage.DefinitionResultLocationCount = 0
				coverage.DefinitionUnselectedLocationCount = maxInt
				coverage.DefinitionSelfDeclarationLocationCount = maxInt
			},
		},
		{
			name: "failure stage",
			mutate: func(coverage *RepositoryGoplsCoverage) {
				coverage.FailureStage = repositoryGoplsStageDefinition
			},
		},
		{
			name: "reconciliation",
			mutate: func(coverage *RepositoryGoplsCoverage) {
				coverage.CorroboratedRelationCount++
			},
		},
		{
			name: "same-file definitions",
			mutate: func(coverage *RepositoryGoplsCoverage) {
				coverage.SupportedSameFileDefinitionCount++
			},
		},
		{
			name: "reference unselected locations",
			mutate: func(coverage *RepositoryGoplsCoverage) {
				coverage.ReferenceUnselectedLocationCount++
			},
		},
		{
			name: "reference self locations",
			mutate: func(coverage *RepositoryGoplsCoverage) {
				coverage.ReferenceSelfDeclarationLocationCount++
			},
		},
		{
			name: "reference ambiguous usages",
			mutate: func(coverage *RepositoryGoplsCoverage) {
				coverage.ReferenceAmbiguousUsageCount++
			},
		},
		{
			name: "reference location partition overflow",
			mutate: func(coverage *RepositoryGoplsCoverage) {
				maxInt := int(^uint(0) >> 1)
				coverage.ReferenceResultLocationCount = 0
				coverage.ReferenceUnselectedLocationCount = maxInt
				coverage.ReferenceSelfDeclarationLocationCount = maxInt
			},
		},
		{
			name: "emitted relations",
			mutate: func(coverage *RepositoryGoplsCoverage) {
				coverage.EmittedRelationCount++
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			output := repositoryGoplsTestOutput(t, input, []ExtractorProposalOutput{proposal}, []ExtractorProposalOutput{proposal})
			test.mutate(output.RepositoryGoplsCoverage)
			_, err := materializeRepositoryGoplsTestOutput(t, input, output)
			assertKind(t, err, ErrorInvalidExtractorOutput)
		})
	}
}

func TestMaterializeRepositoryGoplsCallCoverageRejectsTampering(t *testing.T) {
	input, proposal := repositoryCodeCallTestFixture(t)
	tests := []struct {
		name   string
		mutate func(*RepositoryGoplsCoverage)
	}{
		{
			name: "prepare requests",
			mutate: func(coverage *RepositoryGoplsCoverage) {
				coverage.PrepareCallHierarchyRequestCount++
			},
		},
		{
			name: "outgoing completed requests",
			mutate: func(coverage *RepositoryGoplsCoverage) {
				coverage.OutgoingCallCompletedRequestCount--
			},
		},
		{
			name: "outgoing call sites",
			mutate: func(coverage *RepositoryGoplsCoverage) {
				coverage.OutgoingCallSiteCount--
			},
		},
		{
			name: "outgoing unselected results",
			mutate: func(coverage *RepositoryGoplsCoverage) {
				coverage.OutgoingUnselectedCallResultCount = coverage.OutgoingCallResultCount + 1
			},
		},
		{
			name: "outgoing unselected sites",
			mutate: func(coverage *RepositoryGoplsCoverage) {
				coverage.OutgoingUnselectedCallSiteCount++
			},
		},
		{
			name: "incoming completed requests",
			mutate: func(coverage *RepositoryGoplsCoverage) {
				coverage.IncomingCallCompletedRequestCount--
			},
		},
		{
			name: "incoming unselected results",
			mutate: func(coverage *RepositoryGoplsCoverage) {
				coverage.IncomingUnselectedCallResultCount = coverage.IncomingCallResultCount + 1
			},
		},
		{
			name: "incoming unselected sites",
			mutate: func(coverage *RepositoryGoplsCoverage) {
				coverage.IncomingUnselectedCallSiteCount++
			},
		},
		{
			name: "supported incoming calls",
			mutate: func(coverage *RepositoryGoplsCoverage) {
				coverage.SupportedIncomingCrossFileCallCount++
			},
		},
		{
			name: "supported incoming same-file calls",
			mutate: func(coverage *RepositoryGoplsCoverage) {
				coverage.SupportedIncomingSameFileCallCount++
			},
		},
		{
			name: "call reconciliation",
			mutate: func(coverage *RepositoryGoplsCoverage) {
				coverage.CorroboratedCallRelationCount++
			},
		},
		{
			name: "supported calls",
			mutate: func(coverage *RepositoryGoplsCoverage) {
				coverage.SupportedCrossFileCallCount++
			},
		},
		{
			name: "supported same-file calls",
			mutate: func(coverage *RepositoryGoplsCoverage) {
				coverage.SupportedSameFileCallCount++
			},
		},
		{
			name: "emitted calls",
			mutate: func(coverage *RepositoryGoplsCoverage) {
				coverage.EmittedCallRelationCount++
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			output := repositoryGoplsTestOutputWithCalls(t, input, []ExtractorProposalOutput{proposal}, nil, []ExtractorProposalOutput{proposal})
			test.mutate(output.RepositoryGoplsCoverage)
			_, err := materializeRepositoryGoplsTestOutput(t, input, output)
			assertKind(t, err, ErrorInvalidExtractorOutput)
		})
	}
}

func TestRepositoryGoplsIncomingOnlyCallRemainsDiagnostic(t *testing.T) {
	input, proposal := repositoryCodeCallTestFixture(t)
	output := repositoryGoplsTestOutputWithCallCoverage(t, input, nil, nil, nil, []ExtractorProposalOutput{proposal})
	coverage := output.RepositoryGoplsCoverage
	if coverage == nil || coverage.SupportedCrossFileCallCount != 0 || coverage.SupportedIncomingCrossFileCallCount != 1 || coverage.IncomingOnlyCallRelationCount != 1 || coverage.CorroboratedCallRelationCount != 0 || coverage.EmittedCallRelationCount != 0 {
		t.Fatalf("incoming-only coverage = %+v", coverage)
	}
	batch, err := materializeRepositoryGoplsTestOutput(t, input, output)
	if err != nil {
		t.Fatalf("materializeRepositoryGoplsTestOutput() error = %v", err)
	}
	if len(batch.Occurrences) != len(input.Files) {
		t.Fatalf("incoming-only occurrence count = %d, want package facts only (%d)", len(batch.Occurrences), len(input.Files))
	}
}

func TestRepositoryGoplsOutgoingOnlyCallRemainsPrimary(t *testing.T) {
	input, proposal := repositoryCodeCallTestFixture(t)
	output := repositoryGoplsTestOutputWithCallCoverage(t, input, []ExtractorProposalOutput{proposal}, nil, []ExtractorProposalOutput{proposal}, nil)
	coverage := output.RepositoryGoplsCoverage
	if coverage == nil || coverage.SupportedCrossFileCallCount != 1 || coverage.SupportedIncomingCrossFileCallCount != 0 || coverage.OutgoingOnlyCallRelationCount != 1 || coverage.CorroboratedCallRelationCount != 0 || coverage.EmittedCallRelationCount != 1 {
		t.Fatalf("outgoing-only coverage = %+v", coverage)
	}
	batch, err := materializeRepositoryGoplsTestOutput(t, input, output)
	if err != nil {
		t.Fatalf("materializeRepositoryGoplsTestOutput() error = %v", err)
	}
	if len(batch.Occurrences) != len(input.Files)+1 {
		t.Fatalf("outgoing-only occurrence count = %d, want %d", len(batch.Occurrences), len(input.Files)+1)
	}
}

func TestMaterializeRepositoryGoplsCoverageRequiresPackageFactPerFile(t *testing.T) {
	input, proposal := repositoryCodeDefinitionTestFixture(t)
	output := repositoryGoplsTestOutput(t, input, []ExtractorProposalOutput{proposal}, nil)
	filtered := make([]ExtractorProposalOutput, 0, len(output.Proposals)-1)
	removed := false
	for _, candidate := range output.Proposals {
		if !removed && candidate.CodeFact != nil && candidate.CodeFact.FactKind == CodeFactKindPackage {
			removed = true
			continue
		}
		filtered = append(filtered, candidate)
	}
	output.Proposals = filtered
	_, err := materializeRepositoryGoplsTestOutput(t, input, output)
	assertKind(t, err, ErrorInvalidExtractorOutput)
}

func TestMaterializeRepositoryGoplsCoverageRejectsDuplicatePackagePath(t *testing.T) {
	input, proposal := repositoryCodeDefinitionTestFixture(t)
	output := repositoryGoplsTestOutput(t, input, []ExtractorProposalOutput{proposal}, nil)
	packageIndexes := make([]int, 0, len(input.Files))
	for index, candidate := range output.Proposals {
		if candidate.CodeFact != nil && candidate.CodeFact.FactKind == CodeFactKindPackage {
			packageIndexes = append(packageIndexes, index)
		}
	}
	if len(packageIndexes) != 2 {
		t.Fatalf("package proposal indexes = %v, want 2", packageIndexes)
	}
	duplicate := cloneExtractorOutput(FrozenExtractorOutput{Proposals: []ExtractorProposalOutput{output.Proposals[packageIndexes[0]]}}).Proposals[0]
	duplicate.ProposalLocalID += "-duplicate"
	output.Proposals[packageIndexes[1]] = duplicate

	_, err := materializeRepositoryGoplsTestOutput(t, input, output)
	assertKind(t, err, ErrorInvalidExtractorOutput)
}

func TestMaterializeRepositoryGoParserRejectsGoplsCoverage(t *testing.T) {
	input := repositoryParserTestInput(t)
	output, err := extractRepositoryGoParser(t.Context(), input)
	if err != nil {
		t.Fatalf("extractRepositoryGoParser() error = %v", err)
	}
	coverage := RepositoryGoplsCoverage{SchemaVersion: RepositoryGoplsCoverageSchemaV1}
	output.RepositoryGoplsCoverage = &coverage

	definition, err := buildExtractorDefinition(repositoryGoParserExtractorDefinition())
	if err != nil {
		t.Fatalf("buildExtractorDefinition() error = %v", err)
	}
	run := ExtractionRun{ID: "run:parser-coverage", ExtractorDefinitionID: definition.ID, RepositorySnapshotID: input.RepositorySnapshot.ID}
	attemptCtx, err := buildRepositoryAttemptContext(input, definition, run, 1)
	if err != nil {
		t.Fatalf("buildRepositoryAttemptContext() error = %v", err)
	}
	_, err = materializeRepositoryBatch(attemptCtx, output)
	assertKind(t, err, ErrorInvalidExtractorOutput)
}

func materializeRepositoryGoplsTestOutput(t *testing.T, input RepositoryExtractorInput, output FrozenExtractorOutput) (MaterializedBatch, error) {
	t.Helper()
	definition, err := buildExtractorDefinition((&repositoryGoplsRunner{goplsVersion: "v-test"}).ExtractorDefinition())
	if err != nil {
		t.Fatalf("buildExtractorDefinition() error = %v", err)
	}
	run := ExtractionRun{ID: "run:gopls-coverage", ExtractorDefinitionID: definition.ID, RepositorySnapshotID: input.RepositorySnapshot.ID}
	attemptCtx, err := buildRepositoryAttemptContext(input, definition, run, 1)
	if err != nil {
		t.Fatalf("buildRepositoryAttemptContext() error = %v", err)
	}
	return materializeRepositoryBatch(attemptCtx, output)
}
