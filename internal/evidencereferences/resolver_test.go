package evidencereferences

import (
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencegraph"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestion"
)

func TestResolveSameSnapshotAndPinnedCrossDocument(t *testing.T) {
	for _, profile := range []string{ResolverSameSnapshotV1, ResolverNativeSnapshotV1} {
		for _, manual := range []bool{false, true} {
			t.Run(profile+"/manual="+strconv.FormatBool(manual), func(t *testing.T) {
				req, native := resolverFixture(t, profile, manual, "")
				result, err := Resolve(req, native)
				if err != nil {
					t.Fatal(err)
				}
				if result.ResolverProfile != profile || result.Reference != req.Reference || result.Target != req.Target ||
					!reflect.DeepEqual(result.TargetCut.CandidateIDs, []string{req.ToNodeID}) || result.TargetCut.SpanID != req.Target.SpanID {
					t.Fatalf("unexpected resolution: %+v", result)
				}
				native.TargetCandidateIDs[0] = "caller mutation"
				if result.TargetCut.CandidateIDs[0] != req.ToNodeID {
					t.Fatal("resolution aliases caller candidate cut")
				}
			})
		}
	}
}

func TestResolveRejectsAlteredCoordinatesAndAuthority(t *testing.T) {
	for _, profile := range []string{ResolverSameSnapshotV1, ResolverNativeSnapshotV1} {
		for name, mutate := range map[string]func(*ReviewRequest, *evidenceingestion.ReferencesNativeBasis){
			"unsupported profile": func(r *ReviewRequest, _ *evidenceingestion.ReferencesNativeBasis) { r.ResolverProfile = "url/v1" },
			"self edge":           func(r *ReviewRequest, _ *evidenceingestion.ReferencesNativeBasis) { r.ToNodeID = r.FromNodeID },
			"wrong node":          func(r *ReviewRequest, _ *evidenceingestion.ReferencesNativeBasis) { r.ToNodeID += "changed" },
			"wrong source": func(r *ReviewRequest, _ *evidenceingestion.ReferencesNativeBasis) {
				r.Reference.SourceSnapshotID += "changed"
			},
			"wrong target version": func(r *ReviewRequest, _ *evidenceingestion.ReferencesNativeBasis) {
				r.Target.SourceSnapshotID += "changed"
			},
			"wrong view": func(r *ReviewRequest, _ *evidenceingestion.ReferencesNativeBasis) {
				r.Reference.ExtractionViewID += "changed"
			},
			"wrong reference span": func(r *ReviewRequest, _ *evidenceingestion.ReferencesNativeBasis) { r.Reference.SpanID = "span:S99" },
			"wrong target span":    func(r *ReviewRequest, _ *evidenceingestion.ReferencesNativeBasis) { r.Target.SpanID = "span:S99" },
			"wrong token hash": func(r *ReviewRequest, _ *evidenceingestion.ReferencesNativeBasis) {
				r.Reference.TokenHash = textHash("wrong")
			},
			"wrong target hash": func(r *ReviewRequest, _ *evidenceingestion.ReferencesNativeBasis) {
				r.Target.SpanHash = textHash("wrong")
			},
			"utf8 offset":     func(r *ReviewRequest, _ *evidenceingestion.ReferencesNativeBasis) { r.Reference.StartByte-- },
			"negative offset": func(r *ReviewRequest, _ *evidenceingestion.ReferencesNativeBasis) { r.Reference.StartByte = -1 },
			"past end":        func(r *ReviewRequest, _ *evidenceingestion.ReferencesNativeBasis) { r.Reference.EndByte = 1 << 30 },
			"case fold":       func(r *ReviewRequest, _ *evidenceingestion.ReferencesNativeBasis) { r.Target.AnchorID = "Refund-rule" },
			"fuzzy heading":   func(r *ReviewRequest, _ *evidenceingestion.ReferencesNativeBasis) { r.Target.AnchorID = "refund rule" },
			"bare url": func(r *ReviewRequest, _ *evidenceingestion.ReferencesNativeBasis) {
				r.Reference.Token = "https://example.test/"
				r.Reference.TokenHash = textHash(r.Reference.Token)
			},
			"overlong token": func(r *ReviewRequest, _ *evidenceingestion.ReferencesNativeBasis) {
				r.Reference.Token = strings.Repeat("x", MaxTokenBytes+1)
			},
			"unobserved cut": func(_ *ReviewRequest, n *evidenceingestion.ReferencesNativeBasis) { n.TargetCandidateIDs = nil },
			"second claim": func(_ *ReviewRequest, n *evidenceingestion.ReferencesNativeBasis) {
				n.TargetCandidateIDs = append(n.TargetCandidateIDs, "canon-node:other")
			},
			"duplicate candidate": func(_ *ReviewRequest, n *evidenceingestion.ReferencesNativeBasis) {
				n.TargetCandidateIDs = append(n.TargetCandidateIDs, n.To.Node.CanonicalID)
			},
			"cut overflow": func(_ *ReviewRequest, n *evidenceingestion.ReferencesNativeBasis) {
				n.TargetCandidateIDs = make([]string, 65)
			},
			"raw mutation": func(_ *ReviewRequest, n *evidenceingestion.ReferencesNativeBasis) { n.To.RawText += "wrong" },
			"derived endpoint": func(_ *ReviewRequest, n *evidenceingestion.ReferencesNativeBasis) {
				n.To.Node.NodeKind = evidencegraph.CanonicalDerivedClaim
			},
			"missing title": func(_ *ReviewRequest, n *evidenceingestion.ReferencesNativeBasis) {
				n.To.ReviewSnapshot.ReviewPackage.ProposalBasis.SourceTitle = ""
			},
			"metadata identity is not anchor": func(r *ReviewRequest, n *evidenceingestion.ReferencesNativeBasis) {
				r.Target.AnchorID = "location-only"
				n.To.ReviewSnapshot.ReviewPackage.ProposalBasis.SourceLocation = "location-only"
			},
			"incomplete catalog":   func(_ *ReviewRequest, n *evidenceingestion.ReferencesNativeBasis) { n.To.Spans = n.To.Spans[:1] },
			"catalog wrong offset": func(_ *ReviewRequest, n *evidenceingestion.ReferencesNativeBasis) { n.To.Spans[0].StartByte++ },
			"catalog wrong hash": func(_ *ReviewRequest, n *evidenceingestion.ReferencesNativeBasis) {
				n.To.Spans[0].QuotedTextHash = textHash("wrong")
			},
			"reference outside claim": func(_ *ReviewRequest, n *evidenceingestion.ReferencesNativeBasis) {
				n.From.Node.OriginProposal.SourceRefs = n.To.Node.OriginProposal.SourceRefs
				n.From.ReviewSnapshot.ReviewPackage.ProposalBasis.SourceRefs = n.To.Node.OriginProposal.SourceRefs
			},
			"target multiple refs": func(_ *ReviewRequest, n *evidenceingestion.ReferencesNativeBasis) {
				n.To.Node.OriginProposal.SourceRefs = append(n.To.Node.OriginProposal.SourceRefs, n.To.Node.OriginProposal.SourceRefs[0])
				n.To.ReviewSnapshot.ReviewPackage.ProposalBasis.SourceRefs = n.To.Node.OriginProposal.SourceRefs
			},
		} {
			t.Run(profile+"/"+name, func(t *testing.T) {
				req, native := resolverFixture(t, profile, false, "")
				mutate(&req, &native)
				result, err := Resolve(req, native)
				if !errors.Is(err, ErrUnresolved) || !reflect.DeepEqual(result, Resolution{}) {
					t.Fatalf("expected closed refusal, got %+v, %v", result, err)
				}
			})
		}
	}
}

