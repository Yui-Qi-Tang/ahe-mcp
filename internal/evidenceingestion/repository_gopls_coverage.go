package evidenceingestion

const (
	repositoryGoplsCoverageScopeV1 = "selected_snapshot_go_files_definition_and_reference_requests"
	repositoryGoplsCoverageScopeV2 = "selected_snapshot_go_files_definition_reference_and_outgoing_call_requests"
	repositoryGoplsCoverageScopeV3 = "selected_snapshot_go_files_semantic_requests_with_partial_progress"
	repositoryGoplsCoverageScopeV4 = "selected_snapshot_go_files_package_and_semantic_requests_with_partial_progress"
	repositoryGoplsCoverageScopeV5 = "selected_snapshot_go_files_package_semantic_and_bidirectional_call_requests_with_partial_progress"
	repositoryGoplsCoverageScopeV6 = "selected_snapshot_go_files_package_semantic_and_bidirectional_call_requests_with_relation_locality"
	repositoryGoplsCoverageScopeV7 = "selected_snapshot_go_files_package_semantic_and_bidirectional_call_requests_with_relation_locality_and_unselected_result_diagnostics"
	repositoryGoplsCoverageScopeV8 = "selected_snapshot_go_files_package_semantic_and_bidirectional_call_requests_with_relation_locality_unselected_and_ambiguous_result_diagnostics"
	missingSemanticResultUnknown   = "unknown_not_negative"

	repositoryGoplsStageSyntaxInventory         = "syntax_inventory"
	repositoryGoplsStageSessionInitialize       = "session_initialize"
	repositoryGoplsStageOpenDocument            = "textDocument/didOpen"
	repositoryGoplsStageDocumentSymbol          = "textDocument/documentSymbol"
	repositoryGoplsStagePackageSymbols          = "workspace/executeCommand:gopls.package_symbols"
	repositoryGoplsStageDefinition              = "textDocument/definition"
	repositoryGoplsStageReferences              = "textDocument/references"
	repositoryGoplsStagePrepareCallHierarchy    = "textDocument/prepareCallHierarchy"
	repositoryGoplsStageOutgoingCalls           = "callHierarchy/outgoingCalls"
	repositoryGoplsStageIncomingCalls           = "callHierarchy/incomingCalls"
	repositoryGoplsStageCloseDocument           = "textDocument/didClose"
	repositoryGoplsStageSessionShutdown         = "session_shutdown"
	repositoryGoplsStageDocumentSymbolGrounding = "document_symbol_grounding"
	repositoryGoplsStagePackageGrounding        = "package_grounding"
	repositoryGoplsStageDefinitionGrounding     = "definition_grounding"
	repositoryGoplsStageReferenceGrounding      = "reference_grounding"
	repositoryGoplsStageCallGrounding           = "call_grounding"
	repositoryGoplsStageIncomingCallGrounding   = "incoming_call_grounding"
)

