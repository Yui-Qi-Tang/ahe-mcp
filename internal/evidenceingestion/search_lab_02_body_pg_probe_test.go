//go:build integration && labreplay

package evidenceingestion

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"
)

const searchLab02PGBodyQuery = "replacement"

type searchLab02PGBodyWitness struct {
	Ref                ResolvedSourceRef
	Role               string
	WithinOriginalRefs bool
}

type searchLab02PGBodyHit struct {
	OriginalProposal ProposalQueryResult
	StatementMatched bool
	Witnesses        []searchLab02PGBodyWitness
	OmittedSpanIDs   []string
}

type searchLab02PGBodyResult struct {
	Hits       []searchLab02PGBodyHit
	ScopedRows int
	OmittedIDs []string
	Complete   bool
	Isolation  string
	ReadOnly   string
	Snapshot   string
}

type searchLab02PGBodyError struct {
	Code string
}

func (e *searchLab02PGBodyError) Error() string { return "search lab 02 body: " + e.Code }

// searchLab02PGBody is a disposable integration-only composition, not a native
// retrieval mode. Each call owns one read-only snapshot; afterList is solely a
// test synchronization point, never a runtime hook. Limit in input is replaced
// by the fixed 100-row overflow probe; limit bounds the final Lab hits instead.
func searchLab02PGBody(
	ctx context.Context,
	db sqlDB,
	input fullLabSearchInput,
	mode string,
	limit int,
	afterList func(sqlTx) error,
) (searchLab02PGBodyResult, error) {
	input = fullLabNormalizeSearchInput(input)
	if db == nil || !hasStableIDPrefix(input.SourceSnapshotID, "srcsnap:") ||
		input.RepositorySnapshotID != "" || input.SourceGenerationID != "" ||
		input.ExcludeCanonicalCandidates || limit < 1 || limit > 64 {
		return searchLab02PGBodyResult{}, &searchLab02PGBodyError{Code: "unsupported_request"}
	}
	if mode != "statement" && mode != "cited_refs" && mode != "source_body" {
		return searchLab02PGBodyResult{}, &searchLab02PGBodyError{Code: "unsupported_mode"}
	}
	if err := validateProposalListInput(input.native()); err != nil {
		return searchLab02PGBodyResult{}, err
	}
	input.Limit = 100
	result := searchLab02PGBodyResult{Complete: true}
	err := withReadOnlyTx(ctx, db, func(tx sqlTx) error {
		if err := tx.queryRow(ctx, `SHOW transaction_isolation`).Scan(&result.Isolation); err != nil {
			return fmt.Errorf("reading lab transaction isolation: %w", err)
		}
		if err := tx.queryRow(ctx, `SHOW transaction_read_only`).Scan(&result.ReadOnly); err != nil {
			return fmt.Errorf("reading lab transaction access: %w", err)
		}
		if err := tx.queryRow(ctx, `SELECT pg_current_snapshot()::text`).Scan(&result.Snapshot); err != nil {
			return fmt.Errorf("reading lab transaction snapshot: %w", err)
		}
		if result.Isolation != "repeatable read" || result.ReadOnly != "on" || result.Snapshot == "" {
			return &searchLab02PGBodyError{Code: "snapshot_contract"}
		}
		proposals, err := listProposalRecords(ctx, tx, input.native())
		if err != nil {
			return err
		}
		if len(proposals) >= input.Limit {
			return &searchLab02PGBodyError{Code: "candidate_budget"}
		}
		result.ScopedRows = len(proposals)
		if afterList != nil {
			if err := afterList(tx); err != nil {
				return err
			}
		}
		cache := make(map[[2]string]ExtractorInput)
		seen := make(map[string]bool, len(proposals))
		for _, proposal := range proposals {
			if proposal.ProposalOccurrenceID == "" || seen[proposal.ProposalOccurrenceID] {
				return &searchLab02PGBodyError{Code: "proposal_identity"}
			}
			seen[proposal.ProposalOccurrenceID] = true
			if proposal.SourceSnapshotID != input.SourceSnapshotID ||
				proposal.SourceBindingKind != ProposalSourceBindingSourceSnapshot ||
				proposal.SourceSystem != SourceSystemManualText ||
				proposal.ProposalKind != ProposalKindStatement ||
				proposal.ProposalFingerprintVersion != ProposalFingerprintStatementV1 ||
				proposal.CodeFact != nil || proposal.CodeRelation != nil {
				return &searchLab02PGBodyError{Code: "unsupported_proposal"}
			}
			if !utf8.ValidString(proposal.StatementText) || len(proposal.StatementText) > 8192 ||
				len(proposal.SourceRefs) < 1 || len(proposal.SourceRefs) > 16 {
				return &searchLab02PGBodyError{Code: "proposal_budget_or_integrity"}
			}
			key := [2]string{proposal.SourceSnapshotID, proposal.ExtractionViewID}
			source, ok := cache[key]
			if !ok {
				loaded, err := loadBoundedSourceViewFromQueryer(ctx, tx, key[0], key[1])
				if err != nil {
					return err
				}
				if loaded.Status == BoundedSourceViewStatusOverBudget {
					return &searchLab02PGBodyError{Code: "source_view_over_budget"}
				}
				if loaded.Status != BoundedSourceViewStatusAvailable || loaded.Input == nil {
					return &searchLab02PGBodyError{Code: "source_view_unavailable"}
				}
				source = *loaded.Input
				if err := validateSourceStructureInput(source); err != nil {
					return err
				}
				cache[key] = source
			}
			if source.SourceSnapshotID != proposal.SourceSnapshotID ||
				source.ExtractionViewID != proposal.ExtractionViewID ||
				source.SourceID != proposal.SourceID || source.SourceVersion != proposal.SourceVersion ||
				source.RawContentHash != proposal.RawContentHash ||
				source.RenderedContentHash != proposal.RenderedContentHash ||
				source.Renderer.Name != proposal.RendererName || source.Renderer.Version != proposal.RendererVersion {
				return &searchLab02PGBodyError{Code: "source_tuple_mismatch"}
			}
			hit, matched, err := searchLab02PGBodyMatch(proposal, source, mode)
			if err != nil {
				return err
			}
			if matched {
				result.Hits = append(result.Hits, hit)
				if len(hit.OmittedSpanIDs) > 0 {
					result.Complete = false
				}
			}
		}
		var snapshot string
		if err := tx.queryRow(ctx, `SELECT pg_current_snapshot()::text`).Scan(&snapshot); err != nil {
			return fmt.Errorf("checking lab transaction snapshot: %w", err)
		}
		if snapshot != result.Snapshot {
			return &searchLab02PGBodyError{Code: "snapshot_changed"}
		}
		return nil
	})
	if err != nil {
		return searchLab02PGBodyResult{}, err
	}
	// Occurrence identity is globally scoped; proposal-local IDs can repeat
	// across extraction runs. This order is deterministic, not a quality rank.
	slices.SortFunc(result.Hits, func(a, b searchLab02PGBodyHit) int {
		return strings.Compare(a.OriginalProposal.ProposalOccurrenceID, b.OriginalProposal.ProposalOccurrenceID)
	})
	if len(result.Hits) > limit {
		for _, hit := range result.Hits[limit:] {
			result.OmittedIDs = append(result.OmittedIDs, hit.OriginalProposal.ProposalOccurrenceID)
		}
		result.Hits = result.Hits[:limit]
		result.Complete = false
	}
	return result, nil
}

