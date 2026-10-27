package evidenceingestion

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func manualReviewProfileFixture() *ManualReviewProfileInput {
	return &ManualReviewProfileInput{ContractVersion: ManualSourceReviewProfileV1,
		Title: "Manual refund statement", Location: "manual:refund-policy/v1",
		Coverage: ManualReviewCoverageSubmittedText, Limitations: []string{}}
}

func TestManualReviewProfileInputContract(t *testing.T) {
	for _, tc := range []struct {
		name  string
		edit  func(*ManualReviewProfileInput)
		valid bool
	}{
		{"submitted explicit empty array", func(*ManualReviewProfileInput) {}, true},
		{"submitted limitation", func(p *ManualReviewProfileInput) {
			p.Limitations = []string{"Only this submitted statement is covered."}
		}, true},
		{"exact excerpt", func(p *ManualReviewProfileInput) {
			p.Coverage = ManualReviewCoverageExactExcerpt
			p.Limitations = []string{"The surrounding discussion is not provided."}
		}, true},
		{"unknown contract", func(p *ManualReviewProfileInput) { p.ContractVersion += "-future" }, false},
		{"empty title", func(p *ManualReviewProfileInput) { p.Title = "" }, false},
		{"blank location", func(p *ManualReviewProfileInput) { p.Location = " " }, false},
		{"padded title", func(p *ManualReviewProfileInput) { p.Title += " " }, false},
		{"invalid UTF8", func(p *ManualReviewProfileInput) { p.Title = string([]byte{0xff}) }, false},
		{"NUL location", func(p *ManualReviewProfileInput) { p.Location += "\x00" }, false},
		{"title maximum", func(p *ManualReviewProfileInput) { p.Title = strings.Repeat("x", ManualReviewTitleMaxBytes) }, true},
		{"title overflow", func(p *ManualReviewProfileInput) { p.Title = strings.Repeat("x", ManualReviewTitleMaxBytes+1) }, false},
		{"location maximum", func(p *ManualReviewProfileInput) { p.Location = strings.Repeat("x", ManualReviewLocationMaxBytes) }, true},
		{"location overflow", func(p *ManualReviewProfileInput) { p.Location = strings.Repeat("x", ManualReviewLocationMaxBytes+1) }, false},
		{"nil limitations", func(p *ManualReviewProfileInput) { p.Limitations = nil }, false},
		{"empty limitation", func(p *ManualReviewProfileInput) { p.Limitations = []string{""} }, false},
		{"limitation overflow", func(p *ManualReviewProfileInput) {
			p.Limitations = []string{strings.Repeat("x", ManualReviewLimitationMaxBytes+1)}
		}, false},
		{"NUL limitation", func(p *ManualReviewProfileInput) { p.Limitations = []string{"not\x00valid"} }, false},
		{"too many limitations", func(p *ManualReviewProfileInput) { p.Limitations = make([]string, ManualReviewLimitationsMaxCount+1) }, false},
		{"exact excerpt empty limitations", func(p *ManualReviewProfileInput) { p.Coverage = ManualReviewCoverageExactExcerpt }, false},
		{"external full document is not manual coverage", func(p *ManualReviewProfileInput) { p.Coverage = "full_document" }, false},
		{"canonical escaped bytes overflow", func(p *ManualReviewProfileInput) {
			p.Limitations = make([]string, ManualReviewLimitationsMaxCount)
			for i := range p.Limitations {
				p.Limitations[i] = strings.Repeat("\x01", ManualReviewLimitationMaxBytes)
			}
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			profile := manualReviewProfileFixture()
			tc.edit(profile)
			err := ValidateManualReviewProfileInput(*profile)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v error=%v", tc.valid, err)
			}
		})
	}
}

func TestManualReviewProfileLegacyHashAndDetachedBody(t *testing.T) {
	input := testManualInput("manual-profile-identity")
	legacy, err := buildManualSourceContext(input)
	if err != nil {
		t.Fatal(err)
	}
	// This literal is the pre-profile v1 intake payload shape; nil must omit the
	// new member entirely, not encode null or introduce a profile into identity.
	wantData, err := deterministicJSON(struct {
		SourceSystem        string            `json:"source_system"`
		SourceID            string            `json:"source_id"`
		SourceVersion       string            `json:"source_version"`
		RawContentHash      string            `json:"raw_content_hash"`
		OriginMetadata      map[string]string `json:"origin_metadata"`
		RendererName        string            `json:"renderer_name"`
		RendererVersion     string            `json:"renderer_version"`
		RenderedContentHash string            `json:"rendered_content_hash"`
		SpanCatalogVersion  string            `json:"span_catalog_version"`
	}{legacy.SourceSnapshot.SourceSystem, input.SourceID, input.SourceVersion, legacy.SourceSnapshot.RawContentHash,
		input.OriginMetadata, legacy.ExtractionView.RendererName, legacy.ExtractionView.RendererVersion,
		legacy.ExtractionView.RenderedContentHash, legacy.SpanCatalogVersion})
	if err != nil || contentHash(wantData) != legacy.RequestPayloadHash || legacy.ManualReviewProfile != nil {
		t.Fatalf("legacy hash changed: %v", err)
	}
	input.ManualReviewProfile = manualReviewProfileFixture()
	input.ManualReviewProfile.Limitations = []string{"Only the submitted text is present."}
	qualified, err := buildManualSourceContext(input)
	if err != nil {
		t.Fatal(err)
	}
	if qualified.SourceSnapshot.ID != legacy.SourceSnapshot.ID || qualified.RequestPayloadHash == legacy.RequestPayloadHash {
		t.Fatal("profile changed snapshot identity or was absent from intake hash")
	}
	input.ManualReviewProfile.Limitations[0] = "caller mutation"
	if qualified.ManualReviewProfile.Profile.Limitations[0] == "caller mutation" {
		t.Fatal("profile retained caller-owned slice")
	}
	input.SourceSystem = SourceSystemExternalDocument
	if _, err := buildManualSourceContext(input); err == nil {
		t.Fatal("external source opted into manual profile")
	}
}

