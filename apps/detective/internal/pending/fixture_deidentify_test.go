//go:build darwin || linux

package pending

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

const deidentifiedSourcePath = "/synthetic/ahe-core/docs/STATUS.md"

// This explicit maintenance flag only rewrites the seven named test fixtures.
// It never changes a user checkpoint, original experiment, or model response.
var updateDeidentifiedFixtures = flag.Bool("update-deidentified-fixtures", false, "recompute path-deidentified compatibility fixtures using the existing integrity rules")

func deidentifyReviewFixture(bundle ReviewBundle) (ReviewBundle, error) {
	if err := bundle.validate(); err != nil {
		return ReviewBundle{}, err
	}
	bundle.Checkpoint.Batch.Source.Path = deidentifiedSourcePath
	bundle.Receipt.Batch.Source.Path = deidentifiedSourcePath
	canonical, err := bundle.Checkpoint.canonical()
	if err != nil {
		return ReviewBundle{}, err
	}
	bundle.Checkpoint.Digest = checkpointHash(canonical)
	bundle.Receipt.CheckpointDigest = bundle.Checkpoint.Digest
	bundle.Digest, err = reviewHash(bundle.payload())
	if err != nil {
		return ReviewBundle{}, err
	}
	return bundle, bundle.validate()
}

func deidentifyAssessmentFixture(assessment ReasonAssessment) (ReasonAssessment, error) {
	if err := assessment.validate(); err != nil {
		return ReasonAssessment{}, err
	}
	var err error
	assessment.ReviewDecision.ReviewBundle, err = deidentifyReviewFixture(assessment.ReviewDecision.ReviewBundle)
	if err != nil {
		return ReasonAssessment{}, err
	}
	assessment.ReviewDecision.Digest, err = reviewHash(assessment.ReviewDecision.payload())
	if err != nil {
		return ReasonAssessment{}, err
	}
	assessment.DecisionDigest = assessment.ReviewDecision.Digest
	assessment.Digest, err = reasonAssessmentHash(assessment)
	if err != nil {
		return ReasonAssessment{}, err
	}
	return assessment, assessment.validate()
}

func TestDeidentifiedCompatibilityFixtures(t *testing.T) {
	for _, name := range []string{"source_review", "bounded-support", "citation-only", "praise-paraphrase", "audit-bounded", "reject-overclaim", "pending-scope"} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join("testdata", "source_review.json")
			if name != "source_review" {
				path = filepath.Join("testdata", "frozen_reason_v1_v5", name+".assessment.json")
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var deidentified any
			if name == "source_review" {
				var original ReviewBundle
				if err := json.Unmarshal(before, &original); err != nil {
					t.Fatal(err)
				}
				changed, err := deidentifyReviewFixture(original)
				if err != nil {
					t.Fatal(err)
				}
				if changed.Checkpoint.RawText != original.Checkpoint.RawText || !reflect.DeepEqual(changed.Review, original.Review) || !reflect.DeepEqual(changed.Receipt.Handoff, original.Receipt.Handoff) {
					t.Fatal("path de-identification changed source text, MCP review or receipt identity")
				}
				deidentified = changed
			} else {
				var original ReasonAssessment
				if err := json.Unmarshal(before, &original); err != nil {
					t.Fatal(err)
				}
				inputs, err := buildReasonInputs(original.ReviewDecision)
				if err != nil {
					t.Fatal(err)
				}
				changed, err := deidentifyAssessmentFixture(original)
				if err != nil {
					t.Fatal(err)
				}
				afterInputs, err := buildReasonInputs(changed.ReviewDecision)
				if err != nil || !reflect.DeepEqual(inputs, afterInputs) || !reflect.DeepEqual(changed.Concerns, original.Concerns) || changed.Summary != original.Summary || changed.Verdict != original.Verdict || changed.Model != original.Model || changed.ModelDigest != original.ModelDigest || changed.SchemaVersion != original.SchemaVersion || changed.PromptVersion != original.PromptVersion || !reflect.DeepEqual(changed.Thinking, original.Thinking) {
					t.Fatal("path de-identification changed advisory content, exact anchors or format version")
				}
				deidentified = changed
			}
			after, err := json.Marshal(deidentified)
			if err != nil {
				t.Fatal(err)
			}
			after = append(after, '\n')
			if *updateDeidentifiedFixtures {
				if err := os.WriteFile(path, after, 0o644); err != nil {
					t.Fatal(err)
				}
				t.Logf("%s bytes=%d %s", path, len(after), checkpointHash(after))
			} else if !bytes.Equal(before, after) {
				t.Fatal("fixture is not the canonical path-deidentified compatibility copy; use the explicit maintenance flag after reviewing the source")
			}
		})
	}
}

func TestFixtureDeidentificationRejectsInvalidBindings(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("testdata", "source_review.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"bundle_digest", "source_bytes", "receipt_reference", "review_display"} {
		t.Run(kind, func(t *testing.T) {
			var bundle ReviewBundle
			if err := json.Unmarshal(body, &bundle); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "bundle_digest":
				bundle.Digest = "invalid"
			case "source_bytes":
				bundle.Checkpoint.RawText += "changed"
			case "receipt_reference":
				bundle.Receipt.CheckpointDigest = "invalid"
			case "review_display":
				bundle.Review.Display.PayloadUTF8 += "changed"
			}
			if kind != "bundle_digest" {
				bundle.Digest, err = reviewHash(bundle.payload())
				if err != nil {
					t.Fatal(err)
				}
			}
			if _, err := deidentifyReviewFixture(bundle); err == nil {
				t.Fatal("test-only de-identification repaired an invalid input binding")
			}
		})
	}
}
