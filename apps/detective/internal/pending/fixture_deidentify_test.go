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

// This explicit maintenance flag only rewrites the synthetic review fixture.
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

func TestDeidentifiedCompatibilityFixtures(t *testing.T) {
	t.Run("source_review", func(t *testing.T) {
		path := filepath.Join("testdata", "source_review.json")
		before, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
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
		after, err := json.Marshal(changed)
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