func buildRepositoryGoplsCoverage(
	input RepositoryExtractorInput,
	analysis repositoryGoplsAnalysis,
	packages []ExtractorProposalOutput,
	definitions []ExtractorProposalOutput,
	references []ExtractorProposalOutput,
	calls []ExtractorProposalOutput,
	incomingCalls []ExtractorProposalOutput,
) RepositoryGoplsCoverage {
	definitionIDs := make(map[string]struct{}, len(definitions))
	for _, proposal := range definitions {
		definitionIDs[proposal.ProposalLocalID] = struct{}{}
	}
	corroborated := 0
	for _, proposal := range references {
		if _, ok := definitionIDs[proposal.ProposalLocalID]; ok {
			corroborated++
		}
	}
	outgoingCallIDs := make(map[string]struct{}, len(calls))
	for _, proposal := range calls {
		outgoingCallIDs[proposal.ProposalLocalID] = struct{}{}
	}
	corroboratedCalls := 0
	for _, proposal := range incomingCalls {
		if _, ok := outgoingCallIDs[proposal.ProposalLocalID]; ok {
			corroboratedCalls++
		}
	}
	crossFileDefinitions, sameFileDefinitions := repositoryGoplsRelationLocalityCounts(definitions)
	crossFileReferences, sameFileReferences := repositoryGoplsRelationLocalityCounts(references)
	crossFileCalls, sameFileCalls := repositoryGoplsRelationLocalityCounts(calls)
	incomingCrossFileCalls, incomingSameFileCalls := repositoryGoplsRelationLocalityCounts(incomingCalls)
	coverage := RepositoryGoplsCoverage{
		SchemaVersion:                             RepositoryGoplsCoverageSchemaV8,
		Scope:                                     repositoryGoplsCoverageScopeV8,
		FailureStage:                              analysis.failureStage,
		SelectedFileCount:                         len(input.Files),
		DocumentSymbolRequestCount:                analysis.documentSymbolRequestCount,
		DocumentSymbolFileCount:                   len(analysis.symbolsByPath),
		ParsedPackageClauseCount:                  analysis.syntaxCounts.packages,
		PackageRequestCount:                       analysis.packages.requestCount,
		PackageCompletedRequestCount:              analysis.packages.completedRequestCount,
		PackageResultFileCount:                    analysis.packages.resultFileCount,
		SupportedPackageFactCount:                 len(packages),
		EmittedPackageFactCount:                   len(packages),
		ParsedIdentifierCount:                     analysis.syntaxCounts.identifiers,
		GroundedDeclarationCount:                  analysis.syntaxCounts.declarations,
		DefinitionRequestCount:                    analysis.definitions.requestCount,
		DefinitionCompletedRequestCount:           analysis.definitions.completedRequestCount,
		DefinitionResultLocationCount:             analysis.definitions.locationCount,
		DefinitionUnselectedLocationCount:         analysis.definitions.unselectedLocationCount,
		DefinitionSelfDeclarationLocationCount:    analysis.definitions.selfDeclarationLocationCount,
		DefinitionAmbiguousUsageCount:             analysis.definitions.ambiguousUsageCount,
		SupportedCrossFileDefinitionCount:         crossFileDefinitions,
		SupportedSameFileDefinitionCount:          sameFileDefinitions,
		ReferenceRequestCount:                     analysis.references.requestCount,
		ReferenceCompletedRequestCount:            analysis.references.completedRequestCount,
		ReferenceResultLocationCount:              analysis.references.locationCount,
		ReferenceUnselectedLocationCount:          analysis.references.unselectedLocationCount,
		ReferenceSelfDeclarationLocationCount:     analysis.references.selfDeclarationLocationCount,
		ReferenceAmbiguousUsageCount:              analysis.references.ambiguousUsageCount,
		SupportedCrossFileReferenceCount:          crossFileReferences,
		SupportedSameFileReferenceCount:           sameFileReferences,
		CorroboratedRelationCount:                 corroborated,
		DefinitionOnlyRelationCount:               len(definitions) - corroborated,
		ReferenceOnlyRelationCount:                len(references) - corroborated,
		CallableDeclarationCount:                  analysis.syntaxCounts.callables,
		PrepareCallHierarchyRequestCount:          analysis.calls.prepareRequestCount,
		PrepareCallHierarchyCompletedRequestCount: analysis.calls.prepareCompletedRequestCount,
		PreparedCallHierarchyItemCount:            analysis.calls.preparedItemCount,
		OutgoingCallRequestCount:                  analysis.calls.outgoingRequestCount,
		OutgoingCallCompletedRequestCount:         analysis.calls.outgoingCompletedRequestCount,
		OutgoingCallResultCount:                   analysis.calls.outgoingCallResultCount,
		OutgoingCallSiteCount:                     analysis.calls.outgoingCallSiteCount,
		OutgoingUnselectedCallResultCount:         analysis.calls.outgoingUnselectedResultCount,
		OutgoingUnselectedCallSiteCount:           analysis.calls.outgoingUnselectedSiteCount,
		SupportedCrossFileCallCount:               crossFileCalls,
		SupportedSameFileCallCount:                sameFileCalls,
		IncomingCallRequestCount:                  analysis.calls.incomingRequestCount,
		IncomingCallCompletedRequestCount:         analysis.calls.incomingCompletedRequestCount,
		IncomingCallResultCount:                   analysis.calls.incomingCallResultCount,
		IncomingCallSiteCount:                     analysis.calls.incomingCallSiteCount,
		IncomingUnselectedCallResultCount:         analysis.calls.incomingUnselectedResultCount,
		IncomingUnselectedCallSiteCount:           analysis.calls.incomingUnselectedSiteCount,
		SupportedIncomingCrossFileCallCount:       incomingCrossFileCalls,
		SupportedIncomingSameFileCallCount:        incomingSameFileCalls,
		CorroboratedCallRelationCount:             corroboratedCalls,
		OutgoingOnlyCallRelationCount:             len(calls) - corroboratedCalls,
		IncomingOnlyCallRelationCount:             len(incomingCalls) - corroboratedCalls,
		EmittedCallRelationCount:                  len(calls),
		EmittedRelationCount:                      len(definitions) + len(calls),
		MissingResultSemantics:                    missingSemanticResultUnknown,
	}
	coverage.RequestCoverageComplete = repositoryGoplsRequestsComplete(coverage)
	return coverage
}

