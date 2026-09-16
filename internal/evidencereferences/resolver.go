package evidencereferences

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencegraph"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestion"
)

// Resolve compares a literal reference with independently loaded endpoint and
// observed-cut facts. It performs no I/O and grants no database write authority.
func Resolve(req ReviewRequest, native evidenceingestion.ReferencesNativeBasis) (Resolution, error) {
	if !validCoordinate(req.FromNodeID, "canon-node:", 512) || !validCoordinate(req.ToNodeID, "canon-node:", 512) || req.FromNodeID == req.ToNodeID {
		return Resolution{}, unresolved("endpoints must be distinct bounded canonical node IDs")
	}
	if err := validateEndpoint(native.From, req.FromNodeID); err != nil {
		return Resolution{}, err
	}
	if err := validateEndpoint(native.To, req.ToNodeID); err != nil {
		return Resolution{}, err
	}
	if len(native.TargetCandidateIDs) != 1 || native.TargetCandidateIDs[0] != req.ToNodeID {
		return Resolution{}, unresolved("complete observed target cut must contain exactly the requested target")
	}
	from := native.From.ReviewSnapshot.ReviewPackage.ProposalBasis
	to := native.To.ReviewSnapshot.ReviewPackage.ProposalBasis
	if req.Reference.SourceSnapshotID != from.SourceSnapshotID || req.Reference.ExtractionViewID != from.ExtractionViewID ||
		req.Target.SourceSnapshotID != to.SourceSnapshotID || req.Target.ExtractionViewID != to.ExtractionViewID || !validAnchorID(req.Target.AnchorID) {
		return Resolution{}, unresolved("reference or target coordinates differ from native source")
	}
	var expectedToken string
	switch req.ResolverProfile {
	case ResolverSameSnapshotV1:
		if from.SourceSnapshotID != to.SourceSnapshotID || from.ExtractionViewID != to.ExtractionViewID || native.From.RenderedText != native.To.RenderedText {
			return Resolution{}, unresolved("local reference endpoints must share the exact immutable view")
		}
		expectedToken = "[[ahe-ref:#" + req.Target.AnchorID + "]]"
	case ResolverNativeSnapshotV1:
		if from.SourceSnapshotID == to.SourceSnapshotID || !validSnapshotID(to.SourceSnapshotID) {
			return Resolution{}, unresolved("cross-document reference requires another exact native snapshot")
		}
		expectedToken = "[[ahe-ref:" + to.SourceSnapshotID + "#" + req.Target.AnchorID + "]]"
	default:
		return Resolution{}, unresolved("resolver profile is unsupported")
	}
	if err := validateReference(req.Reference, expectedToken, native.From); err != nil {
		return Resolution{}, err
	}
	// The scan is complete even when the caller points to an early matching line.
	// Otherwise a hidden duplicate later in the same view could select an endpoint.
	anchors, err := scanAnchors(native.To.Spans)
	if err != nil {
		return Resolution{}, err
	}
	span, ok := anchors[req.Target.AnchorID]
	if !ok || span.SpanID != req.Target.SpanID || span.QuotedTextHash != req.Target.SpanHash {
		return Resolution{}, unresolved("anchor does not resolve to the asserted exact target span")
	}
	if len(to.SourceRefs) != 1 || !refMatchesSpan(to.SourceRefs[0], span) {
		return Resolution{}, unresolved("target claim must use exactly the complete anchored line")
	}
	return Resolution{
		ContractVersion: ResolutionContractV1, ResolverProfile: req.ResolverProfile,
		FromNodeID: req.FromNodeID, ToNodeID: req.ToNodeID, Reference: req.Reference, Target: req.Target,
		TargetCut: TargetCut{ContractVersion: TargetCutContractV1, SourceSnapshotID: to.SourceSnapshotID,
			ExtractionViewID: to.ExtractionViewID, SpanID: span.SpanID, StartByte: span.StartByte, EndByte: span.EndByte,
			SpanHash: span.QuotedTextHash, CandidateIDs: slices.Clone(native.TargetCandidateIDs)},
	}, nil
}

