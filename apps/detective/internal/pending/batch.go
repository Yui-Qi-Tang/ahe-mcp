package pending

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"

	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/ahemcp"
	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/labstatus"
)

// MaxBatchCandidates bounds one controller-selected row's recovery workload.
const MaxBatchCandidates = 16
const batchIndexVersion = "detective-candidate-batch-index/v1"
const batchSubmissionMode = "independent-single-candidate-native-attempts/v1"

// BatchIndex freezes the complete validated row output before any intake. Its
// ordered members are projections into independent v1 checkpoints, not a native
// AHE multi-proposal batch, an admission set, or a mutable progress ledger.
type BatchIndex struct {
	SchemaVersion        string             `json:"schema_version"`
	NativeSubmissionMode string             `json:"native_submission_mode"`
	SourceID             string             `json:"source_id"`
	RawText              string             `json:"raw_text"`
	Batch                labstatus.RowBatch `json:"batch"`
	StatusClause         string             `json:"status_clause"`
	Members              []BatchMember      `json:"members"`
	Digest               string             `json:"digest"`
}

// BatchMember binds order and every original candidate field to a v1 checkpoint.
type BatchMember struct {
	Ordinal          int    `json:"ordinal"`
	CandidateDigest  string `json:"candidate_digest"`
	CheckpointDigest string `json:"checkpoint_digest"`
}

// NewBatchIndex detaches one selected row containing 1..16 candidates. Source
// and extraction remain unchanged; only the individual native submissions are
// intentionally projected into single-candidate attempts.
func NewBatchIndex(sourceID string, document *labstatus.Document, batch labstatus.RowBatch, statusClause string) (BatchIndex, error) {
	if document == nil {
		return BatchIndex{}, errors.New("batch index requires an observed document")
	}
	index := BatchIndex{SchemaVersion: batchIndexVersion, NativeSubmissionMode: batchSubmissionMode, SourceID: sourceID, RawText: document.RawText(), Batch: batch, StatusClause: statusClause, Members: []BatchMember{}}
	members, err := index.expectedMembers(document)
	if err != nil {
		return BatchIndex{}, err
	}
	index.Members = members
	index.Digest, err = batchIndexDigest(index)
	if err != nil {
		return BatchIndex{}, err
	}
	encoded, _ := json.Marshal(index)
	var detached BatchIndex
	if json.Unmarshal(encoded, &detached) != nil || !reflect.DeepEqual(index, detached) {
		return BatchIndex{}, errors.New("batch index must preserve exact valid UTF-8")
	}
	return detached, nil
}

func (index BatchIndex) expectedMembers(document *labstatus.Document) ([]BatchMember, error) {
	if index.SchemaVersion != batchIndexVersion || index.NativeSubmissionMode != batchSubmissionMode || len(index.Batch.Rows) != 1 || index.Batch.Rows[0].Result == nil {
		return nil, errors.New("batch index requires one validated source row and its bounded projection contract")
	}
	records := index.Batch.Rows[0].Result.Records
	if len(records) < 1 || len(records) > MaxBatchCandidates {
		return nil, errors.New("batch index requires 1 to 16 candidates")
	}
	if err := labstatus.ValidateRowBatch(document, index.Batch, index.StatusClause); err != nil {
		return nil, errors.New("batch index does not match the frozen row and extraction scope")
	}
	if err := ahemcp.ValidateSubmission(index.SourceID, document, index.Batch.Extractor, records); err != nil {
		return nil, errors.New("batch source and candidates do not satisfy intake constraints")
	}
	members := make([]BatchMember, 0, len(records))
	seen := map[string]bool{}
	for i, record := range records {
		// Native one-candidate identity uses statement + exact reference range;
		// reject two projections that would silently share one native occurrence.
		statement, err := ahemcp.ProposalStatement(index.Batch.Extractor, record)
		if err != nil {
			return nil, err
		}
		identity, _ := json.Marshal([]any{statement, record.Citation.StartLine, record.Citation.EndLine})
		if seen[string(identity)] {
			return nil, errors.New("batch candidates would share the same native request identity")
		}
		seen[string(identity)] = true
		checkpoint, err := New(index.SourceID, document, index.project(i), index.StatusClause)
		if err != nil {
			return nil, err
		}
		body, _ := json.Marshal(record)
		members = append(members, BatchMember{Ordinal: i + 1, CandidateDigest: checkpointHash(body), CheckpointDigest: checkpoint.Digest})
	}
	return members, nil
}

func (index BatchIndex) project(i int) labstatus.RowBatch {
	batch := index.Batch
	row := batch.Rows[0]
	result := *row.Result
	result.Records = []labstatus.Record{result.Records[i]}
	row.Result = &result
	batch.Rows = []labstatus.RowOutcome{row}
	return batch
}

func batchIndexDigest(index BatchIndex) (string, error) {
	index.Digest = ""
	body, err := json.Marshal(index)
	if err != nil || len(body)+128 > maxCheckpointBytes {
		return "", errors.New("batch index exceeds its encoding bound")
	}
	return checkpointHash(body), nil
}