func repositoryGoplsRelationLocalityCounts(proposals []ExtractorProposalOutput) (crossFile int, sameFile int) {
	for _, proposal := range proposals {
		if proposal.CodeRelation == nil {
			continue
		}
		if proposal.CodeRelation.Usage.Path == proposal.CodeRelation.Target.Path {
			sameFile++
		} else {
			crossFile++
		}
	}
	return crossFile, sameFile
}

func resolveRepositoryGoplsCoverage(
	input RepositoryExtractorInput,
	definition ExtractorDefinition,
	output FrozenExtractorOutput,
) (*RepositoryGoplsCoverage, error) {
	if output.DocumentSectionCoverage != nil {
		return nil, newDomainError(
			ErrorInvalidExtractorOutput,
			"document section coverage is not supported for repository extraction",
		)
	}
	expectsCoverage := definition.Name == ExtractorRepositoryGoplsCodeFact && definition.Version == ExtractorRepositoryGoplsCodeFactVersion
	if !expectsCoverage {
		if output.RepositoryGoplsCoverage != nil {
			return nil, newDomainError(ErrorInvalidExtractorOutput, "extractor %s/%s does not support repository gopls coverage", definition.Name, definition.Version)
		}
		return nil, nil
	}
	if output.RepositoryGoplsCoverage == nil {
		return nil, newDomainError(ErrorInvalidExtractorOutput, "repository gopls output requires coverage summary")
	}

	coverage := *output.RepositoryGoplsCoverage
	if coverage.SchemaVersion != RepositoryGoplsCoverageSchemaV8 {
		return nil, newDomainError(ErrorInvalidExtractorOutput, "repository gopls coverage schema_version %q is not supported", coverage.SchemaVersion)
	}
	if coverage.Scope != repositoryGoplsCoverageScopeV8 {
		return nil, newDomainError(ErrorInvalidExtractorOutput, "repository gopls coverage scope %q is not supported", coverage.Scope)
	}
	if coverage.MissingResultSemantics != missingSemanticResultUnknown {
		return nil, newDomainError(ErrorInvalidExtractorOutput, "repository gopls missing result semantics %q is not supported", coverage.MissingResultSemantics)
	}
	if err := validateRepositoryGoplsCoverageCounts(coverage); err != nil {
		return nil, err
	}
	if !coverage.RequestCoverageComplete {
		return nil, newDomainError(ErrorInvalidExtractorOutput, "successful repository gopls output must have complete request coverage")
	}
	if coverage.FailureStage != "" {
		return nil, newDomainError(ErrorInvalidExtractorOutput, "successful repository gopls output cannot include failure_stage %q", coverage.FailureStage)
	}

	syntaxCounts, err := repositoryGoSyntaxCounts(input)
	if err != nil {
		return nil, err
	}
	crossFileDefinitionCount := 0
	sameFileDefinitionCount := 0
	crossFileCallCount := 0
	sameFileCallCount := 0
	packageCount := 0
	selectedPackagePaths := make(map[string]struct{}, len(input.Files))
	for _, file := range input.Files {
		selectedPackagePaths[file.FileSnapshot.Path] = struct{}{}
	}
	observedPackagePaths := make(map[string]struct{}, len(input.Files))
	for _, proposal := range output.Proposals {
		if proposal.CodeFact != nil {
			if proposal.CodeFact.FactKind == CodeFactKindPackage {
				path := proposal.CodeFact.Path
				if _, ok := selectedPackagePaths[path]; !ok {
					return nil, newDomainError(ErrorInvalidExtractorOutput, "repository gopls package fact references unselected path %q", path)
				}
				if _, ok := observedPackagePaths[path]; ok {
					return nil, newDomainError(ErrorInvalidExtractorOutput, "repository gopls output repeats package fact for %q", path)
				}
				observedPackagePaths[path] = struct{}{}
				packageCount++
			}
			continue
		}
		if proposal.CodeRelation == nil {
			continue
		}
		switch proposal.CodeRelation.RelationKind {
		case CodeRelationKindDefinition:
			if proposal.CodeRelation.Usage.Path == proposal.CodeRelation.Target.Path {
				sameFileDefinitionCount++
			} else {
				crossFileDefinitionCount++
			}
		case CodeRelationKindCall:
			if proposal.CodeRelation.Usage.Path == proposal.CodeRelation.Target.Path {
				sameFileCallCount++
			} else {
				crossFileCallCount++
			}
		}
	}
	if coverage.SelectedFileCount != len(input.Files) || coverage.DocumentSymbolRequestCount != len(input.Files) || coverage.DocumentSymbolFileCount != len(input.Files) {
		return nil, newDomainError(ErrorInvalidExtractorOutput, "repository gopls coverage file counts selected=%d symbol_requests=%d symbol_results=%d, want %d", coverage.SelectedFileCount, coverage.DocumentSymbolRequestCount, coverage.DocumentSymbolFileCount, len(input.Files))
	}
	if coverage.ParsedPackageClauseCount != len(input.Files) || coverage.PackageRequestCount != len(input.Files) || coverage.PackageCompletedRequestCount != len(input.Files) {
		return nil, newDomainError(ErrorInvalidExtractorOutput, "repository gopls coverage package/request counts %d/%d/%d, want %d", coverage.ParsedPackageClauseCount, coverage.PackageRequestCount, coverage.PackageCompletedRequestCount, len(input.Files))
	}
	if coverage.SupportedPackageFactCount != packageCount || coverage.EmittedPackageFactCount != packageCount || packageCount != len(selectedPackagePaths) {
		return nil, newDomainError(ErrorInvalidExtractorOutput, "repository gopls supported/emitted/output package fact counts %d/%d/%d, want %d", coverage.SupportedPackageFactCount, coverage.EmittedPackageFactCount, packageCount, len(input.Files))
	}
	if coverage.ParsedIdentifierCount != syntaxCounts.identifiers || coverage.DefinitionRequestCount != syntaxCounts.identifiers || coverage.DefinitionCompletedRequestCount != syntaxCounts.identifiers {
		return nil, newDomainError(ErrorInvalidExtractorOutput, "repository gopls coverage identifier/definition request counts %d/%d/%d, want %d", coverage.ParsedIdentifierCount, coverage.DefinitionRequestCount, coverage.DefinitionCompletedRequestCount, syntaxCounts.identifiers)
	}
	if coverage.GroundedDeclarationCount != syntaxCounts.declarations || coverage.ReferenceRequestCount != syntaxCounts.declarations || coverage.ReferenceCompletedRequestCount != syntaxCounts.declarations {
		return nil, newDomainError(ErrorInvalidExtractorOutput, "repository gopls coverage declaration/reference request counts %d/%d/%d, want %d", coverage.GroundedDeclarationCount, coverage.ReferenceRequestCount, coverage.ReferenceCompletedRequestCount, syntaxCounts.declarations)
	}
	if coverage.CallableDeclarationCount != syntaxCounts.callables || coverage.PrepareCallHierarchyRequestCount != syntaxCounts.callables || coverage.PrepareCallHierarchyCompletedRequestCount != syntaxCounts.callables {
		return nil, newDomainError(ErrorInvalidExtractorOutput, "repository gopls coverage callable/prepare request counts %d/%d/%d, want %d", coverage.CallableDeclarationCount, coverage.PrepareCallHierarchyRequestCount, coverage.PrepareCallHierarchyCompletedRequestCount, syntaxCounts.callables)
	}
	if coverage.OutgoingCallCompletedRequestCount != coverage.OutgoingCallRequestCount {
		return nil, newDomainError(ErrorInvalidExtractorOutput, "repository gopls outgoing completed request count %d, want %d", coverage.OutgoingCallCompletedRequestCount, coverage.OutgoingCallRequestCount)
	}
	if coverage.IncomingCallCompletedRequestCount != coverage.IncomingCallRequestCount {
		return nil, newDomainError(ErrorInvalidExtractorOutput, "repository gopls incoming completed request count %d, want %d", coverage.IncomingCallCompletedRequestCount, coverage.IncomingCallRequestCount)
	}
	if coverage.SupportedCrossFileDefinitionCount != crossFileDefinitionCount || coverage.SupportedSameFileDefinitionCount != sameFileDefinitionCount {
		return nil, newDomainError(ErrorInvalidExtractorOutput, "repository gopls supported cross/same-file definition counts %d/%d, want %d/%d", coverage.SupportedCrossFileDefinitionCount, coverage.SupportedSameFileDefinitionCount, crossFileDefinitionCount, sameFileDefinitionCount)
	}
	callCount := crossFileCallCount + sameFileCallCount
	if coverage.SupportedCrossFileCallCount != crossFileCallCount || coverage.SupportedSameFileCallCount != sameFileCallCount || coverage.EmittedCallRelationCount != callCount {
		return nil, newDomainError(ErrorInvalidExtractorOutput, "repository gopls supported cross/same-file and emitted call counts %d/%d/%d, want %d/%d/%d", coverage.SupportedCrossFileCallCount, coverage.SupportedSameFileCallCount, coverage.EmittedCallRelationCount, crossFileCallCount, sameFileCallCount, callCount)
	}
	definitionCount := crossFileDefinitionCount + sameFileDefinitionCount
	if coverage.EmittedRelationCount != definitionCount+callCount {
		return nil, newDomainError(ErrorInvalidExtractorOutput, "repository gopls emitted relation count %d, want %d", coverage.EmittedRelationCount, definitionCount+callCount)
	}
	return &coverage, nil
}

