package evidenceingestion

import (
	"math"
	"strings"
	"testing"
	"time"
)

const repositoryExtractionWorkTestLeaseMilliseconds = int64(60_000)

func TestValidateRepositoryExtractionWorkInputs(t *testing.T) {
	for _, extractor := range []string{ExtractorRepositoryGoParserCodeFact, ExtractorRepositoryGoplsCodeFact} {
		got, err := validateRepositoryExtractionWorkExtractor(" " + extractor + " ")
		if err != nil {
			t.Fatalf("validateRepositoryExtractionWorkExtractor(%q) error = %v", extractor, err)
		}
		if got != extractor {
			t.Fatalf("validated extractor = %q, want %q", got, extractor)
		}
	}
	_, err := validateRepositoryExtractionWorkExtractor("arbitrary-extractor")
	assertKind(t, err, ErrorInvalidInput)

	_, _, _, err = validateRepositoryExtractionWorkScheduleInput(RepositoryExtractionWorkScheduleInput{
		RequestID:            "schedule-1",
		ObservationRequestID: "observation-1",
		ExtractorName:        "arbitrary-extractor",
	})
	assertKind(t, err, ErrorInvalidInput)

	claimInput := RepositoryExtractionWorkClaimInput{
		RequestID:                 "claim-1",
		RepoID:                    "repo",
		ExtractorName:             ExtractorRepositoryGoParserCodeFact,
		WorkerID:                  "worker-1",
		LeaseDurationMilliseconds: repositoryExtractionWorkTestLeaseMilliseconds,
	}
	_, _, _, _, leaseDuration, err := validateRepositoryExtractionWorkClaimInput(claimInput)
	if err != nil {
		t.Fatalf("validateRepositoryExtractionWorkClaimInput() error = %v", err)
	}
	if leaseDuration != time.Minute {
		t.Fatalf("lease duration = %s, want 1m", leaseDuration)
	}

	claimInput.WorkerID = strings.Repeat("x", 201)
	_, _, _, _, _, err = validateRepositoryExtractionWorkClaimInput(claimInput)
	assertKind(t, err, ErrorInvalidInput)

	claimInput.WorkerID = "worker-1"
	for _, leaseMilliseconds := range []int64{0, RepositoryExtractionWorkMaxLeaseDuration.Milliseconds() + 1, math.MaxInt64} {
		claimInput.LeaseDurationMilliseconds = leaseMilliseconds
		_, _, _, _, _, err = validateRepositoryExtractionWorkClaimInput(claimInput)
		assertKind(t, err, ErrorInvalidInput)
	}
}

func TestRepositoryExtractionWorkPayloadsBindStreamAndWorker(t *testing.T) {
	parser, err := repositoryExtractionWorkSchedulePayloadHash("observation-1", ExtractorRepositoryGoParserCodeFact)
	if err != nil {
		t.Fatalf("parser schedule payload hash error = %v", err)
	}
	gopls, err := repositoryExtractionWorkSchedulePayloadHash("observation-1", ExtractorRepositoryGoplsCodeFact)
	if err != nil {
		t.Fatalf("gopls schedule payload hash error = %v", err)
	}
	if parser == gopls {
		t.Fatalf("extractor-specific schedule payload hashes are equal: %s", parser)
	}

	workerA, err := repositoryExtractionWorkClaimPayloadHash("repo", ExtractorRepositoryGoParserCodeFact, "worker-a", repositoryExtractionWorkTestLeaseMilliseconds)
	if err != nil {
		t.Fatalf("worker A claim payload hash error = %v", err)
	}
	workerB, err := repositoryExtractionWorkClaimPayloadHash("repo", ExtractorRepositoryGoParserCodeFact, "worker-b", repositoryExtractionWorkTestLeaseMilliseconds)
	if err != nil {
		t.Fatalf("worker B claim payload hash error = %v", err)
	}
	if workerA == workerB {
		t.Fatalf("worker-specific claim payload hashes are equal: %s", workerA)
	}
	longerLease, err := repositoryExtractionWorkClaimPayloadHash("repo", ExtractorRepositoryGoParserCodeFact, "worker-a", repositoryExtractionWorkTestLeaseMilliseconds+1)
	if err != nil {
		t.Fatalf("longer lease claim payload hash error = %v", err)
	}
	if workerA == longerLease {
		t.Fatalf("lease-specific claim payload hashes are equal: %s", workerA)
	}
}

func TestNewRepositoryExtractionWorkIsExtractorScoped(t *testing.T) {
	inspection := gitRepositoryChangeObservationTestInspection(t, false)
	observation := GitRepositoryChangeObservationResult{
		ObservationID:     "change-observation:1",
		ObservationNumber: 3,
		Inspection:        inspection,
		Stable:            true,
		StableAfter:       time.Now(),
	}
	parser, err := newRepositoryExtractionWork(observation, ExtractorRepositoryGoParserCodeFact)
	if err != nil {
		t.Fatalf("newRepositoryExtractionWork(parser) error = %v", err)
	}
	gopls, err := newRepositoryExtractionWork(observation, ExtractorRepositoryGoplsCodeFact)
	if err != nil {
		t.Fatalf("newRepositoryExtractionWork(gopls) error = %v", err)
	}
	if parser.WorkItemID == gopls.WorkItemID {
		t.Fatalf("extractor work item IDs are equal: %s", parser.WorkItemID)
	}
	if parser.ObservationID != observation.ObservationID || parser.HeadCommitSHA != inspection.HeadCommitSHA || parser.ChangeToken != inspection.ChangeToken {
		t.Fatalf("parser work identity = %+v, observation = %+v", parser, observation)
	}
}
