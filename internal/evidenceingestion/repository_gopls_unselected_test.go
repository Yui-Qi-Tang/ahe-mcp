package evidenceingestion

import (
	"path/filepath"
	"testing"
)

func TestRepositoryGoplsDefinitionsCountUnselectedLocations(t *testing.T) {
	input, _ := repositorySameFileDefinitionTestFixture(t)
	file := input.Files[0]
	selectedPath := filepath.Join("/workspace", file.FileSnapshot.Path)
	unselected := lspLocation{URI: fileURI(filepath.Join("/module-cache", "dependency.go"))}
	client := repositoryGoplsFailureTestClient(t,
		map[string]any{"jsonrpc": "2.0", "id": 1, "result": []lspLocation{}},
		map[string]any{"jsonrpc": "2.0", "id": 2, "result": []lspLocation{}},
		map[string]any{"jsonrpc": "2.0", "id": 3, "result": []lspLocation{}},
		map[string]any{"jsonrpc": "2.0", "id": 4, "result": []lspLocation{unselected}},
	)

	definitions, err := requestRepositoryGoplsDefinitions(client, input.Files, map[string]string{file.FileSnapshot.Path: selectedPath})
	if err != nil {
		t.Fatalf("requestRepositoryGoplsDefinitions() error = %v", err)
	}
	if definitions.locationCount != 1 || definitions.unselectedLocationCount != 1 || definitions.selfDeclarationLocationCount != 0 || len(definitions.relations) != 0 {
		t.Fatalf("unselected definition coverage = %+v", definitions)
	}
}

func TestRepositoryGoplsReferencesCountUnselectedLocations(t *testing.T) {
	input, _ := repositorySameFileDefinitionTestFixture(t)
	file := input.Files[0]
	selectedPath := filepath.Join("/workspace", file.FileSnapshot.Path)
	unselected := lspLocation{URI: fileURI(filepath.Join("/module-cache", "dependency.go"))}
	client := repositoryGoplsFailureTestClient(t,
		map[string]any{"jsonrpc": "2.0", "id": 1, "result": []lspLocation{unselected}},
		map[string]any{"jsonrpc": "2.0", "id": 2, "result": []lspLocation{}},
	)

	references, err := requestRepositoryGoplsReferences(client, input, map[string]string{file.FileSnapshot.Path: selectedPath})
	if err != nil {
		t.Fatalf("requestRepositoryGoplsReferences() error = %v", err)
	}
	if references.locationCount != 1 || references.unselectedLocationCount != 1 || references.selfDeclarationLocationCount != 0 || len(references.relations) != 0 {
		t.Fatalf("unselected reference coverage = %+v", references)
	}
}

func TestRepositoryGoplsCallsCountUnselectedResultsWithoutMappingEndpoints(t *testing.T) {
	input := repositoryGoplsPartialTestInput()
	file := input.Files[0]
	selectedPath := filepath.Join("/workspace", file.FileSnapshot.Path)
	declaration := repositoryTestDeclaration(t, input.RepositorySnapshot, file.FileSnapshot.Path, file.Content, "sample.Run")
	selected := testCallHierarchyItem(t, selectedPath, file.Content, declaration)
	unselected := lspCallHierarchyItem{
		Name: "External",
		Kind: 12,
		URI:  fileURI(filepath.Join("/module-cache", "dependency.go")),
	}
	selectedRange := testLSPRange(t, file.Content, declaration.StartByte, declaration.EndByte)
	client := repositoryGoplsFailureTestClient(t,
		map[string]any{"jsonrpc": "2.0", "id": 1, "result": []lspCallHierarchyItem{selected}},
		map[string]any{"jsonrpc": "2.0", "id": 2, "result": []lspCallHierarchyOutgoingCall{{To: unselected, FromRanges: []lspRange{selectedRange}}}},
		map[string]any{"jsonrpc": "2.0", "id": 3, "result": []lspCallHierarchyIncomingCall{{From: unselected, FromRanges: []lspRange{{}}}}},
	)

	calls, err := requestRepositoryGoplsCalls(client, input, map[string]string{file.FileSnapshot.Path: selectedPath})
	if err != nil {
		t.Fatalf("requestRepositoryGoplsCalls() error = %v", err)
	}
	if calls.outgoingCallResultCount != 1 || calls.outgoingCallSiteCount != 1 || calls.outgoingUnselectedResultCount != 1 || calls.outgoingUnselectedSiteCount != 1 || len(calls.calls) != 0 {
		t.Fatalf("unselected outgoing call coverage = %+v", calls)
	}
	if calls.incomingCallResultCount != 1 || calls.incomingCallSiteCount != 1 || calls.incomingUnselectedResultCount != 1 || calls.incomingUnselectedSiteCount != 1 || len(calls.incomingCalls) != 0 {
		t.Fatalf("unselected incoming call coverage = %+v", calls)
	}
}