func TestResolveScansWholeViewForDuplicateAndMalformedAnchors(t *testing.T) {
	for _, extra := range []string{
		"[[ahe-anchor:refund-rule]] Hidden duplicate.",
		" [[ahe-anchor:refund-rule]] Indented.",
		"[[ahe-anchor:refund-rule]]",
		"[[ahe-anchor:refund-rule]] ",
		"[[ahe-anchor:refund-rule]]\tMissing space.",
		"[[ahe-anchor:refund-rule] Broken closing.",
		"[[ahe-anchor:1wrong]] Invalid first character.",
		"[[ahe-anchor:refund-rule]] Text [[ahe-anchor:other]] Another.",
	} {
		t.Run(extra, func(t *testing.T) {
			req, native := resolverFixture(t, ResolverSameSnapshotV1, false, extra)
			if _, err := Resolve(req, native); !errors.Is(err, ErrUnresolved) {
				t.Fatalf("hidden/malformed anchor accepted: %v", err)
			}
		})
	}
}

func TestResolveManualProfileAndCatalogBounds(t *testing.T) {
	req, native := resolverFixture(t, ResolverSameSnapshotV1, true, "")
	native.To.ReviewSnapshot.ReviewPackage.ProposalBasis.ManualReviewProfile = nil
	if _, err := Resolve(req, native); !errors.Is(err, ErrUnresolved) {
		t.Fatal("legacy manual acquired anchor eligibility")
	}
	for _, extra := range []string{strings.Repeat("x", 8193), strings.Repeat("x\n", 4096), strings.Repeat("x", 1<<20)} {
		req, native := resolverFixture(t, ResolverSameSnapshotV1, false, extra)
		if _, err := Resolve(req, native); !errors.Is(err, ErrUnresolved) {
			t.Fatal("oversize view/line/catalog accepted")
		}
	}
	for _, anchor := range []string{"", "1wrong", "a b", "a#b", "中文", strings.Repeat("a", 65)} {
		if validAnchorID(anchor) {
			t.Fatalf("invalid anchor accepted: %q", anchor)
		}
	}
	if !validAnchorID("A" + strings.Repeat("b", 63)) {
		t.Fatal("exact anchor bound rejected")
	}
}