func validateEndpoint(endpoint evidenceingestion.ReferencesNativeEndpoint, nodeID string) error {
	basis := endpoint.ReviewSnapshot.ReviewPackage.ProposalBasis
	p := endpoint.Node.OriginProposal
	if endpoint.Node.CanonicalID != nodeID || endpoint.Node.NodeKind != evidencegraph.CanonicalSourceClaim ||
		p.SourceBindingKind != evidenceingestion.ProposalSourceBindingSourceSnapshot ||
		basis.SourceSnapshotID != p.SourceSnapshotID || basis.ExtractionViewID != p.ExtractionViewID || basis.SourceSystem != p.SourceSystem ||
		basis.StatementText != endpoint.Node.Payload.Claim || !reflect.DeepEqual(basis.SourceRefs, p.SourceRefs) || len(basis.SourceRefs) < 1 || len(basis.SourceRefs) > 64 {
		return unresolved("endpoint differs from its native source claim and exact review basis")
	}
	if basis.SourceTitle == "" || basis.SourceLocation == "" || basis.SourceLimitations == nil {
		return unresolved("endpoint lacks bounded qualified source review metadata")
	}
	var renderer, catalog string
	switch basis.SourceSystem {
	case evidenceingestion.SourceSystemManualText:
		profile := basis.ManualReviewProfile
		if profile == nil || profile.SourceSnapshotID != basis.SourceSnapshotID || evidenceingestion.ValidateManualReviewProfileBinding(*profile) != nil ||
			profile.Profile.Title != basis.SourceTitle || profile.Profile.Location != basis.SourceLocation || profile.Profile.Coverage != basis.SourceCoverage ||
			!slices.Equal(profile.Profile.Limitations, basis.SourceLimitations) {
			return unresolved("manual endpoint lacks its native first-capture review binding")
		}
		renderer, catalog = evidenceingestion.RendererManualTextIdentity, evidenceingestion.SpanCatalogManualLineV1
	case evidenceingestion.SourceSystemExternalDocument:
		if basis.ManualReviewProfile != nil {
			return unresolved("external endpoint cannot use a manual review binding")
		}
		renderer, catalog = evidenceingestion.RendererExternalDocumentIdentity, evidenceingestion.SpanCatalogExternalDocumentLineV1
	default:
		return unresolved("endpoint source profile is unsupported")
	}
	if !validSnapshotID(basis.SourceSnapshotID) || !validCoordinate(basis.ExtractionViewID, "view:", 200) ||
		basis.RendererName != renderer || basis.RendererVersion != "v1" ||
		len(endpoint.RawText) > int(evidenceingestion.BoundedSourceViewMaxRenderedBytesV1) ||
		endpoint.RawText != endpoint.RenderedText || !utf8.ValidString(endpoint.RawText) || strings.ContainsRune(endpoint.RawText, 0) ||
		textHash(endpoint.RawText) != basis.RawContentHash || basis.RawContentHash != basis.RenderedContentHash {
		return unresolved("endpoint does not contain an exact bounded identity-rendered view")
	}
	if err := validateCompleteSpans(endpoint.RenderedText, basis.ExtractionViewID, catalog, endpoint.Spans); err != nil {
		return err
	}
	for _, ref := range basis.SourceRefs {
		found := false
		for _, span := range endpoint.Spans {
			if refMatchesSpan(ref, span) {
				found = true
				break
			}
		}
		if !found {
			return unresolved("endpoint source reference differs from the complete catalog")
		}
	}
	return nil
}

func validateCompleteSpans(text, viewID, catalog string, spans []evidenceingestion.SpanEntry) error {
	if len(spans) > int(evidenceingestion.BoundedSourceViewMaxSpansV1) {
		return unresolved("source span catalog exceeds the complete scan bound")
	}
	index, start, line := 0, 0, 1
	for start <= len(text) {
		end := len(text)
		if newline := strings.IndexByte(text[start:], '\n'); newline >= 0 {
			end = start + newline
		}
		contentEnd := end
		if contentEnd > start && text[contentEnd-1] == '\r' {
			contentEnd--
		}
		if contentEnd > start {
			if index >= len(spans) || contentEnd-start > int(evidenceingestion.BoundedSourceViewMaxSpanBytesV1) {
				return unresolved("source catalog omits a line or exceeds the atomic span bound")
			}
			span := spans[index]
			if span.ExtractionViewID != viewID || span.SpanCatalogVersion != catalog || span.SpanID != "span:S"+strconv.Itoa(index+1) ||
				span.StartByte != start || span.EndByte != contentEnd || span.DisplayLine != line ||
				span.QuotedText != text[start:contentEnd] || span.QuotedTextHash != textHash(text[start:contentEnd]) {
				return unresolved("complete source catalog differs from original line bytes")
			}
			index++
		}
		if end == len(text) {
			break
		}
		start, line = end+1, line+1
	}
	if index != len(spans) {
		return unresolved("source catalog contains extra spans")
	}
	return nil
}

