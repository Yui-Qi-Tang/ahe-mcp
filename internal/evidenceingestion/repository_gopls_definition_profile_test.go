package evidenceingestion

import (
	"bytes"
	"path/filepath"
	"testing"
)

func TestRepositoryGoplsDefinitionContributionRecorderConservesOutcomes(t *testing.T) {
	recorder := newRepositoryGoplsDefinitionContributionRecorder()
	definition := func(usageStart, targetStart int) repositoryGoplsDefinition {
		return repositoryGoplsDefinition{
			usagePath:       "usage.go",
			usageStartByte:  usageStart,
			usageEndByte:    usageStart + 1,
			targetPath:      "target.go",
			targetStartByte: targetStart,
			targetEndByte:   targetStart + 1,
		}
	}
	a := definition(1, 101)
	b := definition(2, 102)
	c := definition(3, 103)
	d := definition(3, 104)

	recorder.record(0, 0, 0, nil)
	recorder.record(1, 1, 0, nil)
	recorder.record(1, 0, 1, nil)
	recorder.record(2, 1, 1, nil)
	recorder.record(1, 0, 0, []repositoryGoplsDefinition{a})
	recorder.record(1, 0, 0, []repositoryGoplsDefinition{a})
	recorder.record(2, 0, 0, []repositoryGoplsDefinition{b, b})
	recorder.record(3, 0, 0, []repositoryGoplsDefinition{c, d, d})

	got := recorder.result()
	if got.requestCount != 8 ||
		got.noLocationRequestCount != 1 ||
		got.selfOnlyRequestCount != 1 ||
		got.unselectedOnlyRequestCount != 1 ||
		got.selfAndUnselectedOnlyRequestCount != 1 ||
		got.selectedSingleTargetRequestCount != 3 ||
		got.selectedAmbiguousTargetRequestCount != 1 {
		t.Fatalf("definition request outcomes = %+v", got)
	}
	if got.selectedUniqueOnlyRequestCount != 1 ||
		got.selectedDuplicateOnlyRequestCount != 1 ||
		got.selectedMixedContributionRequestCount != 2 ||
		got.selectedLocationCount != 7 ||
		got.uniqueRelationCount != 4 ||
		got.duplicateLocationCount != 3 {
		t.Fatalf("definition request contributions = %+v", got)
	}
	if got.requestCount != got.noLocationRequestCount+
		got.selfOnlyRequestCount+
		got.unselectedOnlyRequestCount+
		got.selfAndUnselectedOnlyRequestCount+
		got.selectedSingleTargetRequestCount+
		got.selectedAmbiguousTargetRequestCount {
		t.Fatalf("definition request outcomes do not conserve requests: %+v", got)
	}
	if got.selectedSingleTargetRequestCount+got.selectedAmbiguousTargetRequestCount !=
		got.selectedUniqueOnlyRequestCount+
			got.selectedDuplicateOnlyRequestCount+
			got.selectedMixedContributionRequestCount {
		t.Fatalf("selected definition contributions do not conserve requests: %+v", got)
	}
	if got.selectedLocationCount != got.uniqueRelationCount+got.duplicateLocationCount {
		t.Fatalf("selected definition locations do not conserve relations: %+v", got)
	}
}

func TestRequestRepositoryGoplsDefinitionsProfilesUnchangedResults(t *testing.T) {
	input := repositoryGoplsPartialTestInput()
	file := input.Files[0]
	path := filepath.Join("/workspace", file.FileSnapshot.Path)
	runStart := bytes.Index(file.Content, []byte("Run"))
	runRange := testLSPRange(t, file.Content, runStart, runStart+len("Run"))
	client := repositoryGoplsFailureTestClient(
		t,
		map[string]any{"jsonrpc": "2.0", "id": 1, "result": []lspLocation{}},
		map[string]any{
			"jsonrpc": "2.0",
			"id":      2,
			"result":  []lspLocation{{URI: fileURI(path), Range: runRange}},
		},
	)
	recorder := newRepositoryGoplsDefinitionContributionRecorder()

	definitions, err := requestRepositoryGoplsDefinitionsProfiled(
		client,
		input.Files,
		map[string]string{file.FileSnapshot.Path: path},
		recorder,
	)
	if err != nil {
		t.Fatalf("requestRepositoryGoplsDefinitionsProfiled() error = %v", err)
	}
	if definitions.requestCount != 2 ||
		definitions.completedRequestCount != 2 ||
		definitions.locationCount != 1 ||
		definitions.selfDeclarationLocationCount != 1 ||
		len(definitions.relations) != 0 {
		t.Fatalf("profiled definitions = %+v", definitions)
	}
	got := recorder.result()
	if got.requestCount != 2 ||
		got.noLocationRequestCount != 1 ||
		got.selfOnlyRequestCount != 1 ||
		got.selectedLocationCount != 0 ||
		got.uniqueRelationCount != 0 {
		t.Fatalf("definition contribution = %+v", got)
	}
}