func validateRepositoryGoplsCoverageCounts(coverage RepositoryGoplsCoverage) error {
	counts := []int{
		coverage.SelectedFileCount,
		coverage.DocumentSymbolRequestCount,
		coverage.DocumentSymbolFileCount,
		coverage.ParsedPackageClauseCount,
		coverage.PackageRequestCount,
		coverage.PackageCompletedRequestCount,
		coverage.PackageResultFileCount,
		coverage.SupportedPackageFactCount,
		coverage.EmittedPackageFactCount,
		coverage.ParsedIdentifierCount,
		coverage.GroundedDeclarationCount,
		coverage.DefinitionRequestCount,
		coverage.DefinitionCompletedRequestCount,
		coverage.DefinitionResultLocationCount,
		coverage.DefinitionUnselectedLocationCount,
		coverage.DefinitionSelfDeclarationLocationCount,
		coverage.DefinitionAmbiguousUsageCount,
		coverage.SupportedCrossFileDefinitionCount,
		coverage.SupportedSameFileDefinitionCount,
		coverage.ReferenceRequestCount,
		coverage.ReferenceCompletedRequestCount,
		coverage.ReferenceResultLocationCount,
		coverage.ReferenceUnselectedLocationCount,
		coverage.ReferenceSelfDeclarationLocationCount,
		coverage.ReferenceAmbiguousUsageCount,
		coverage.SupportedCrossFileReferenceCount,
		coverage.SupportedSameFileReferenceCount,
		coverage.CorroboratedRelationCount,
		coverage.DefinitionOnlyRelationCount,
		coverage.ReferenceOnlyRelationCount,
		coverage.CallableDeclarationCount,
		coverage.PrepareCallHierarchyRequestCount,
		coverage.PrepareCallHierarchyCompletedRequestCount,
		coverage.PreparedCallHierarchyItemCount,
		coverage.OutgoingCallRequestCount,
		coverage.OutgoingCallCompletedRequestCount,
		coverage.OutgoingCallResultCount,
		coverage.OutgoingCallSiteCount,
		coverage.OutgoingUnselectedCallResultCount,
		coverage.OutgoingUnselectedCallSiteCount,
		coverage.SupportedCrossFileCallCount,
		coverage.SupportedSameFileCallCount,
		coverage.IncomingCallRequestCount,
		coverage.IncomingCallCompletedRequestCount,
		coverage.IncomingCallResultCount,
		coverage.IncomingCallSiteCount,
		coverage.IncomingUnselectedCallResultCount,
		coverage.IncomingUnselectedCallSiteCount,
		coverage.SupportedIncomingCrossFileCallCount,
		coverage.SupportedIncomingSameFileCallCount,
		coverage.CorroboratedCallRelationCount,
		coverage.OutgoingOnlyCallRelationCount,
		coverage.IncomingOnlyCallRelationCount,
		coverage.EmittedCallRelationCount,
		coverage.EmittedRelationCount,
	}
	for _, count := range counts {
		if count < 0 {
			return newDomainError(ErrorInvalidExtractorOutput, "repository gopls coverage counts must be non-negative")
		}
	}
	supportedDefinitions := coverage.SupportedCrossFileDefinitionCount + coverage.SupportedSameFileDefinitionCount
	supportedReferences := coverage.SupportedCrossFileReferenceCount + coverage.SupportedSameFileReferenceCount
	supportedCalls := coverage.SupportedCrossFileCallCount + coverage.SupportedSameFileCallCount
	supportedIncomingCalls := coverage.SupportedIncomingCrossFileCallCount + coverage.SupportedIncomingSameFileCallCount
	if coverage.DefinitionUnselectedLocationCount > coverage.DefinitionResultLocationCount || coverage.DefinitionSelfDeclarationLocationCount > coverage.DefinitionResultLocationCount-coverage.DefinitionUnselectedLocationCount {
		return newDomainError(ErrorInvalidExtractorOutput, "repository gopls definition location coverage is inconsistent")
	}
	if coverage.ReferenceUnselectedLocationCount > coverage.ReferenceResultLocationCount || coverage.ReferenceSelfDeclarationLocationCount > coverage.ReferenceResultLocationCount-coverage.ReferenceUnselectedLocationCount {
		return newDomainError(ErrorInvalidExtractorOutput, "repository gopls reference location coverage is inconsistent")
	}
	if coverage.OutgoingUnselectedCallResultCount > coverage.OutgoingCallResultCount || coverage.OutgoingUnselectedCallSiteCount > coverage.OutgoingCallSiteCount || coverage.OutgoingUnselectedCallSiteCount > 0 && coverage.OutgoingUnselectedCallResultCount == 0 {
		return newDomainError(ErrorInvalidExtractorOutput, "repository gopls outgoing unselected call coverage is inconsistent")
	}
	if coverage.IncomingUnselectedCallResultCount > coverage.IncomingCallResultCount || coverage.IncomingUnselectedCallSiteCount > coverage.IncomingCallSiteCount || coverage.IncomingUnselectedCallSiteCount > 0 && coverage.IncomingUnselectedCallResultCount == 0 {
		return newDomainError(ErrorInvalidExtractorOutput, "repository gopls incoming unselected call coverage is inconsistent")
	}
	selectedDefinitionLocations := coverage.DefinitionResultLocationCount - coverage.DefinitionUnselectedLocationCount - coverage.DefinitionSelfDeclarationLocationCount
	selectedReferenceLocations := coverage.ReferenceResultLocationCount - coverage.ReferenceUnselectedLocationCount - coverage.ReferenceSelfDeclarationLocationCount
	selectedOutgoingCallSites := coverage.OutgoingCallSiteCount - coverage.OutgoingUnselectedCallSiteCount
	selectedIncomingCallSites := coverage.IncomingCallSiteCount - coverage.IncomingUnselectedCallSiteCount
	if selectedDefinitionLocations < supportedDefinitions {
		return newDomainError(ErrorInvalidExtractorOutput, "repository gopls selected definition locations cannot be smaller than supported definitions")
	}
	if selectedReferenceLocations < supportedReferences {
		return newDomainError(ErrorInvalidExtractorOutput, "repository gopls selected reference locations cannot be smaller than supported references")
	}
	if coverage.DefinitionAmbiguousUsageCount > coverage.DefinitionCompletedRequestCount ||
		selectedDefinitionLocations < supportedDefinitions+2*coverage.DefinitionAmbiguousUsageCount {
		return newDomainError(ErrorInvalidExtractorOutput, "repository gopls ambiguous definition coverage is inconsistent")
	}
	if coverage.ReferenceAmbiguousUsageCount > coverage.ReferenceCompletedRequestCount ||
		selectedReferenceLocations < supportedReferences+2*coverage.ReferenceAmbiguousUsageCount {
		return newDomainError(ErrorInvalidExtractorOutput, "repository gopls ambiguous reference coverage is inconsistent")
	}
	if coverage.CorroboratedRelationCount+coverage.DefinitionOnlyRelationCount != supportedDefinitions {
		return newDomainError(ErrorInvalidExtractorOutput, "repository gopls definition coverage reconciliation is inconsistent")
	}
	if coverage.CorroboratedRelationCount+coverage.ReferenceOnlyRelationCount != supportedReferences {
		return newDomainError(ErrorInvalidExtractorOutput, "repository gopls reference coverage reconciliation is inconsistent")
	}
	if coverage.DocumentSymbolFileCount > coverage.DocumentSymbolRequestCount || coverage.DocumentSymbolRequestCount > coverage.SelectedFileCount {
		return newDomainError(ErrorInvalidExtractorOutput, "repository gopls document-symbol request coverage is inconsistent")
	}
	if coverage.PackageCompletedRequestCount > coverage.PackageRequestCount || coverage.PackageRequestCount > coverage.ParsedPackageClauseCount || coverage.ParsedPackageClauseCount > coverage.SelectedFileCount {
		return newDomainError(ErrorInvalidExtractorOutput, "repository gopls package request coverage is inconsistent")
	}
	if coverage.PackageResultFileCount < coverage.PackageCompletedRequestCount || coverage.SupportedPackageFactCount > coverage.PackageCompletedRequestCount || coverage.EmittedPackageFactCount != coverage.SupportedPackageFactCount {
		return newDomainError(ErrorInvalidExtractorOutput, "repository gopls package result coverage is inconsistent")
	}
	if coverage.DefinitionCompletedRequestCount > coverage.DefinitionRequestCount || coverage.DefinitionRequestCount > coverage.ParsedIdentifierCount {
		return newDomainError(ErrorInvalidExtractorOutput, "repository gopls definition request coverage is inconsistent")
	}
	if coverage.ReferenceCompletedRequestCount > coverage.ReferenceRequestCount || coverage.ReferenceRequestCount > coverage.GroundedDeclarationCount {
		return newDomainError(ErrorInvalidExtractorOutput, "repository gopls reference request coverage is inconsistent")
	}
	if coverage.PrepareCallHierarchyCompletedRequestCount > coverage.PrepareCallHierarchyRequestCount || coverage.PrepareCallHierarchyRequestCount > coverage.CallableDeclarationCount || coverage.PreparedCallHierarchyItemCount > coverage.PrepareCallHierarchyCompletedRequestCount || coverage.OutgoingCallRequestCount != coverage.PreparedCallHierarchyItemCount {
		return newDomainError(ErrorInvalidExtractorOutput, "repository gopls call hierarchy request coverage is inconsistent")
	}
	if coverage.OutgoingCallCompletedRequestCount > coverage.OutgoingCallRequestCount {
		return newDomainError(ErrorInvalidExtractorOutput, "repository gopls outgoing call request coverage is inconsistent")
	}
	if coverage.IncomingCallRequestCount > coverage.OutgoingCallCompletedRequestCount || coverage.IncomingCallCompletedRequestCount > coverage.IncomingCallRequestCount {
		return newDomainError(ErrorInvalidExtractorOutput, "repository gopls incoming call request coverage is inconsistent")
	}
	if selectedOutgoingCallSites < supportedCalls {
		return newDomainError(ErrorInvalidExtractorOutput, "repository gopls selected outgoing call-site count cannot be smaller than supported calls")
	}
	if selectedIncomingCallSites < supportedIncomingCalls {
		return newDomainError(ErrorInvalidExtractorOutput, "repository gopls selected incoming call-site count cannot be smaller than supported calls")
	}
	if coverage.CorroboratedCallRelationCount+coverage.OutgoingOnlyCallRelationCount != supportedCalls || coverage.CorroboratedCallRelationCount+coverage.IncomingOnlyCallRelationCount != supportedIncomingCalls {
		return newDomainError(ErrorInvalidExtractorOutput, "repository gopls call coverage reconciliation is inconsistent")
	}
	if coverage.EmittedCallRelationCount != supportedCalls || coverage.EmittedRelationCount != supportedDefinitions+coverage.EmittedCallRelationCount {
		return newDomainError(ErrorInvalidExtractorOutput, "repository gopls emitted relation coverage is inconsistent")
	}
	complete := repositoryGoplsRequestsComplete(coverage)
	if coverage.RequestCoverageComplete != complete {
		return newDomainError(ErrorInvalidExtractorOutput, "repository gopls request_coverage_complete does not match request counters")
	}
	if !complete && coverage.FailureStage == "" {
		return newDomainError(ErrorInvalidExtractorOutput, "incomplete repository gopls request coverage requires failure_stage")
	}
	if coverage.FailureStage != "" && !supportedRepositoryGoplsFailureStage(coverage.FailureStage) {
		return newDomainError(ErrorInvalidExtractorOutput, "repository gopls failure_stage %q is not supported", coverage.FailureStage)
	}
	return nil
}

