package evidenceingestion

import (
	"testing"
	"time"
)

func TestValidateGitRepositoryChangeObservationRequest(t *testing.T) {
	for _, tc := range []struct {
		name      string
		requestID string
		window    time.Duration
		wantError bool
	}{
		{name: "valid", requestID: " observation-1 ", window: 2 * time.Second},
		{name: "missing request", requestID: " ", window: time.Second, wantError: true},
		{name: "zero window", requestID: "observation-1", wantError: true},
		{name: "fractional millisecond", requestID: "observation-1", window: time.Millisecond + time.Nanosecond, wantError: true},
		{name: "window too large", requestID: "observation-1", window: GitRepositoryChangeMaxStabilityWindow + time.Millisecond, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requestID, window, err := validateGitRepositoryChangeObservationRequest(tc.requestID, tc.window)
			if tc.wantError {
				assertKind(t, err, ErrorInvalidInput)
				return
			}
			if err != nil {
				t.Fatalf("validateGitRepositoryChangeObservationRequest() error = %v", err)
			}
			if requestID != "observation-1" || window != tc.window {
				t.Fatalf("validated request = %q/%s", requestID, window)
			}
		})
	}
}

func TestValidateGitRepositoryChangeInspection(t *testing.T) {
	clean := gitRepositoryChangeObservationTestInspection(t, false)
	validated, err := validateGitRepositoryChangeInspection(clean)
	if err != nil {
		t.Fatalf("validateGitRepositoryChangeInspection(clean) error = %v", err)
	}
	if validated != clean {
		t.Fatalf("validated clean inspection = %+v, want %+v", validated, clean)
	}

	dirty := gitRepositoryChangeObservationTestInspection(t, true)
	if _, err := validateGitRepositoryChangeInspection(dirty); err != nil {
		t.Fatalf("validateGitRepositoryChangeInspection(dirty) error = %v", err)
	}

	t.Run("tampered token", func(t *testing.T) {
		tampered := dirty
		tampered.ChangeToken = contentHash([]byte("tampered"))
		_, err := validateGitRepositoryChangeInspection(tampered)
		assertKind(t, err, ErrorChangeObservationConflict)
	})

	t.Run("dirty without changes", func(t *testing.T) {
		invalid := dirty
		invalid.TrackedChangeCount = 0
		_, err := validateGitRepositoryChangeInspection(invalid)
		assertKind(t, err, ErrorInvalidInput)
	})

	t.Run("clean with fingerprint", func(t *testing.T) {
		invalid := clean
		invalid.DirtyFingerprint = contentHash([]byte("unexpected"))
		_, err := validateGitRepositoryChangeInspection(invalid)
		assertKind(t, err, ErrorInvalidInput)
	})
}

func TestGitRepositoryChangeObservationPayloadBindsStabilityPolicy(t *testing.T) {
	inspection := gitRepositoryChangeObservationTestInspection(t, false)
	short, err := gitRepositoryChangeObservationPayloadHash(inspection, time.Second)
	if err != nil {
		t.Fatalf("short payload hash error = %v", err)
	}
	long, err := gitRepositoryChangeObservationPayloadHash(inspection, 2*time.Second)
	if err != nil {
		t.Fatalf("long payload hash error = %v", err)
	}
	if short == long {
		t.Fatalf("payload hashes are equal: %s", short)
	}
}

func gitRepositoryChangeObservationTestInspection(t *testing.T, dirty bool) GitRepositoryChangeInspection {
	t.Helper()
	inspection := GitRepositoryChangeInspection{
		TokenContract:            GitRepositoryChangeTokenV1,
		DirtyFingerprintContract: GitRepositoryDirtyFingerprintV1,
		RepoID:                   "observation-test",
		HeadCommitSHA:            "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}
	if dirty {
		inspection.Dirty = true
		inspection.DirtyFingerprint = contentHash([]byte("dirty"))
		inspection.TrackedChangeCount = 1
	}
	changeToken, err := gitRepositoryChangeToken(inspection.RepoID, inspection.HeadCommitSHA, inspection.DirtyFingerprint)
	if err != nil {
		t.Fatalf("gitRepositoryChangeToken() error = %v", err)
	}
	inspection.ChangeToken = changeToken
	return inspection
}
