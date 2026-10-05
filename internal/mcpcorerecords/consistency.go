package mcpcorerecords

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestion"
	"github.com/jackc/pgx/v5/pgxpool"
)

func isConsistencyRead(name string) bool {
	switch name {
	case "get_consistency_scope", "get_consistency_watch", "get_consistency_events", "get_consistency_run", "get_consistency_artifact":
		return true
	}
	return false
}

func callConsistencyRead(ctx context.Context, pool *pgxpool.Pool, name string, args json.RawMessage) (json.RawMessage, error) {
	var value any
	var err error
	switch name {
	case "get_consistency_scope":
		var in evidenceingestion.ConsistencyScope
		if err = decodeRequest(args, &in); err != nil {
			break
		}
		value, err = evidenceingestion.ReadConsistencyScope(ctx, pool, in)
	case "get_consistency_watch":
		var in struct {
			WatchID  string `json:"watch_id"`
			Revision *int64 `json:"revision"`
		}
		if err = decodeRequest(args, &in); err != nil {
			break
		}
		if in.Revision == nil {
			return nil, errors.New("explicit watch revision required: -1 latest or a positive revision")
		}
		value, err = evidenceingestion.ReadConsistencyWatch(ctx, pool, in.WatchID, *in.Revision)
	case "get_consistency_events":
		var in struct {
			WatchID string `json:"watch_id"`
			After   int64  `json:"after"`
			Limit   int    `json:"limit"`
		}
		if err = decodeRequest(args, &in); err != nil {
			break
		}
		value, err = evidenceingestion.ReadConsistencyEvents(ctx, pool, in.WatchID, in.After, in.Limit)
	case "get_consistency_run":
		var in struct {
			RunID string `json:"run_id"`
		}
		if err = decodeRequest(args, &in); err != nil {
			break
		}
		value, err = evidenceingestion.ReadConsistencyRun(ctx, pool, in.RunID)
	case "get_consistency_artifact":
		var in struct {
			RunID      string `json:"run_id"`
			ArtifactID string `json:"artifact_id"`
			Offset     int64  `json:"offset"`
			Limit      int    `json:"limit"`
		}
		if err = decodeRequest(args, &in); err != nil {
			break
		}
		if in.Offset < 0 || in.Limit < 1 || in.Limit > 65536 {
			return nil, errors.New("artifact offset or chunk limit invalid")
		}
		var body []byte
		body, err = evidenceingestion.ReadConsistencyArtifact(ctx, pool, in.RunID, in.ArtifactID)
		if err != nil {
			break
		}
		value, err = artifactChunk(in.RunID, in.ArtifactID, body, in.Offset, in.Limit)
	}
	if err != nil {
		return nil, err
	}
	return json.Marshal(value)
}

// Chunks are pinned to an immutable run and artifact, never a watch's latest run.
// No freshness assertion is made; use get_consistency_run for the read snapshot.
func artifactChunk(run, artifact string, body []byte, offset int64, limit int) (any, error) {
	if offset < 0 || offset > int64(len(body)) || limit < 1 || limit > 65536 {
		return nil, errors.New("artifact range invalid")
	}
	end := min(int64(len(body)), offset+int64(limit))
	sum := sha256.Sum256(body)
	return struct {
		RunID      string `json:"run_id"`
		ArtifactID string `json:"artifact_id"`
		SHA256     string `json:"sha256"`
		TotalBytes int    `json:"total_bytes"`
		Offset     int64  `json:"offset"`
		NextOffset int64  `json:"next_offset"`
		Done       bool   `json:"done"`
		Data       []byte `json:"data_base64"`
	}{run, artifact, hex.EncodeToString(sum[:]), len(body), offset, end, end == int64(len(body)), body[offset:end]}, nil
}
