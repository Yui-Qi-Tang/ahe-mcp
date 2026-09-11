package pending

import (
	"context"
	"fmt"

	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/ahemcp"
	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/labstatus"
)

// Result describes a pending proposal verified through the independent Query
// service during this invocation. It does not grant canonical admission.
type Result struct {
	SchemaVersion    string             `json:"schema_version"`
	CheckpointDigest string             `json:"checkpoint_digest"`
	State            string             `json:"state"`
	Batch            labstatus.RowBatch `json:"batch"`
	Handoff          ahemcp.Handoff     `json:"handoff"`
	ReadbackVerified bool               `json:"readback_verified"`
}

// Resume replays the immutable checkpoint and verifies the pending result.
// Neither the original file nor a model is accessed. A failure after submission
// is not a rollback: retry the same checkpoint to resolve an uncertain outcome.
func Resume(ctx context.Context, path, intakeCommand, queryCommand string) (Result, error) {
	checkpoint, err := Load(path)
	if err != nil {
		return Result{}, err
	}
	document, err := checkpoint.Document()
	if err != nil {
		return Result{}, err
	}
	records := checkpoint.Batch.Rows[0].Result.Records
	handoff, err := ahemcp.Submit(ctx, intakeCommand, checkpoint.SourceID, document, checkpoint.Batch.Extractor, records)
	if err != nil {
		return Result{}, fmt.Errorf("resume pending submission; checkpoint retained for replay: %w", err)
	}
	if err := ahemcp.VerifyPending(ctx, queryCommand, checkpoint.SourceID, document, checkpoint.Batch.Extractor, records, handoff); err != nil {
		return Result{}, fmt.Errorf("pending readback unverified; checkpoint retained for replay: %w", err)
	}
	return Result{
		SchemaVersion: "detective-pending-resume/v1", CheckpointDigest: checkpoint.Digest,
		State: "pending_verified", Batch: checkpoint.Batch, Handoff: handoff,
		ReadbackVerified: true,
	}, nil
}