func repositoryGoplsRequestsComplete(coverage RepositoryGoplsCoverage) bool {
	return coverage.DocumentSymbolRequestCount == coverage.SelectedFileCount &&
		coverage.DocumentSymbolFileCount == coverage.SelectedFileCount &&
		coverage.PackageRequestCount == coverage.ParsedPackageClauseCount &&
		coverage.PackageCompletedRequestCount == coverage.ParsedPackageClauseCount &&
		coverage.DefinitionRequestCount == coverage.ParsedIdentifierCount &&
		coverage.DefinitionCompletedRequestCount == coverage.ParsedIdentifierCount &&
		coverage.ReferenceRequestCount == coverage.GroundedDeclarationCount &&
		coverage.ReferenceCompletedRequestCount == coverage.GroundedDeclarationCount &&
		coverage.PrepareCallHierarchyRequestCount == coverage.CallableDeclarationCount &&
		coverage.PrepareCallHierarchyCompletedRequestCount == coverage.CallableDeclarationCount &&
		coverage.OutgoingCallRequestCount == coverage.PreparedCallHierarchyItemCount &&
		coverage.OutgoingCallCompletedRequestCount == coverage.OutgoingCallRequestCount &&
		coverage.IncomingCallRequestCount == coverage.PreparedCallHierarchyItemCount &&
		coverage.IncomingCallCompletedRequestCount == coverage.IncomingCallRequestCount
}