func searchLab02PGBodyMatch(proposal ProposalQueryResult, source ExtractorInput, mode string) (searchLab02PGBodyHit, bool, error) {
	spans := make(map[string]ExtractorInputSpan, len(source.Spans))
	for _, span := range source.Spans {
		spans[span.SpanID] = span
	}
	originals := make(map[ResolvedSourceRef]bool, len(proposal.SourceRefs))
	for _, ref := range proposal.SourceRefs {
		span, ok := spans[ref.SpanID]
		if !ok || ref.ExtractionViewID != source.ExtractionViewID ||
			ref.TargetKind != "" || ref.RepositorySnapshotID != "" || ref.FileSnapshotID != "" ||
			ref.RepoID != "" || ref.CommitSHA != "" || ref.Path != "" ||
			ref.StartByte < span.StartByte || ref.EndByte > span.EndByte || ref.StartByte >= ref.EndByte {
			return searchLab02PGBodyHit{}, false, &searchLab02PGBodyError{Code: "citation_integrity"}
		}
		quoted := source.RenderedText[ref.StartByte:ref.EndByte]
		if !utf8.ValidString(quoted) || quoted != ref.QuotedText || contentHash([]byte(quoted)) != ref.QuotedTextHash || originals[ref] {
			return searchLab02PGBodyHit{}, false, &searchLab02PGBodyError{Code: "citation_integrity"}
		}
		originals[ref] = true
	}
	hit := searchLab02PGBodyHit{
		OriginalProposal: proposal,
		StatementMatched: len(searchLab02PGBodyPositions(proposal.StatementText)) > 0,
	}
	// Scan the intact rendered text, never a sliced quote or concatenated spans:
	// neither a partial citation nor a catalog gap may invent a token boundary.
	positions := searchLab02PGBodyPositions(source.RenderedText)
	switch mode {
	case "cited_refs":
		for _, ref := range proposal.SourceRefs {
			if searchLab02PGBodyContains(positions, ref.StartByte, ref.EndByte) {
				hit.Witnesses = append(hit.Witnesses, searchLab02PGBodyWitness{
					Ref: ref, Role: "original_citation", WithinOriginalRefs: true,
				})
			}
		}
	case "source_body":
		spanIndex := 0
		for _, position := range positions {
			for spanIndex < len(source.Spans) && source.Spans[spanIndex].EndByte <= position[0] {
				spanIndex++
			}
			if spanIndex == len(source.Spans) || source.Spans[spanIndex].StartByte > position[0] ||
				position[1] > source.Spans[spanIndex].EndByte {
				return searchLab02PGBodyHit{}, false, &searchLab02PGBodyError{Code: "unwitnessed_body_match"}
			}
		}
		for _, span := range source.Spans {
			if !searchLab02PGBodyContains(positions, span.StartByte, span.EndByte) {
				continue
			}
			ref := resolvedExtractorInputSpan(source.ExtractionViewID, span)
			witness := searchLab02PGBodyWitness{Ref: ref, Role: "retrieval_context", WithinOriginalRefs: originals[ref]}
			if witness.WithinOriginalRefs {
				witness.Role = "original_citation"
			}
			hit.Witnesses = append(hit.Witnesses, witness)
		}
	}
	slices.SortFunc(hit.Witnesses, func(a, b searchLab02PGBodyWitness) int {
		if a.Ref.StartByte != b.Ref.StartByte {
			return a.Ref.StartByte - b.Ref.StartByte
		}
		if a.Ref.EndByte != b.Ref.EndByte {
			return a.Ref.EndByte - b.Ref.EndByte
		}
		return strings.Compare(a.Ref.SpanID, b.Ref.SpanID)
	})
	if len(hit.Witnesses) > 8 {
		for _, witness := range hit.Witnesses[8:] {
			hit.OmittedSpanIDs = append(hit.OmittedSpanIDs, witness.Ref.SpanID)
		}
		hit.Witnesses = hit.Witnesses[:8]
	}
	return hit, hit.StatementMatched || len(hit.Witnesses) > 0, nil
}

func searchLab02PGBodyContains(positions [][2]int, start, end int) bool {
	index := sort.Search(len(positions), func(i int) bool { return positions[i][0] >= start })
	return index < len(positions) && positions[index][1] <= end
}

func searchLab02PGBodyPositions(text string) [][2]int {
	var positions [][2]int
	for index := 0; index < len(text); {
		if !searchLab02PGBodyASCIIAlnum(text[index]) {
			index++
			continue
		}
		start := index
		for index < len(text) && searchLab02PGBodyASCIIAlnum(text[index]) {
			index++
		}
		if strings.EqualFold(text[start:index], searchLab02PGBodyQuery) {
			positions = append(positions, [2]int{start, index})
		}
	}
	return positions
}

func searchLab02PGBodyASCIIAlnum(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}
