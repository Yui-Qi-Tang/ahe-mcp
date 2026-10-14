package evidenceingestion

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"unicode"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencesupersession"
	"github.com/jackc/pgx/v5/pgxpool"
)

const ConflictSuggestionsV1 = "intake-conflict-suggestions/v1"
const conflictScanMax = 1000

type ConflictSuggestionRequest struct {
	ProposalOccurrenceID string `json:"proposal_occurrence_id"`
	// Optional caller-declared slot; its object coordinates must match the source.
	// It is a retrieval hint, never a new lineage binding or admission decision.
	Basis     *SupersessionLineageBasis `json:"basis,omitempty"`
	Limit     int                       `json:"limit,omitempty"`
	ScanLimit int                       `json:"scan_limit,omitempty"`
}
type ConflictMatch struct {
	Kind        string                  `json:"kind"`
	Basis       map[string]string       `json:"basis,omitempty"`
	SharedTerms []string                `json:"shared_terms,omitempty"`
	Edge        *ConflictSuggestionEdge `json:"edge,omitempty"`
}
type ConflictSuggestionEdge struct {
	ID         string          `json:"id"`
	From       string          `json:"from"`
	To         string          `json:"to"`
	Relation   string          `json:"relation"`
	Provenance json.RawMessage `json:"provenance"`
}
type ConflictSuggestion struct {
	CanonicalID          string            `json:"canonical_id"`
	ProposalOccurrenceID string            `json:"proposal_occurrence_id"`
	Statement            string            `json:"statement"`
	SourceSnapshotID     string            `json:"source_snapshot_id,omitempty"`
	SourceIdentity       map[string]string `json:"source_identity"`
	Matches              []ConflictMatch   `json:"matches"`
}
type ConflictSuggestionCoverage struct {
	ScanLimit                  int    `json:"scan_limit"`
	ScannedClaims              int    `json:"scanned_claims"`
	ScanTruncated              bool   `json:"scan_truncated"`
	MatchedClaims              int    `json:"matched_claims"`
	ReturnedClaims             int    `json:"returned_claims"`
	ResultsTruncated           bool   `json:"results_truncated"`
	RelationsScanned           int    `json:"relations_scanned"`
	RelationsTruncated         bool   `json:"relations_truncated"`
	SlotBindingsTruncated      bool   `json:"slot_bindings_truncated"`
	LexicalInputTruncated      bool   `json:"lexical_input_truncated"`
	LexicalCandidatesTruncated int    `json:"lexical_candidates_truncated"`
	ObjectIdentityAvailable    bool   `json:"object_identity_available"`
	RelationAnchorAvailable    bool   `json:"relation_anchor_available"`
	SlotBasis                  string `json:"slot_basis"`
}
type ConflictSuggestionResult struct {
	Contract           string                     `json:"contract"`
	StoreID            string                     `json:"store_id"`
	GovernanceRevision int64                      `json:"governance_revision"`
	Proposal           ProposalQueryResult        `json:"proposal"`
	Suggestions        []ConflictSuggestion       `json:"suggestions"`
	Coverage           ConflictSuggestionCoverage `json:"coverage"`
	Limitations        []string                   `json:"limitations"`
}