// Document validates all indexed projections before reconstructing the frozen
// source. The original source path is an observation label and is never opened.
func (index BatchIndex) Document() (*labstatus.Document, error) {
	digest, err := batchIndexDigest(index)
	if err != nil || digest != index.Digest {
		return nil, errors.New("batch index digest differs from its exact inputs")
	}
	document, err := labstatus.RestoreDocument(index.Batch.Source.Path, index.RawText)
	if err != nil {
		return nil, errors.New("batch index contains invalid source bytes")
	}
	expected, err := index.expectedMembers(document)
	if err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(index.Members, expected) {
		return nil, errors.New("batch member order or checkpoint binding drifted")
	}
	return document, nil
}

// WriteBatchIndex publishes the immutable complete index before child files.
// A crash while publishing children can then be resumed without extraction.
func (w *Writer) WriteBatchIndex(index BatchIndex) error {
	if _, err := index.Document(); err != nil {
		return err
	}
	body, err := json.Marshal(index)
	if err != nil || len(body)+1 > maxCheckpointBytes {
		return errors.New("batch index exceeds its encoding bound")
	}
	return w.writeBody(body)
}

// LoadBatchIndex reads the closed, private index without creating a lock.
func LoadBatchIndex(path string) (BatchIndex, error) {
	var index BatchIndex
	if err := readReviewFile(path, &index); err != nil {
		return BatchIndex{}, err
	}
	if _, err := index.Document(); err != nil {
		return BatchIndex{}, err
	}
	return index, nil
}

func batchMemberPaths(path string, ordinal int) (checkpoint, locator, receipt string) {
	base := fmt.Sprintf("%s.candidate-%03d", path, ordinal)
	return base + ".checkpoint.json", base + ".locator.json", base + ".receipt.json"
}

func batchMemberCheckpoint(index BatchIndex, document *labstatus.Document, ordinal int) (Checkpoint, error) {
	if ordinal < 1 || ordinal > len(index.Members) {
		return Checkpoint{}, errors.New("batch member ordinal is outside its complete index")
	}
	checkpoint, err := New(index.SourceID, document, index.project(ordinal-1), index.StatusClause)
	if err != nil || checkpoint.Digest != index.Members[ordinal-1].CheckpointDigest {
		return Checkpoint{}, errors.New("batch member differs from its indexed checkpoint")
	}
	return checkpoint, nil
}

func ensureBatchCheckpoint(path string, expected Checkpoint) error {
	if _, err := os.Lstat(path); err == nil {
		current, err := Load(path)
		if err != nil || !reflect.DeepEqual(current, expected) {
			return errors.New("existing batch checkpoint differs from its index")
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return errors.New("cannot inspect batch checkpoint path")
	}
	w, err := Reserve(path)
	if err != nil {
		return err
	}
	defer w.Close()
	if err := w.Write(expected); err != nil {
		return err
	}
	return w.Close()
}

// MaterializeBatch publishes or checks every deterministic v1 checkpoint. The
// index remains available even if a child publication fails partway through.
func MaterializeBatch(path string) error {
	index, err := LoadBatchIndex(path)
	if err != nil {
		return err
	}
	document, err := index.Document()
	if err != nil {
		return err
	}
	for _, member := range index.Members {
		checkpoint, err := batchMemberCheckpoint(index, document, member.Ordinal)
		if err != nil {
			return err
		}
		child, _, _ := batchMemberPaths(path, member.Ordinal)
		if filepath.Dir(child) != filepath.Dir(path) {
			return errors.New("batch child escaped its private directory")
		}
		if err := ensureBatchCheckpoint(child, checkpoint); err != nil {
			return err
		}
	}
	return nil
}

// PrepareBatchIndex consumes a previously saved, complete row extraction. It
// does not invoke a model. The immutable index is published before its children
// so publication failures can be recovered from that same index.
func PrepareBatchIndex(path, sourcePath, rowBatchPath, sourceID, statusClause string) (BatchIndex, error) {
	w, err := Reserve(path)
	if err != nil {
		return BatchIndex{}, err
	}
	defer w.Close()
	var batch labstatus.RowBatch
	if err := readReviewFile(rowBatchPath, &batch); err != nil {
		return BatchIndex{}, err
	}
	document, err := labstatus.LoadDocument(sourcePath)
	if err != nil {
		return BatchIndex{}, errors.New("cannot load the selected frozen source")
	}
	index, err := NewBatchIndex(sourceID, document, batch, statusClause)
	if err != nil {
		return BatchIndex{}, err
	}
	if err := w.WriteBatchIndex(index); err != nil {
		return BatchIndex{}, err
	}
	if err := w.Close(); err != nil {
		return BatchIndex{}, err
	}
	if err := MaterializeBatch(path); err != nil {
		return BatchIndex{}, errors.New("batch index was saved but child publication is incomplete; resume the same index")
	}
	return index, nil
}
