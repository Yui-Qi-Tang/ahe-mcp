package evidenceingestion

import (
	"strings"
	"testing"
	"time"
)

func TestPrepareRepositoryExtractionWorkRetryControllerTick(t *testing.T) {
	request, err := prepareRepositoryExtractionWorkRetryControllerTick(RepositoryExtractionWorkRetryControllerTickInput{
		RequestID:       "  retry-controller-tick-1  ",
		Limit:           RepositoryExtractionWorkRetryControllerTickMaxLimit,
		ConsumerActorID: "  retry-controller  ",
	})
	if err != nil {
		t.Fatalf("prepare retry controller tick: %v", err)
	}
	if request.input.RequestID != "retry-controller-tick-1" ||
		request.input.Limit != RepositoryExtractionWorkRetryControllerTickMaxLimit ||
		request.input.ConsumerActorID != "retry-controller" ||
		request.requestPayloadHash == "" {
		t.Fatalf("prepared retry controller tick = %+v", request)
	}

	changedLimit, err := prepareRepositoryExtractionWorkRetryControllerTick(RepositoryExtractionWorkRetryControllerTickInput{
		RequestID:       request.input.RequestID,
		Limit:           request.input.Limit - 1,
		ConsumerActorID: request.input.ConsumerActorID,
	})
	if err != nil {
		t.Fatalf("prepare changed-limit retry controller tick: %v", err)
	}
	changedActor, err := prepareRepositoryExtractionWorkRetryControllerTick(RepositoryExtractionWorkRetryControllerTickInput{
		RequestID:       request.input.RequestID,
		Limit:           request.input.Limit,
		ConsumerActorID: "another-controller",
	})
	if err != nil {
		t.Fatalf("prepare changed-actor retry controller tick: %v", err)
	}
	if changedLimit.requestPayloadHash == request.requestPayloadHash || changedActor.requestPayloadHash == request.requestPayloadHash {
		t.Fatal("retry controller tick payload hash did not bind limit and actor")
	}
}

func TestPrepareRepositoryExtractionWorkRetryControllerTickRejectsInvalidInput(t *testing.T) {
	valid := RepositoryExtractionWorkRetryControllerTickInput{
		RequestID:       "retry-controller-tick-1",
		Limit:           1,
		ConsumerActorID: "retry-controller",
	}
	for name, input := range map[string]RepositoryExtractionWorkRetryControllerTickInput{
		"missing request": {Limit: valid.Limit, ConsumerActorID: valid.ConsumerActorID},
		"zero limit":      {RequestID: valid.RequestID, ConsumerActorID: valid.ConsumerActorID},
		"large limit":     {RequestID: valid.RequestID, Limit: RepositoryExtractionWorkRetryControllerTickMaxLimit + 1, ConsumerActorID: valid.ConsumerActorID},
		"missing actor":   {RequestID: valid.RequestID, Limit: valid.Limit},
		"large actor":     {RequestID: valid.RequestID, Limit: valid.Limit, ConsumerActorID: strings.Repeat("a", 201)},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := prepareRepositoryExtractionWorkRetryControllerTick(input)
			assertKind(t, err, ErrorInvalidInput)
		})
	}
}

func TestRepositoryExtractionWorkRetryControllerTickChildRequestIDIsStableAndBound(t *testing.T) {
	first, err := repositoryExtractionWorkRetryControllerTickChildRequestID("tick-1", "policy-1")
	if err != nil {
		t.Fatalf("build retry controller child request ID: %v", err)
	}
	replay, err := repositoryExtractionWorkRetryControllerTickChildRequestID("tick-1", "policy-1")
	if err != nil {
		t.Fatalf("rebuild retry controller child request ID: %v", err)
	}
	changedTick, err := repositoryExtractionWorkRetryControllerTickChildRequestID("tick-2", "policy-1")
	if err != nil {
		t.Fatalf("build changed-tick child request ID: %v", err)
	}
	changedPolicy, err := repositoryExtractionWorkRetryControllerTickChildRequestID("tick-1", "policy-2")
	if err != nil {
		t.Fatalf("build changed-policy child request ID: %v", err)
	}
	if first != replay || first == changedTick || first == changedPolicy || !strings.HasPrefix(first, "retry-controller-consume:") {
		t.Fatalf("retry controller child IDs = %q/%q/%q/%q", first, replay, changedTick, changedPolicy)
	}
}

func TestRunRepositoryExtractionWorkRetryControllerTickValidatesPoolAndTime(t *testing.T) {
	_, err := RunRepositoryExtractionWorkRetryControllerTick(t.Context(), nil, RepositoryExtractionWorkRetryControllerTickInput{})
	assertKind(t, err, ErrorInvalidInput)

	_, err = runRepositoryExtractionWorkRetryControllerTick(t.Context(), nil, RepositoryExtractionWorkRetryControllerTickInput{
		RequestID:       "retry-controller-tick-1",
		Limit:           1,
		ConsumerActorID: "retry-controller",
	}, time.Time{})
	assertKind(t, err, ErrorInvalidInput)
}