// These fixtures simulate already-reloaded source authority. They do not pretend
// to exercise admission or PostgreSQL. Native integration remains unwired.
func resolverFixture(t *testing.T, profile string, manual bool, extra string) (ReviewRequest, evidenceingestion.ReferencesNativeBasis) {
	t.Helper()
	snapshot := "srcsnap:" + strings.Repeat("a", 64)
	other := "srcsnap:" + strings.Repeat("b", 64)
	token := "[[ahe-ref:#refund-rule]]"
	if profile == ResolverNativeSnapshotV1 {
		token = "[[ahe-ref:" + snapshot + "#refund-rule]]"
	}
	text := "[[ahe-anchor:refund-rule]] 退款期限三十日。\r\n\r\n  \r\n請參考 " + token + "\r\n"
	if extra != "" {
		text += extra + "\n"
	}
	to := resolverEndpoint(t, "canon-node:to", snapshot, text, 0, manual)
	from := resolverEndpoint(t, "canon-node:from", snapshot, text, 2, manual)
	if profile == ResolverNativeSnapshotV1 {
		from = resolverEndpoint(t, "canon-node:from", other, "請參考 "+token+"\n", 0, manual)
	}
	ref := from.Node.OriginProposal.SourceRefs[0]
	start := strings.Index(from.RenderedText, token)
	req := ReviewRequest{ResolverProfile: profile, FromNodeID: from.Node.CanonicalID, ToNodeID: to.Node.CanonicalID,
		Reference: ReferenceWitness{SourceSnapshotID: from.Node.OriginProposal.SourceSnapshotID, ExtractionViewID: ref.ExtractionViewID,
			SpanID: ref.SpanID, StartByte: start, EndByte: start + len(token), Token: token, TokenHash: textHash(token)},
		Target: TargetWitness{SourceSnapshotID: snapshot, ExtractionViewID: to.Spans[0].ExtractionViewID,
			SpanID: to.Spans[0].SpanID, AnchorID: "refund-rule", SpanHash: to.Spans[0].QuotedTextHash}}
	return req, evidenceingestion.ReferencesNativeBasis{From: from, To: to, TargetCandidateIDs: []string{to.Node.CanonicalID}}
}