func validateReference(ref ReferenceWitness, expected string, from evidenceingestion.ReferencesNativeEndpoint) error {
	if ref.Token != expected || len(ref.Token) > MaxTokenBytes || ref.TokenHash != textHash(ref.Token) ||
		ref.StartByte < 0 || ref.EndByte <= ref.StartByte || ref.EndByte > len(from.RenderedText) ||
		from.RenderedText[ref.StartByte:ref.EndByte] != ref.Token {
		return unresolved("reference token differs from its exact pinned bytes")
	}
	for _, span := range from.Spans {
		if span.SpanID != ref.SpanID || ref.StartByte < span.StartByte || ref.EndByte > span.EndByte {
			continue
		}
		for _, sourceRef := range from.Node.OriginProposal.SourceRefs {
			if refMatchesSpan(sourceRef, span) {
				return nil
			}
		}
	}
	return unresolved("reference token is not inside the source claim's exact evidence span")
}

func scanAnchors(spans []evidenceingestion.SpanEntry) (map[string]evidenceingestion.SpanEntry, error) {
	const prefix = "[[ahe-anchor:"
	anchors := make(map[string]evidenceingestion.SpanEntry)
	for _, span := range spans {
		position := strings.Index(span.QuotedText, prefix)
		if position < 0 {
			continue
		}
		if position != 0 || strings.Count(span.QuotedText, prefix) != 1 {
			return nil, unresolved("anchor marker must occur once at the start of its line")
		}
		suffix := span.QuotedText[len(prefix):]
		close := strings.Index(suffix, "]]")
		if close < 0 || !validAnchorID(suffix[:close]) || !strings.HasPrefix(suffix[close+2:], " ") || strings.TrimSpace(suffix[close+3:]) == "" {
			return nil, unresolved("anchor marker has malformed ID or missing line content")
		}
		id := suffix[:close]
		if _, exists := anchors[id]; exists {
			return nil, unresolved("anchor ID is duplicated in the complete source view")
		}
		anchors[id] = span
	}
	return anchors, nil
}

func validAnchorID(id string) bool {
	if len(id) < 1 || len(id) > 64 || !asciiLetter(id[0]) {
		return false
	}
	for i := 1; i < len(id); i++ {
		c := id[i]
		if !asciiLetter(c) && (c < '0' || c > '9') && c != '.' && c != '_' && c != '-' {
			return false
		}
	}
	return true
}

func asciiLetter(c byte) bool { return c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' }

func validSnapshotID(id string) bool {
	if len(id) != len("srcsnap:")+64 || !strings.HasPrefix(id, "srcsnap:") {
		return false
	}
	for _, c := range id[len("srcsnap:"):] {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func validCoordinate(id, prefix string, limit int) bool {
	return strings.HasPrefix(id, prefix) && len(id) > len(prefix) && len(id) <= limit && strings.TrimSpace(id) == id && utf8.ValidString(id) && !strings.ContainsRune(id, 0)
}

func refMatchesSpan(ref evidenceingestion.ResolvedSourceRef, span evidenceingestion.SpanEntry) bool {
	return ref == (evidenceingestion.ResolvedSourceRef{ExtractionViewID: span.ExtractionViewID, SpanID: span.SpanID,
		StartByte: span.StartByte, EndByte: span.EndByte, QuotedTextHash: span.QuotedTextHash, QuotedText: span.QuotedText})
}

func textHash(text string) string {
	hash := sha256.Sum256([]byte(text))
	return "sha256:" + hex.EncodeToString(hash[:])
}

func unresolved(reason string) error { return fmt.Errorf("%w: %s", ErrUnresolved, reason) }