func supportedRepositoryGoplsFailureStage(stage string) bool {
	switch stage {
	case repositoryGoplsStageSyntaxInventory,
		repositoryGoplsStageSessionInitialize,
		repositoryGoplsStageOpenDocument,
		repositoryGoplsStageDocumentSymbol,
		repositoryGoplsStagePackageSymbols,
		repositoryGoplsStageDefinition,
		repositoryGoplsStageReferences,
		repositoryGoplsStagePrepareCallHierarchy,
		repositoryGoplsStageOutgoingCalls,
		repositoryGoplsStageIncomingCalls,
		repositoryGoplsStageCloseDocument,
		repositoryGoplsStageSessionShutdown,
		repositoryGoplsStageDocumentSymbolGrounding,
		repositoryGoplsStagePackageGrounding,
		repositoryGoplsStageDefinitionGrounding,
		repositoryGoplsStageReferenceGrounding,
		repositoryGoplsStageCallGrounding,
		repositoryGoplsStageIncomingCallGrounding:
		return true
	default:
		return false
	}
}

type repositoryGoSyntaxCount struct {
	packages     int
	identifiers  int
	declarations int
	callables    int
}

func repositoryGoSyntaxCounts(input RepositoryExtractorInput) (repositoryGoSyntaxCount, error) {
	var counts repositoryGoSyntaxCount
	for _, file := range input.Files {
		sites, err := collectGoIdentifierSites(file.FileSnapshot.Path, file.Content)
		if err != nil {
			return repositoryGoSyntaxCount{}, err
		}
		counts.identifiers += len(sites)
		extraction, err := ExtractGoParserFile(GoParserFileInput{
			RepoID:    input.RepositorySnapshot.RepoID,
			CommitSHA: input.RepositorySnapshot.CommitSHA,
			Path:      file.FileSnapshot.Path,
			Source:    file.Content,
		})
		if err != nil {
			return repositoryGoSyntaxCount{}, err
		}
		counts.packages++
		for _, declaration := range extraction.Declarations {
			if declaration.Name == "_" {
				continue
			}
			counts.declarations++
			if isCallableCodeSymbol(declaration.Kind) {
				counts.callables++
			}
		}
	}
	return counts, nil
}
