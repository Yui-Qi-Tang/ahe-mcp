package evidenceingestion

import (
	"strings"
	"testing"
	"time"
)

func TestPrepareRepositoryExtractionWorkExpiredMaintenanceTick(t *testing.T) {
	request, err := prepareRepositoryExtractionWorkExpiredMaintenanceTick(RepositoryExtractionWorkExpiredMaintenanceTickInput{
		RequestID:          " expired-maintenance-tick-1 ",
		Limit:              RepositoryExtractionWorkExpiredMaintenanceTickMaxLimit,
		MaintenanceActorID: " maintenance-controller-1 ",
	})
	if err != nil {
		t.Fatalf("prepare expired maintenance tick: %v", err)
	}
	if request.input.RequestID != "expired-maintenance-tick-1" ||
		request.input.Limit != RepositoryExtractionWorkExpiredMaintenanceTickMaxLimit ||
		request.input.MaintenanceActorID != "maintenance-controller-1" ||
		request.requestPayloadHash == "" {
		t.Fatalf("expired maintenance tick request = %+v", request)
	}

	changedLimit, err := prepareRepositoryExtractionWorkExpiredMaintenanceTick(RepositoryExtractionWorkExpiredMaintenanceTickInput{
		RequestID:          request.input.RequestID,
		Limit:              1,
		MaintenanceActorID: request.input.MaintenanceActorID,
	})
	if err != nil {
		t.Fatalf("prepare changed limit: %v", err)
	}
	changedActor, err := prepareRepositoryExtractionWorkExpiredMaintenanceTick(RepositoryExtractionWorkExpiredMaintenanceTickInput{
		RequestID:          request.input.RequestID,
		Limit:              request.input.Limit,
		MaintenanceActorID: "maintenance-controller-2",
	})
	if err != nil {
		t.Fatalf("prepare changed actor: %v", err)
	}
	if request.requestPayloadHash == changedLimit.requestPayloadHash || request.requestPayloadHash == changedActor.requestPayloadHash {
		t.Fatal("expired maintenance tick payload hash did not bind limit and actor")
	}
}

func TestPrepareRepositoryExtractionWorkExpiredMaintenanceTickRejectsInvalidInput(t *testing.T) {
	valid := RepositoryExtractionWorkExpiredMaintenanceTickInput{
		RequestID:          "expired-maintenance-tick-1",
		Limit:              1,
		MaintenanceActorID: "maintenance-controller-1",
	}
	for name, input := range map[string]RepositoryExtractionWorkExpiredMaintenanceTickInput{
		"missing request": {Limit: valid.Limit, MaintenanceActorID: valid.MaintenanceActorID},
		"zero limit":      {RequestID: valid.RequestID, MaintenanceActorID: valid.MaintenanceActorID},
		"large limit":     {RequestID: valid.RequestID, Limit: RepositoryExtractionWorkExpiredMaintenanceTickMaxLimit + 1, MaintenanceActorID: valid.MaintenanceActorID},
		"missing actor":   {RequestID: valid.RequestID, Limit: valid.Limit},
		"long actor":      {RequestID: valid.RequestID, Limit: valid.Limit, MaintenanceActorID: strings.Repeat("a", 201)},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := prepareRepositoryExtractionWorkExpiredMaintenanceTick(input)
			assertKind(t, err, ErrorInvalidInput)
		})
	}
}

func TestRepositoryExtractionWorkExpiredMaintenanceChildRequestIDsAreStableAndBound(t *testing.T) {
	recovery, err := repositoryExtractionWorkExpiredMaintenanceRecoveryRequestID("tick-1", "repo-work:1", "work-claim:1")
	if err != nil {
		t.Fatalf("build recovery child request ID: %v", err)
	}
	recoveryReplay, err := repositoryExtractionWorkExpiredMaintenanceRecoveryRequestID("tick-1", "repo-work:1", "work-claim:1")
	if err != nil {
		t.Fatalf("rebuild recovery child request ID: %v", err)
	}
	recoveryChanged, err := repositoryExtractionWorkExpiredMaintenanceRecoveryRequestID("tick-1", "repo-work:1", "work-claim:2")
	if err != nil {
		t.Fatalf("build changed recovery child request ID: %v", err)
	}
	if recovery != recoveryReplay || recovery == recoveryChanged || !strings.HasPrefix(recovery, "expired-maintenance-recover:") {
		t.Fatalf("recovery child IDs = %q/%q/%q", recovery, recoveryReplay, recoveryChanged)
	}

	repair, err := repositoryExtractionWorkExpiredMaintenanceExecutionRepairRequestID("tick-1", "execution-1")
	if err != nil {
		t.Fatalf("build repair child request ID: %v", err)
	}
	repairReplay, err := repositoryExtractionWorkExpiredMaintenanceExecutionRepairRequestID("tick-1", "execution-1")
	if err != nil {
		t.Fatalf("rebuild repair child request ID: %v", err)
	}
	repairChanged, err := repositoryExtractionWorkExpiredMaintenanceExecutionRepairRequestID("tick-2", "execution-1")
	if err != nil {
		t.Fatalf("build changed repair child request ID: %v", err)
	}
	if repair != repairReplay || repair == repairChanged || !strings.HasPrefix(repair, "expired-maintenance-repair:") {
		t.Fatalf("repair child IDs = %q/%q/%q", repair, repairReplay, repairChanged)
	}
}

func TestRunExpiredRepositoryExtractionWorkMaintenanceTickValidatesPoolAndTime(t *testing.T) {
	_, err := RunExpiredRepositoryExtractionWorkMaintenanceTick(t.Context(), nil, RepositoryExtractionWorkExpiredMaintenanceTickInput{})
	assertKind(t, err, ErrorInvalidInput)

	_, err = runExpiredRepositoryExtractionWorkMaintenanceTick(t.Context(), nil, RepositoryExtractionWorkExpiredMaintenanceTickInput{
		RequestID:          "expired-maintenance-tick-1",
		Limit:              1,
		MaintenanceActorID: "maintenance-controller-1",
	}, time.Time{})
	assertKind(t, err, ErrorInvalidInput)
}