func resolverEndpoint(t *testing.T, id, snapshot, text string, refIndex int, manual bool) evidenceingestion.ReferencesNativeEndpoint {
	t.Helper()
	system, renderer, catalog := evidenceingestion.SourceSystemExternalDocument, evidenceingestion.RendererExternalDocumentIdentity, evidenceingestion.SpanCatalogExternalDocumentLineV1
	if manual {
		system, renderer, catalog = evidenceingestion.SourceSystemManualText, evidenceingestion.RendererManualTextIdentity, evidenceingestion.SpanCatalogManualLineV1
	}
	view := "view:" + strings.TrimPrefix(snapshot, "srcsnap:")
	spans := make([]evidenceingestion.SpanEntry, 0, strings.Count(text, "\n")+1)
	offset := 0
	for i, line := range strings.Split(text, "\n") {
		content := strings.TrimSuffix(line, "\r")
		if len(content) > 0 {
			spans = append(spans, evidenceingestion.SpanEntry{ExtractionViewID: view, SpanID: "span:S" + strconv.Itoa(len(spans)+1),
				SpanCatalogVersion: catalog, StartByte: offset, EndByte: offset + len(content), DisplayLine: i + 1,
				QuotedTextHash: textHash(content), QuotedText: content})
		}
		offset += len(line) + 1
	}
	span := spans[refIndex]
	refs := []evidenceingestion.ResolvedSourceRef{{ExtractionViewID: span.ExtractionViewID, SpanID: span.SpanID,
		StartByte: span.StartByte, EndByte: span.EndByte, QuotedTextHash: span.QuotedTextHash, QuotedText: span.QuotedText}}
	p := evidenceingestion.ProposalQueryResult{SourceSnapshotID: snapshot, ExtractionViewID: view, SourceSystem: system,
		SourceBindingKind: evidenceingestion.ProposalSourceBindingSourceSnapshot, SourceRefs: refs}
	basis := evidenceingestion.ProposalBasis{SourceSnapshotID: snapshot, ExtractionViewID: view, SourceSystem: system,
		StatementText: id, SourceRefs: refs, RawContentHash: textHash(text), RenderedContentHash: textHash(text),
		RendererName: renderer, RendererVersion: "v1", SourceTitle: "Submitted source", SourceLocation: "manual:test",
		SourceCoverage: "full_document", SourceLimitations: []string{}}
	if manual {
		profile := evidenceingestion.ManualReviewProfileInput{ContractVersion: evidenceingestion.ManualSourceReviewProfileV1,
			Title: basis.SourceTitle, Location: basis.SourceLocation, Coverage: evidenceingestion.ManualReviewCoverageSubmittedText, Limitations: []string{}}
		data, err := json.Marshal(profile)
		if err != nil {
			t.Fatal(err)
		}
		basis.SourceCoverage = profile.Coverage
		basis.ManualReviewProfile = &evidenceingestion.ManualReviewProfileBinding{SourceSnapshotID: snapshot, IntakeRequestID: "fixture",
			ContractVersion: evidenceingestion.ManualSourceReviewProfileV1, Profile: profile, ProfileHash: textHash(string(data))}
	}
	return evidenceingestion.ReferencesNativeEndpoint{
		Node: evidenceingestion.CanonicalQueryResult{CanonicalID: id, NodeKind: evidencegraph.CanonicalSourceClaim,
			Payload: evidencegraph.EvidencePayload{Claim: id}, OriginProposal: p},
		ReviewSnapshot: evidenceingestion.ReviewableSourceClaimReviewSnapshot{ReviewPackage: evidenceingestion.ReviewPackage{ProposalBasis: basis}},
		RawText:        text, RenderedText: text, Spans: spans,
	}
}