func TestManualReviewProfileNativeBindingAndReplay(t *testing.T) {
	ctx := context.Background()
	db := newMockSQLDB()
	input := testManualInput("manual-profile-replay")
	input.ManualReviewProfile = manualReviewProfileFixture()
	first, err := captureManualSource(ctx, db, input)
	if err != nil {
		t.Fatal(err)
	}
	profile, err := loadManualReviewProfile(ctx, db, first.SourceSnapshotID)
	if err != nil || profile == nil {
		t.Fatalf("load profile: %v", err)
	}
	second, err := captureManualSource(ctx, db, input)
	if err != nil || !second.Replayed {
		t.Fatalf("exact replay: %+v %v", second, err)
	}
	input.RequestID += "-other"
	if _, err := captureManualSource(ctx, db, input); err != nil {
		t.Fatal(err)
	}
	reloaded, err := loadManualReviewProfile(ctx, db, first.SourceSnapshotID)
	if err != nil || !reflect.DeepEqual(profile, reloaded) {
		t.Fatalf("new request replaced first profile authority: %v", err)
	}
	input.ManualReviewProfile.Title += " changed"
	if _, err := captureManualSource(ctx, db, input); err == nil {
		t.Fatal("same identity changed its profile")
	}
	if len(db.manualReviewProfiles) != 1 {
		t.Fatal("profile replay appended a second binding")
	}
	input.ManualReviewProfile = nil
	input.RequestID += "-strip"
	if _, err := captureManualSource(ctx, db, input); err == nil {
		t.Fatal("qualified snapshot replay silently omitted its profile")
	}
	view, err := loadManualSourceContext(ctx, db, first.SourceSnapshotID, first.ExtractionViewID)
	if err != nil || !reflect.DeepEqual(view.ManualReviewProfile, profile) {
		t.Fatalf("source context lost profile: %v", err)
	}
	viewByID, err := loadManualSourceContextByViewID(ctx, db, first.ExtractionViewID)
	if err != nil || !reflect.DeepEqual(viewByID.ManualReviewProfile, profile) {
		t.Fatalf("view context lost profile: %v", err)
	}
}

func TestManualReviewProfileNoRetrofitOrLegacyCombinedEntry(t *testing.T) {
	db := newMockSQLDB()
	input := testManualInput("manual-profile-no-retrofit")
	input.OriginMetadata = map[string]string{"contract_version": ManualSourceReviewProfileV1, "title": "Not a typed profile", "location": "manual:unqualified"}
	source, err := captureManualSource(context.Background(), db, input)
	if err != nil {
		t.Fatal(err)
	}
	profile, err := loadManualReviewProfile(context.Background(), db, source.SourceSnapshotID)
	if err != nil || profile != nil {
		t.Fatalf("metadata keys became qualification: %v", err)
	}
	input.ManualReviewProfile = manualReviewProfileFixture()
	input.RequestID += "-qualified"
	if _, err := captureManualSource(context.Background(), db, input); err == nil {
		t.Fatal("old snapshot acquired a profile")
	}
	if len(db.manualReviewProfiles) != 0 || len(db.sourceIntakeRequests) != 1 {
		t.Fatal("rejected retrofit changed profile or request records")
	}
	if _, err := ingestManualText(context.Background(), nil, input, testFixture()); err == nil {
		t.Fatal("combined legacy ingest accepted typed profile")
	}
	if _, err := LoadManualReviewProfileInTx(context.Background(), nil, source.SourceSnapshotID); err == nil {
		t.Fatal("nil transaction accepted")
	}
}

func TestManualReviewProfileCorruptionFailsClosed(t *testing.T) {
	for _, name := range []string{"contract", "hash", "noncanonical bytes", "unknown field", "wrong intake", "wrong source", "oversize", "invalid body"} {
		t.Run(name, func(t *testing.T) {
			db := newMockSQLDB()
			input := testManualInput("manual-profile-corrupt")
			input.ManualReviewProfile = manualReviewProfileFixture()
			source, err := captureManualSource(context.Background(), db, input)
			if err != nil {
				t.Fatal(err)
			}
			profile := db.manualReviewProfiles[source.SourceSnapshotID]
			switch name {
			case "contract":
				profile.contractVersion += "-other"
			case "hash":
				profile.hash = contentHash([]byte("wrong"))
			case "noncanonical bytes":
				profile.payload += " "
			case "unknown field":
				var body map[string]any
				if err := json.Unmarshal([]byte(profile.payload), &body); err != nil {
					t.Fatal(err)
				}
				body["approved"] = true
				data, _ := json.Marshal(body)
				profile.payload = string(data)
			case "wrong intake":
				profile.intakeRequestID = "missing"
			case "wrong source":
				snapshot := db.sourceSnapshots[source.SourceSnapshotID]
				snapshot.sourceSystem = SourceSystemExternalDocument
				db.sourceSnapshots[source.SourceSnapshotID] = snapshot
			case "oversize":
				profile.payload = strings.Repeat("x", ManualReviewProfileMaxBytes+1)
			case "invalid body":
				profile.payload = "null"
			}
			db.manualReviewProfiles[source.SourceSnapshotID] = profile
			if _, err := loadManualReviewProfile(context.Background(), db, source.SourceSnapshotID); err == nil {
				t.Fatal("corrupt profile was accepted or treated as legacy")
			}
		})
	}
}
