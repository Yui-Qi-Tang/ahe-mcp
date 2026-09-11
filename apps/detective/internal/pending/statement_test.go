//go:build darwin || linux

package pending

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/ahemcp"
)

func TestStatementBatchKeepsDistinctProjectedContext(t *testing.T) {
	for _, version := range []string{"0.1.0", "0.1.1", "0.1.2"} {
		t.Run(version, func(t *testing.T) {
			doc, batch := batchFixture(t, 2)
			batch.Extractor.Version = version
			batch.Rows[0].Result.Records[1].Statement = batch.Rows[0].Result.Records[0].Statement
			index, err := NewBatchIndex("mock:context", doc, batch, "")
			if version != "0.1.2" {
				if err == nil {
					t.Fatal("legacy raw statements unexpectedly acquired distinct native identities")
				}
				return
			}
			if err != nil {
				t.Fatal("new context projections were collapsed", err)
			}
			if len(index.Members) != 2 || index.Members[0].CheckpointDigest == index.Members[1].CheckpointDigest || index.Members[0].CandidateDigest == index.Members[1].CandidateDigest {
				t.Fatal("batch lost distinct context or child checkpoint identity")
			}
			var previous string
			for _, member := range index.Members {
				child, err := batchMemberCheckpoint(index, doc, member.Ordinal)
				if err != nil {
					t.Fatal(err)
				}
				statement, err := ahemcp.ProposalStatement(child.Batch.Extractor, child.Batch.Rows[0].Result.Records[0])
				if err != nil || statement == previous {
					t.Fatal("batch would share one native statement/reference identity", err)
				}
				previous = statement
			}
		})
	}
}

func TestStatementLegacyCheckpointPhraseRoundTrip(t *testing.T) {
	for _, version := range []string{"0.1.0", "0.1.1"} {
		t.Run(version, func(t *testing.T) {
			doc, batch := checkpointFixture(t, "\r\n")
			batch.Extractor.Version = version
			record := &batch.Rows[0].Result.Records[0]
			record.Statement = record.Subject
			checkpoint, err := New("mock:legacy-statement", doc, batch, "")
			if err != nil {
				t.Fatal("historical phrase was requalified by new rules", err)
			}
			path := filepath.Join(checkpointDirectory(t), "checkpoint.json")
			before := saveReviewTestJSON(t, path, checkpoint)
			loaded, err := Load(path)
			if err != nil || !reflect.DeepEqual(loaded, checkpoint) {
				t.Fatal("historical checkpoint or digest changed on read", err)
			}
			statement, err := ahemcp.ProposalStatement(loaded.Batch.Extractor, loaded.Batch.Rows[0].Result.Records[0])
			if err != nil || statement != record.Statement {
				t.Fatal("historical checkpoint was silently upgraded", err)
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(after, before) {
				t.Fatal("read changed saved historical bytes", err)
			}
		})
	}
}

func TestStatementHistoricalNativeReviewUnchanged(t *testing.T) {
	body, err := os.ReadFile("testdata/source_review.json")
	if err != nil {
		t.Fatal(err)
	}
	if checkpointHash(body) != "sha256:46711eaaf4be85f62b12d41e553ed9ea9e001a9549137f30f6f90a149a4e9ef6" {
		t.Fatal("historical native review fixture changed")
	}
	var original ReviewBundle
	if err := json.Unmarshal(body, &original); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(checkpointDirectory(t), "review.json")
	before := saveReviewTestJSON(t, path, original)
	loaded, err := LoadReview(path)
	if err != nil || !reflect.DeepEqual(loaded, original) {
		t.Fatal("historical native review failed exact validation", err)
	}
	if loaded.Digest != "sha256:4f6042af4c1896038c3f99db3466aec76e882206043878be88101bcddca84858" ||
		loaded.Checkpoint.Digest != "sha256:c2df91ee235e913776cf4c2ce198f54c1510b9e31441f4b5963b5ae59b3cd974" ||
		loaded.Review.SubmissionReceipt.RequestID != "detective-v1-0b5aed87b185b80efff011995c062ce48b9736ebf2fd1dee5ec416541bdcf0c5" {
		t.Fatal("historical review, checkpoint or native request identity was rewritten")
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(after, before) {
		t.Fatal("loading a historical review changed its private file", err)
	}
}

func TestStatementGlobalLimitationsStopNewHandoff(t *testing.T) {
	for _, version := range []string{"0.1.0", "0.1.1", "0.1.2"} {
		t.Run(version, func(t *testing.T) {
			doc, batch := checkpointFixture(t, "\n")
			batch.Extractor.Version = version
			batch.Rows[0].Result.Limitations = []string{"The extraction covers only part of the document."}
			_, checkpointErr := New("mock:limitations", doc, batch, "")
			_, batchErr := NewBatchIndex("mock:limitations", doc, batch, "")
			if version == "0.1.2" {
				if checkpointErr == nil || batchErr == nil {
					t.Fatal("new handoff silently omitted whole-extraction limitations")
				}
			} else if checkpointErr != nil || batchErr != nil {
				t.Fatal("new limitation gate changed historical replay", checkpointErr, batchErr)
			}
		})
	}
}