// SuggestIntakeConflicts searches one bounded, read-only repeatable-read cut.
// Matching is deterministic retrieval, never a contradiction or use decision.
func SuggestIntakeConflicts(ctx context.Context, pool *pgxpool.Pool, req ConflictSuggestionRequest) (ConflictSuggestionResult, error) {
	if pool == nil || !validateEndpointText(req.ProposalOccurrenceID, 512) {
		return ConflictSuggestionResult{}, newDomainError(ErrorInvalidInput, "persisted proposal occurrence and postgres pool required")
	}
	if req.Limit == 0 {
		req.Limit = 20
	}
	if req.ScanLimit == 0 {
		req.ScanLimit = 250
	}
	if req.Limit < 1 || req.Limit > 100 || req.ScanLimit < 1 || req.ScanLimit > conflictScanMax {
		return ConflictSuggestionResult{}, newDomainError(ErrorInvalidInput, "limit must be 1..100 and scan_limit 1..1000")
	}
	var out ConflictSuggestionResult
	err := withReadOnlyTx(ctx, pgxDB{pool: pool}, func(tx sqlTx) error {
		if _, err := tx.exec(ctx, `SET LOCAL row_security=off`); err != nil {
			return err
		}
		p, err := getProposalByOccurrenceID(ctx, tx, req.ProposalOccurrenceID)
		if err != nil {
			return err
		}
		if p.ProposalKind != ProposalKindStatement || p.ExtractionAttemptStatus != attemptStatusSucceeded {
			return newDomainError(ErrorInvalidInput, "successful grounded statement proposal required")
		}
		out = ConflictSuggestionResult{Contract: ConflictSuggestionsV1, Proposal: p, Suggestions: []ConflictSuggestion{}, Coverage: ConflictSuggestionCoverage{ScanLimit: req.ScanLimit, SlotBasis: "unavailable"}, Limitations: []string{"Suggestions are candidates for independent human review, not contradiction verdicts or current-use clearance. Lexical overlap does not prove incompatibility. No suggestions is not proof of no conflict.", "Only source_claim and derived_claim nodes in the first scan_limit canonical IDs are scanned. Other claims, repositories, unadmitted proposals, inaccessible data and external/provider revisions are not searched. Query failure is an error, never an empty successful result.", "Object matching requires four exact non-placeholder external identity fields. Slot matching uses recorded lineage membership or an explicitly caller-declared six-field hint; the hint does not bind a pending claim to that slot. Relations are actual direct edges of this proposal's admitted node only; pending proposals have no relation anchor. No component-to-clique inference.", "Lexical matching uses at most 256 distinct lowercase words/Han bigrams per statement and requires two shared terms. Ordering prioritizes direct relation, slot, object and overlap; it is not truth confidence. This tool creates no proposal, decision, edge, withdrawal or supersession."}}
		if err = tx.queryRow(ctx, `SELECT store_id,revision FROM evidence_governance_head WHERE singleton`).Scan(&out.StoreID, &out.GovernanceRevision); err != nil {
			return err
		}
		obj, objectOK := conflictObjectIdentity(p.OriginMetadata)
		out.Coverage.ObjectIdentityAvailable = objectOK
		slots := map[string]bool{}
		if req.Basis != nil {
			b := evidencesupersession.Basis(*req.Basis)
			key, err := b.Key()
			if err != nil {
				return err
			}
			if !objectOK {
				return newDomainError(ErrorInvalidInput, "slot hint requires known external object identity")
			}
			if err = compareSupersessionExternalOrigin(p.OriginMetadata, b, p.ProposalOccurrenceID); err != nil {
				return err
			}
			slots[key] = true
			out.Coverage.SlotBasis = "caller_declared_hint"
		}
		// A successful admission's canonical_ref may also refer to a non-claim; only
		// persisted graph edges and lineage membership are considered below.
		anchor := p.CanonicalRef
		out.Coverage.RelationAnchorAvailable = anchor != ""
		rows, err := tx.query(ctx, `SELECT n.canonical_node_id,n.origin_proposal_occurrence_id,coalesce(n.payload->>'claim',''),coalesce(r.source_snapshot_id,''),coalesce(s.origin_metadata,'{}'::jsonb)
 FROM canonical_graph_nodes n JOIN proposal_occurrences p ON p.proposal_occurrence_id=n.origin_proposal_occurrence_id
 JOIN extraction_attempts a USING(extraction_attempt_id) JOIN extraction_runs r USING(extraction_run_id)
 LEFT JOIN source_snapshots s ON s.source_snapshot_id=r.source_snapshot_id
 WHERE n.node_kind IN ('source_claim','derived_claim') AND n.canonical_node_id<>$1 ORDER BY n.canonical_node_id LIMIT $2`, anchor, req.ScanLimit+1)
		if err != nil {
			return err
		}
		candidates := []ConflictSuggestion{}
		ids := []string{anchor}
		for rows.Next() {
			var c ConflictSuggestion
			var meta []byte
			if err = rows.Scan(&c.CanonicalID, &c.ProposalOccurrenceID, &c.Statement, &c.SourceSnapshotID, &meta); err != nil {
				rows.Close()
				return err
			}
			var origin map[string]string
			if err = json.Unmarshal(meta, &origin); err != nil {
				rows.Close()
				return err
			}
			c.SourceIdentity, _ = conflictObjectIdentity(origin)
			c.Matches = []ConflictMatch{}
			candidates = append(candidates, c)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if len(candidates) > req.ScanLimit {
			out.Coverage.ScanTruncated = true
			candidates = candidates[:req.ScanLimit]
		}
		out.Coverage.ScannedClaims = len(candidates)
		for _, c := range candidates {
			ids = append(ids, c.CanonicalID)
		}
		membership := map[string][]string{}
		rows, err = tx.query(ctx, `SELECT canonical_node_id,lineage_key FROM canonical_supersession_members WHERE canonical_node_id=ANY($1::text[]) ORDER BY canonical_node_id,lineage_key LIMIT 4001`, ids)
		if err != nil {
			return err
		}
		n := 0
		for rows.Next() {
			var id, key string
			if err = rows.Scan(&id, &key); err != nil {
				rows.Close()
				return err
			}
			n++
			if n <= 4000 {
				membership[id] = append(membership[id], key)
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		out.Coverage.SlotBindingsTruncated = n > 4000
		for _, key := range membership[anchor] {
			slots[key] = true
			if out.Coverage.SlotBasis == "unavailable" {
				out.Coverage.SlotBasis = "recorded_membership"
			} else if out.Coverage.SlotBasis == "caller_declared_hint" {
				out.Coverage.SlotBasis = "recorded_membership_and_caller_hint"
			}
		}
		relations := map[string][]ConflictSuggestionEdge{}
		if anchor != "" {
			rows, err = tx.query(ctx, `SELECT canonical_edge_id,from_node_id,to_node_id,relation,provenance FROM canonical_graph_edges WHERE (from_node_id=$1 OR to_node_id=$1) AND relation IN ('contradicts','supersedes') ORDER BY canonical_edge_id LIMIT 1001`, anchor)
			if err != nil {
				return err
			}
			n = 0
			for rows.Next() {
				var e ConflictSuggestionEdge
				if err = rows.Scan(&e.ID, &e.From, &e.To, &e.Relation, &e.Provenance); err != nil {
					rows.Close()
					return err
				}
				n++
				if n <= 1000 {
					other := e.From
					if other == anchor {
						other = e.To
					}
					relations[other] = append(relations[other], e)
				}
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				return err
			}
			out.Coverage.RelationsScanned = min(n, 1000)
			out.Coverage.RelationsTruncated = n > 1000
		}
		terms, cut := conflictTerms(p.StatementText)
		out.Coverage.LexicalInputTruncated = cut
		scores := map[string]int{}
		for _, c := range candidates {
			score := 0
			if objectOK && sameConflictObject(obj, c.SourceIdentity) {
				c.Matches = append(c.Matches, ConflictMatch{Kind: "same_external_object", Basis: obj})
				score += 100
			}
			for _, key := range membership[c.CanonicalID] {
				if slots[key] {
					c.Matches = append(c.Matches, ConflictMatch{Kind: "same_declared_slot", Basis: map[string]string{"lineage_key": key, "input_basis": out.Coverage.SlotBasis}})
					score += 200
				}
			}
			for _, e := range relations[c.CanonicalID] {
				c.Matches = append(c.Matches, ConflictMatch{Kind: "existing_direct_relation", Edge: &e})
				score += 300
			}
			ct, cut := conflictTerms(c.Statement)
			if cut {
				out.Coverage.LexicalCandidatesTruncated++
			}
			shared := []string{}
			for term := range terms {
				if ct[term] {
					shared = append(shared, term)
				}
			}
			slices.Sort(shared)
			if len(shared) >= 2 {
				c.Matches = append(c.Matches, ConflictMatch{Kind: "lexical_overlap", SharedTerms: shared})
				score += min(len(shared), 20)
			}
			if len(c.Matches) > 0 {
				out.Suggestions = append(out.Suggestions, c)
				scores[c.CanonicalID] = score
			}
		}
		slices.SortFunc(out.Suggestions, func(a, b ConflictSuggestion) int {
			if scores[a.CanonicalID] != scores[b.CanonicalID] {
				return scores[b.CanonicalID] - scores[a.CanonicalID]
			}
			return strings.Compare(a.CanonicalID, b.CanonicalID)
		})
		out.Coverage.MatchedClaims = len(out.Suggestions)
		if len(out.Suggestions) > req.Limit {
			out.Coverage.ResultsTruncated = true
			out.Suggestions = out.Suggestions[:req.Limit]
		}
		out.Coverage.ReturnedClaims = len(out.Suggestions)
		return nil
	})
	return out, err
}

func conflictObjectIdentity(meta map[string]string) (map[string]string, bool) {
	out := map[string]string{}
	for _, field := range []struct{ key, name string }{{externalSourceOriginSystemKey, "source_system"}, {externalSourceOriginNamespaceKey, "source_namespace"}, {externalSourceOriginObjectTypeKey, "object_type"}, {externalSourceOriginObjectIDKey, "object_id"}} {
		v := meta[field.key]
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "", "unknown", "unspecified", "n/a", "null":
			return map[string]string{}, false
		}
		out[field.name] = v
	}
	return out, true
}
func sameConflictObject(a, b map[string]string) bool {
	if len(a) != 4 || len(b) != 4 {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}
func conflictTerms(text string) (map[string]bool, bool) {
	terms := map[string]bool{}
	cut := false
	add := func(s string) {
		if len(terms) >= 256 && !terms[s] {
			cut = true
			return
		}
		terms[s] = true
	}
	for _, word := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
		runes := []rune(word)
		han := false
		for _, r := range runes {
			han = han || unicode.Is(unicode.Han, r)
		}
		if han {
			for i := 0; i+1 < len(runes); i++ {
				add(string(runes[i : i+2]))
			}
		} else if len(runes) >= 2 {
			add(word)
		}
	}
	return terms, cut
}
