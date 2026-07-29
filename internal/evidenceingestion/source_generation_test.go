package evidenceingestion

import (
	"reflect"
	"testing"
)

func TestCreateRepositorySourceGenerationValidatesInput(t *testing.T) {
	_, err := CreateRepositorySourceGeneration(t.Context(), nil, RepositorySourceGenerationCreateInput{})
	assertKind(t, err, ErrorInvalidInput)

	_, err = createRepositorySourceGeneration(t.Context(), nil, RepositorySourceGenerationCreateInput{})
	assertKind(t, err, ErrorInvalidInput)

	_, err = createRepositorySourceGeneration(t.Context(), nil, RepositorySourceGenerationCreateInput{ProposalBatchID: "not-a-batch"})
	assertKind(t, err, ErrorInvalidRecordID)
}

func TestActivateRepositorySourceGenerationValidatesInput(t *testing.T) {
	_, err := ActivateRepositorySourceGeneration(t.Context(), nil, RepositorySourceGenerationActivationInput{})
	assertKind(t, err, ErrorInvalidInput)

	tests := []RepositorySourceGenerationActivationInput{
		{},
		{RequestID: "activate"},
		{RequestID: "activate", SourceGenerationID: "not-a-generation"},
	}
	for _, input := range tests {
		_, err := activateRepositorySourceGeneration(t.Context(), nil, input)
		if input.SourceGenerationID == "not-a-generation" {
			assertKind(t, err, ErrorInvalidRecordID)
			continue
		}
		assertKind(t, err, ErrorInvalidInput)
	}
}

func TestClassifyRepositoryProposalIdentitiesPairsDuplicatesDeterministically(t *testing.T) {
	current := []repositoryProposalIdentity{
		{OccurrenceID: "occ:current-z", Identity: "sha256:b"},
		{OccurrenceID: "occ:current-b", Identity: "sha256:a"},
		{OccurrenceID: "occ:current-a", Identity: "sha256:a"},
	}
	previous := []repositoryProposalIdentity{
		{OccurrenceID: "occ:previous-c", Identity: "sha256:c"},
		{OccurrenceID: "occ:previous-a", Identity: "sha256:a"},
	}

	got := classifyRepositoryProposalIdentities(current, previous)
	want := []repositoryProposalReconciliationItem{
		{
			State:                RepositoryProposalLifecycleUnchanged,
			ProposalIdentity:     "sha256:a",
			CurrentOccurrenceID:  "occ:current-a",
			PreviousOccurrenceID: "occ:previous-a",
		},
		{
			State:               RepositoryProposalLifecycleNew,
			ProposalIdentity:    "sha256:a",
			CurrentOccurrenceID: "occ:current-b",
		},
		{
			State:               RepositoryProposalLifecycleNew,
			ProposalIdentity:    "sha256:b",
			CurrentOccurrenceID: "occ:current-z",
		},
		{
			State:                RepositoryProposalLifecycleStale,
			ProposalIdentity:     "sha256:c",
			PreviousOccurrenceID: "occ:previous-c",
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("classifyRepositoryProposalIdentities() = %+v, want %+v", got, want)
	}
}

func TestClassifyRepositoryProposalIdentitiesHandlesEmptyGeneration(t *testing.T) {
	previous := []repositoryProposalIdentity{
		{OccurrenceID: "occ:previous-b", Identity: "sha256:b"},
		{OccurrenceID: "occ:previous-a", Identity: "sha256:a"},
	}

	got := classifyRepositoryProposalIdentities(nil, previous)
	want := []repositoryProposalReconciliationItem{
		{
			State:                RepositoryProposalLifecycleStale,
			ProposalIdentity:     "sha256:a",
			PreviousOccurrenceID: "occ:previous-a",
		},
		{
			State:                RepositoryProposalLifecycleStale,
			ProposalIdentity:     "sha256:b",
			PreviousOccurrenceID: "occ:previous-b",
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("classifyRepositoryProposalIdentities() = %+v, want %+v", got, want)
	}
}

func TestValidateRepositoryProposalIdentitySourceRefsRequiresExactGenerationBinding(t *testing.T) {
	generation := RepositorySourceGeneration{
		ID:                   "generation:one",
		RepoID:               "repo",
		RepositorySnapshotID: "repo-snapshot:one",
		CommitSHA:            "abc123",
	}
	valid := ResolvedSourceRef{
		RepositorySnapshotID: generation.RepositorySnapshotID,
		FileSnapshotID:       "file-snapshot:one",
		RepoID:               generation.RepoID,
		CommitSHA:            generation.CommitSHA,
	}
	if err := validateRepositoryProposalIdentitySourceRefs(generation, "occ:one", []ResolvedSourceRef{valid}); err != nil {
		t.Fatalf("valid source refs error = %v", err)
	}

	invalid := valid
	invalid.CommitSHA = "different"
	err := validateRepositoryProposalIdentitySourceRefs(generation, "occ:one", []ResolvedSourceRef{invalid})
	assertKind(t, err, ErrorRepositorySnapshotIntegrity)
}
