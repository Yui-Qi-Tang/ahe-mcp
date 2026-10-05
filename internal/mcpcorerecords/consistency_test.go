package mcpcorerecords

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/runtimeauth"
	"github.com/jackc/pgx/v5/pgxpool"
	"testing"
)

func TestConsistencyRecorderRejectsImpersonationAndAmbiguousRequests(t *testing.T) {
	b, err := NewBackend(new(pgxpool.Pool), runtimeauth.Principal{ID: "fixed-recorder"})
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{`{"recorded_by":"fake"}`, `{"Recorded_By":"fake"}`, `{"watch_id":"a","WATCH_ID":"b"}`, `{"request":{"scope":{"namespace":"a","namespace":"b"}}}`, `{"unexpected":1}`} {
		if _, err := b.CallTool(context.Background(), "register_consistency_watch", []byte(raw)); err == nil {
			t.Fatal("ambiguous/impersonated watch accepted")
		}
	}
	for _, raw := range []string{`{"watch_id":"watch"}`, `{"watch_id":"watch","revision":1,"unexpected":1}`} {
		if _, err := CallRead(context.Background(), new(pgxpool.Pool), "get_consistency_watch", []byte(raw)); err == nil {
			t.Fatal("invalid read reached database")
		}
	}
}

func TestConsistencyArtifactChunksPreserveImmutableIdentityAndDigest(t *testing.T) {
	body := bytes.Repeat([]byte("proof bytes\n"), 14000)
	var restored []byte
	var digest string
	offset := int64(0)
	for {
		value, err := artifactChunk("run-a", "proof.drat", body, offset, 65536)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		var chunk struct {
			RunID      string `json:"run_id"`
			ArtifactID string `json:"artifact_id"`
			SHA256     string `json:"sha256"`
			Data       []byte `json:"data_base64"`
			NextOffset int64  `json:"next_offset"`
			Done       bool   `json:"done"`
		}
		if err := json.Unmarshal(raw, &chunk); err != nil {
			t.Fatal(err)
		}
		if chunk.RunID != "run-a" || chunk.ArtifactID != "proof.drat" || len(chunk.Data) > 65536 || (digest != "" && digest != chunk.SHA256) {
			t.Fatal("chunk identity/digest changed")
		}
		digest = chunk.SHA256
		restored = append(restored, chunk.Data...)
		offset = chunk.NextOffset
		if chunk.Done {
			break
		}
	}
	if !bytes.Equal(body, restored) {
		t.Fatal("chunk reassembly changed bytes")
	}
	for _, off := range []int64{-1, int64(len(body) + 1)} {
		if _, err := artifactChunk("run-a", "proof", body, off, 1); err == nil {
			t.Fatal("invalid offset accepted")
		}
	}
	if _, err := artifactChunk("run-a", "proof", body, 0, 65537); err == nil {
		t.Fatal("unbounded chunk accepted")
	}
}
