package pending

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"time"
	"unicode/utf8"

	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/ahemcp"
	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/labstatus"
)

// Inspection is a read-only display of saved candidate inputs. Neither this
// display nor a successful pending readback is a human decision or admission.
type Inspection struct {
	SchemaVersion          string                  `json:"schema_version"`
	CheckpointDigest       string                  `json:"checkpoint_digest"`
	RequestIdentityVersion string                  `json:"request_identity_version"`
	SourceID               string                  `json:"source_id"`
	Source                 labstatus.Source        `json:"source"`
	Extractor              labstatus.ExtractorInfo `json:"extractor"`
	Section                labstatus.SourceSection `json:"section"`
	Row                    labstatus.SourceRow     `json:"row"`
	StatusClause           string                  `json:"status_clause"`
	Candidate              labstatus.Record        `json:"candidate"`
	Limitations            []string                `json:"limitations"`
	Pending                PendingInspection       `json:"pending"`
	AuthorityEffect        string                  `json:"authority_effect"`
	HumanReview            string                  `json:"human_review"`
	NextAction             string                  `json:"next_action"`
}

// PendingInspection separates no query from an exact, current-invocation
// pending observation. CheckCompletedAt is a client completion time, not a DB
// snapshot timestamp. Receipt is only a locator, never source authentication.
type PendingInspection struct {
	State            string          `json:"state"`
	CheckCompletedAt string          `json:"check_completed_at,omitempty"`
	Receipt          *ahemcp.Handoff `json:"receipt,omitempty"`
}

// Inspect loads a checkpoint without reserving or changing it. With both a
// saved resume result and a Query launcher, it verifies that exact occurrence
// is still pending. It never submits, retries intake, reads the original source
// or invokes a model. Query failure returns no successful inspection.
func Inspect(ctx context.Context, checkpointPath, receiptPath, queryCommand string) (Inspection, error) {
	if err := ctx.Err(); err != nil {
		return Inspection{}, err
	}
	if (receiptPath == "") != (queryCommand == "") {
		return Inspection{}, errors.New("pending inspection requires both a saved resume receipt and an explicit Query launcher, or neither")
	}
	checkpoint, err := Load(checkpointPath)
	if err != nil {
		return Inspection{}, err
	}
	result := Inspection{
		SchemaVersion: "detective-pending-inspection/v1", CheckpointDigest: checkpoint.Digest,
		RequestIdentityVersion: checkpoint.RequestIdentityVersion, SourceID: checkpoint.SourceID,
		Source: checkpoint.Batch.Source, Extractor: checkpoint.Batch.Extractor,
		Section: checkpoint.Batch.Section, Row: checkpoint.Batch.Rows[0].Row,
		StatusClause: checkpoint.StatusClause, Candidate: checkpoint.Batch.Rows[0].Result.Records[0],
		Limitations: checkpoint.Batch.Rows[0].Result.Limitations,
		Pending:     PendingInspection{State: "not_checked"}, AuthorityEffect: "none",
		HumanReview: "not_recorded", NextAction: "inspect_and_handoff_only",
	}
	if receiptPath == "" {
		return result, nil
	}
	receipt, err := loadInspectionReceipt(receiptPath, checkpoint)
	if err != nil {
		return Inspection{}, err
	}
	document, err := checkpoint.Document()
	if err != nil {
		return Inspection{}, err
	}
	if err := ahemcp.VerifyPending(ctx, queryCommand, checkpoint.SourceID, document,
		checkpoint.Batch.Extractor, checkpoint.Batch.Rows[0].Result.Records, receipt.Handoff); err != nil {
		return Inspection{}, fmt.Errorf("pending inspection unverified; no intake or admission was attempted: %w", err)
	}
	result.Pending = PendingInspection{State: "pending_verified",
		CheckCompletedAt: time.Now().UTC().Format(time.RFC3339Nano), Receipt: &receipt.Handoff}
	return result, nil
}

func loadInspectionReceipt(path string, checkpoint Checkpoint) (Result, error) {
	root, directory, name, err := checkpointParent(path)
	if err != nil {
		return Result{}, errors.New("inspection receipt requires a clean absolute path in a private directory")
	}
	defer root.Close()
	defer directory.Close()
	file, err := privateOpen(root, name, os.O_RDONLY, 0)
	if err != nil {
		return Result{}, errors.New("inspection receipt is not a readable private regular file")
	}
	defer file.Close()
	body, err := io.ReadAll(io.LimitReader(file, maxCheckpointBytes+1))
	if err != nil || len(body) > maxCheckpointBytes || !utf8.Valid(body) {
		return Result{}, errors.New("inspection receipt is unreadable, oversized, or invalid UTF-8")
	}
	return decodeInspectionReceipt(body, checkpoint)
}

func decodeInspectionReceipt(body []byte, checkpoint Checkpoint) (Result, error) {
	check := json.NewDecoder(bytes.NewReader(body))
	if uniqueJSON(check, 0) != nil {
		return Result{}, errors.New("inspection receipt contains malformed or duplicate JSON fields")
	}
	if _, err := check.Token(); err != io.EOF {
		return Result{}, errors.New("inspection receipt contains trailing JSON")
	}
	var receipt Result
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&receipt) != nil {
		return Result{}, errors.New("inspection receipt does not match the closed resume schema")
	}
	canonical, err := json.Marshal(receipt)
	var observed, expected any
	if err != nil || json.Unmarshal(body, &observed) != nil || json.Unmarshal(canonical, &expected) != nil || !reflect.DeepEqual(observed, expected) {
		return Result{}, errors.New("inspection receipt contains missing or noncanonical schema fields")
	}
	if receipt.SchemaVersion != "detective-pending-resume/v1" || receipt.State != "pending_verified" ||
		!receipt.ReadbackVerified || receipt.CheckpointDigest != checkpoint.Digest || !reflect.DeepEqual(receipt.Batch, checkpoint.Batch) {
		return Result{}, errors.New("inspection receipt does not match the saved checkpoint and prior resume result")
	}
	// VerifyPending separately validates the handoff and reads current state.
	// A syntactically valid historical result cannot certify today's pending state.
	return receipt, nil
}
