package evidenceingestion

import (
	"strings"
	"testing"
	"time"
)

func TestRepositoryExtractionWorkHeartbeatDelay(t *testing.T) {
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	leaseDuration := 200 * time.Millisecond
	tests := []struct {
		name           string
		leaseExpiresAt time.Time
		want           time.Duration
	}{
		{name: "fresh lease", leaseExpiresAt: now.Add(leaseDuration), want: 100 * time.Millisecond},
		{name: "long manual renewal", leaseExpiresAt: now.Add(500 * time.Millisecond), want: 400 * time.Millisecond},
		{name: "renew now", leaseExpiresAt: now.Add(100 * time.Millisecond), want: 0},
		{name: "already late", leaseExpiresAt: now.Add(50 * time.Millisecond), want: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := repositoryExtractionWorkHeartbeatDelay(now, tt.leaseExpiresAt, leaseDuration); got != tt.want {
				t.Fatalf("repositoryExtractionWorkHeartbeatDelay() = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestRepositoryExtractionWorkExecutionHeartbeatRequestIDIsStablePerSequence(t *testing.T) {
	first, err := repositoryExtractionWorkExecutionHeartbeatRequestID("execute-1", 1)
	if err != nil {
		t.Fatalf("first heartbeat request ID: %v", err)
	}
	replay, err := repositoryExtractionWorkExecutionHeartbeatRequestID("execute-1", 1)
	if err != nil {
		t.Fatalf("replayed heartbeat request ID: %v", err)
	}
	second, err := repositoryExtractionWorkExecutionHeartbeatRequestID("execute-1", 2)
	if err != nil {
		t.Fatalf("second heartbeat request ID: %v", err)
	}
	if first != replay || first == second || !strings.HasPrefix(first, "work-exec-heartbeat:") || !strings.HasPrefix(second, "work-exec-heartbeat:") {
		t.Fatalf("heartbeat request IDs = %q/%q/%q", first, replay, second)
	}
	_, err = repositoryExtractionWorkExecutionHeartbeatRequestID("execute-1", 0)
	assertKind(t, err, ErrorInvalidInput)
}
