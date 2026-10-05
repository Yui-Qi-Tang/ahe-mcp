package evidenceingestion

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ExternalRepresentationContract describes externally authored structure, not executable Core rules.
const ExternalRepresentationContract = "external-representation/v1"

// ExternalDependencyKey identifies a missing or supplied external item within an explicit scope.
// Core compares this tuple exactly; it does not decide whether two labels are synonymous.
type ExternalDependencyKey struct {
	Namespace string `json:"namespace"`
	LocalID   string `json:"local_id"`
	ScopeRef  string `json:"scope_ref"`
	Revision  string `json:"revision"`
}

// ID returns an unambiguous reference for use at the declared JSON pointers.
func (k ExternalDependencyKey) ID() string {
	return "dependency:sha256:" + propositionDigest("external-dependency/v1", k.Namespace, k.LocalID, k.ScopeRef, k.Revision)
}

// ExternalDependency preserves the identity and exact locations of an external dependency.
// Supplied means bytes are linked, not that they are sufficient, true, or approved.
type ExternalDependency struct {
	Key                 ExternalDependencyKey `json:"key"`
	Status              string                `json:"status"`
	Pointers            []string              `json:"pointers"`
	Reason              string                `json:"reason"`
	SourceSnapshotID    string                `json:"source_snapshot_id,omitempty"`
	ExtractionViewID    string                `json:"extraction_view_id,omitempty"`
	RenderedContentHash string                `json:"rendered_content_hash,omitempty"`
}

// ExternalRepresentationInput preserves exact JSON text and declared references.
// PreviousID records an external revision assertion; branches are allowed and no current winner is inferred.
type ExternalRepresentationInput struct {
	Contract        string               `json:"contract"`
	RequestID       string               `json:"request_id"`
	Subject         ExternalCheckSubject `json:"subject"`
	Name            string               `json:"name"`
	Version         string               `json:"version"`
	Role            string               `json:"role"`
	Format          string               `json:"format"`
	FormatVersion   string               `json:"format_version"`
	Content         string               `json:"content"`
	RecordedBy      string               `json:"recorded_by"`
	Producer        string               `json:"producer"`
	MappingClaim    string               `json:"mapping_claim"`
	Dependencies    []ExternalDependency `json:"dependencies"`
	PreviousID      string               `json:"previous_id,omitempty"`
	RevisionReason  string               `json:"revision_reason,omitempty"`
	CanonicalNodeID string               `json:"canonical_node_id,omitempty"`
	DerivationID    string               `json:"derivation_id,omitempty"`
}

// ExternalRepresentationRecord is an immutable declaration and its database receipt time.
type ExternalRepresentationRecord struct {
	ID         string                      `json:"id"`
	Definition ExternalRepresentationInput `json:"definition"`
	RecordedAt time.Time                   `json:"recorded_at"`
}

// ExternalRepresentationReceipt is historical, including on replay after a later revision.
type ExternalRepresentationReceipt struct {
	Record   ExternalRepresentationRecord `json:"record"`
	Replayed bool                         `json:"replayed"`
}

// ExternalRepresentationLink records that one actual checker input is this complete envelope.
type ExternalRepresentationLink struct {
	CheckID          string `json:"check_id"`
	InputID          string `json:"input_id"`
	RepresentationID string `json:"representation_id"`
}

// ExternalDependencyUsers is a bounded reverse index, not automatic recomputation or invalidation.
type ExternalDependencyUsers struct {
	Representations          []ExternalRepresentationRecord `json:"representations"`
	CheckInputs              []ExternalRepresentationLink   `json:"check_inputs"`
	RepresentationsTruncated bool                           `json:"representations_truncated"`
	CheckInputsTruncated     bool                           `json:"check_inputs_truncated"`
}

func (k ExternalDependencyKey) validate() error {
	for _, s := range []string{k.Namespace, k.LocalID, k.ScopeRef, k.Revision} {
		if !externalText(s, 512, false) {
			return newDomainError(ErrorInvalidInput, "invalid external dependency identity")
		}
	}
	return nil
}

// decodeExternalJSON checks syntax, duplicate keys and nesting without interpreting rules.
// UseNumber avoids lossy float64 conversion even for very large literal numbers.
func decodeExternalJSON(content string) (any, error) {
	d := json.NewDecoder(strings.NewReader(content))
	d.UseNumber()
	var read func(int) (any, error)
	read = func(depth int) (any, error) {
		if depth > 64 {
			return nil, newDomainError(ErrorInvalidInput, "representation JSON nesting exceeds 64")
		}
		token, err := d.Token()
		if err != nil {
			return nil, err
		}
		delim, ok := token.(json.Delim)
		if !ok {
			return token, nil
		}
		switch delim {
		case '{':
			object := map[string]any{}
			for d.More() {
				key, err := d.Token()
				if err != nil {
					return nil, err
				}
				name, ok := key.(string)
				if !ok {
					return nil, newDomainError(ErrorInvalidInput, "JSON object key required")
				}
				if _, exists := object[name]; exists {
					return nil, newDomainError(ErrorInvalidInput, "duplicate JSON key")
				}
				v, err := read(depth + 1)
				if err != nil {
					return nil, err
				}
				object[name] = v
			}
			_, err := d.Token()
			return object, err
		case '[':
			values := []any{}
			for d.More() {
				v, err := read(depth + 1)
				if err != nil {
					return nil, err
				}
				values = append(values, v)
			}
			_, err := d.Token()
			return values, err
		default:
			return nil, newDomainError(ErrorInvalidInput, "unexpected JSON delimiter")
		}
	}
	v, err := read(0)
	if err != nil {
		return nil, err
	}
	if _, err = d.Token(); err != io.EOF {
		return nil, newDomainError(ErrorInvalidInput, "representation must contain exactly one JSON value")
	}
	return v, nil
}

func externalJSONPointer(value any, pointer string) (any, error) {
	if !strings.HasPrefix(pointer, "/") || !externalText(pointer, 2048, false) {
		return nil, newDomainError(ErrorInvalidInput, "dependency requires a non-root JSON pointer")
	}
	for _, raw := range strings.Split(pointer[1:], "/") {
		for i := 0; i < len(raw); i++ {
			if raw[i] == '~' {
				if i+1 == len(raw) || (raw[i+1] != '0' && raw[i+1] != '1') {
					return nil, newDomainError(ErrorInvalidInput, "invalid JSON pointer escape")
				}
				i++
			}
		}
		key := strings.ReplaceAll(strings.ReplaceAll(raw, "~1", "/"), "~0", "~")
		switch v := value.(type) {
		case map[string]any:
			var ok bool
			value, ok = v[key]
			if !ok {
				return nil, newDomainError(ErrorInvalidInput, "dependency pointer missing")
			}
		case []any:
			i, err := strconv.Atoi(key)
			if err != nil || strconv.Itoa(i) != key || i < 0 || i >= len(v) {
				return nil, newDomainError(ErrorInvalidInput, "invalid dependency array index")
			}
			value = v[i]
		default:
			return nil, newDomainError(ErrorInvalidInput, "dependency pointer crosses scalar")
		}
	}
	return value, nil
}

func (in ExternalRepresentationInput) validate() error {
	if in.Contract != ExternalRepresentationContract || !externalText(in.RequestID, 512, false) || (in.Role != "source" && in.Role != "candidate") {
		return newDomainError(ErrorInvalidInput, "invalid representation contract, request or role")
	}
	for _, s := range []string{in.Subject.ProposalOccurrenceID, in.Subject.SourceSnapshotID, in.Subject.ExtractionViewID, in.Subject.SourceVersion, in.Subject.RawContentHash, in.Subject.RenderedContentHash, in.Subject.ProposalFingerprint, in.Name, in.Version, in.Format, in.FormatVersion, in.Producer, in.RecordedBy} {
		if !externalText(s, 1024, false) {
			return newDomainError(ErrorInvalidInput, "invalid representation identity")
		}
	}
	if !externalText(in.Content, 262144, false) || !externalText(in.MappingClaim, 8192, false) || len(in.Dependencies) > 64 {
		return newDomainError(ErrorInvalidInput, "invalid representation bounds")
	}
	value, err := decodeExternalJSON(in.Content)
	if err != nil {
		return err
	}
	keys, pointers := map[string]bool{}, map[string]bool{}
	for _, dep := range in.Dependencies {
		if err := dep.Key.validate(); err != nil {
			return err
		}
		id := dep.Key.ID()
		if keys[id] || len(dep.Pointers) < 1 || len(dep.Pointers) > 32 || !externalText(dep.Reason, 8192, false) {
			return newDomainError(ErrorInvalidInput, "invalid or duplicate dependency")
		}
		keys[id] = true
		switch dep.Status {
		case "missing":
			if dep.SourceSnapshotID != "" || dep.ExtractionViewID != "" || dep.RenderedContentHash != "" {
				return newDomainError(ErrorInvalidInput, "missing dependency cannot claim supplied evidence")
			}
		case "supplied":
			for _, s := range []string{dep.SourceSnapshotID, dep.ExtractionViewID, dep.RenderedContentHash} {
				if !externalText(s, 1024, false) {
					return newDomainError(ErrorInvalidInput, "supplied dependency requires exact source binding")
				}
			}
		default:
			return newDomainError(ErrorInvalidInput, "invalid dependency status")
		}
		for _, pointer := range dep.Pointers {
			if pointers[pointer] {
				return newDomainError(ErrorInvalidInput, "duplicate dependency pointer")
			}
			pointers[pointer] = true
			got, err := externalJSONPointer(value, pointer)
			if err != nil {
				return err
			}
			if text, ok := got.(string); !ok || text != id {
				return newDomainError(ErrorInvalidInput, "dependency pointer does not name its identity")
			}
		}
	}
	if (in.PreviousID == "") != (in.RevisionReason == "") || !externalText(in.PreviousID, 128, true) || !externalText(in.RevisionReason, 8192, true) || !externalText(in.CanonicalNodeID, 512, true) || !externalText(in.DerivationID, 512, true) || (in.DerivationID != "" && in.CanonicalNodeID == "") {
		return newDomainError(ErrorInvalidInput, "invalid representation history or derivation binding")
	}
	return nil
}

// RecordExternalRepresentation preserves an external declaration without creating evidence or executing it.
func RecordExternalRepresentation(ctx context.Context, pool *pgxpool.Pool, in ExternalRepresentationInput) (ExternalRepresentationReceipt, error) {
	if pool == nil {
		return ExternalRepresentationReceipt{}, newDomainError(ErrorInvalidInput, "postgres pool is required")
	}
	if err := in.validate(); err != nil {
		return ExternalRepresentationReceipt{}, err
	}
	body, err := json.Marshal(in)
	if err != nil {
		return ExternalRepresentationReceipt{}, err
	}
	if len(body) > 1048576 {
		return ExternalRepresentationReceipt{}, newDomainError(ErrorInvalidInput, "representation exceeds byte budget")
	}
	id := "representation:sha256:" + externalDigest(body)
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return ExternalRepresentationReceipt{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	tag, err := tx.Exec(ctx, `INSERT INTO external_representation_records(representation_id,request_id,proposal_occurrence_id,body,previous_id)
 VALUES($1,$2,$3,$4,NULLIF($5,'')) ON CONFLICT(request_id) DO NOTHING`, id, in.RequestID, in.Subject.ProposalOccurrenceID, string(body), in.PreviousID)
	if err != nil {
		return ExternalRepresentationReceipt{}, fmt.Errorf("recording external representation: %w", err)
	}
	var storedID, storedBody string
	var at time.Time
	if err := tx.QueryRow(ctx, `SELECT representation_id,body,recorded_at FROM external_representation_records WHERE request_id=$1`, in.RequestID).Scan(&storedID, &storedBody, &at); err != nil {
		return ExternalRepresentationReceipt{}, err
	}
	if storedID != id || storedBody != string(body) {
		return ExternalRepresentationReceipt{}, newDomainError(ErrorIdempotencyKeyReused, "representation request differs from historical receipt")
	}
	if err := tx.Commit(ctx); err != nil {
		return ExternalRepresentationReceipt{}, err
	}
	return ExternalRepresentationReceipt{Record: ExternalRepresentationRecord{ID: id, Definition: in, RecordedAt: at}, Replayed: tag.RowsAffected() == 0}, nil
}

func scanExternalRepresentation(row sqlRow) (ExternalRepresentationRecord, error) {
	var out ExternalRepresentationRecord
	var body string
	if err := row.Scan(&out.ID, &body, &out.RecordedAt); err != nil {
		return out, err
	}
	if out.ID != "representation:sha256:"+externalDigest([]byte(body)) {
		return out, newDomainError(ErrorInvalidInput, "representation digest mismatch")
	}
	if err := json.Unmarshal([]byte(body), &out.Definition); err != nil {
		return out, err
	}
	if err := out.Definition.validate(); err != nil {
		return out, err
	}
	return out, nil
}

// ReadExternalRepresentation returns an exact historical declaration, including its dependency states.
func ReadExternalRepresentation(ctx context.Context, pool *pgxpool.Pool, id string) (ExternalRepresentationRecord, error) {
	var out ExternalRepresentationRecord
	if pool == nil || !externalText(id, 128, false) {
		return out, newDomainError(ErrorInvalidInput, "pool and representation id required")
	}
	err := withExternalRecordRead(ctx, pool, func(tx sqlTx) error {
		var err error
		out, err = scanExternalRepresentation(tx.queryRow(ctx, `SELECT representation_id,body,recorded_at FROM external_representation_records WHERE representation_id=$1`, id))
		return err
	})
	return out, err
}

// CheckMaterial serializes the entire declaration, not just the condition tree.
// This binds dependency states, source references, versions and mapping claims too.
func (r ExternalRepresentationRecord) CheckMaterial(inputID string) (ExternalCheckMaterial, error) {
	if err := r.Definition.validate(); err != nil {
		return ExternalCheckMaterial{}, err
	}
	body, err := json.Marshal(r.Definition)
	if err != nil {
		return ExternalCheckMaterial{}, err
	}
	if r.ID != "representation:sha256:"+externalDigest(body) || !externalText(inputID, 128, false) || len(body) > 262144 {
		return ExternalCheckMaterial{}, newDomainError(ErrorInvalidInput, "invalid representation receipt or material id")
	}
	return ExternalCheckMaterial{ID: inputID, Role: r.Definition.Role, Format: ExternalRepresentationContract, Version: "1", Content: string(body), MappingClaim: r.Definition.MappingClaim}, nil
}

// LinkExternalCheckRepresentation indexes an exact complete-envelope input already in a check.
// The link cannot replace the input, upgrade its result, or attach an old check to changed material.
func LinkExternalCheckRepresentation(ctx context.Context, pool *pgxpool.Pool, link ExternalRepresentationLink) (bool, error) {
	if pool == nil || !externalText(link.CheckID, 128, false) || !externalText(link.RepresentationID, 128, false) || !externalText(link.InputID, 128, false) {
		return false, newDomainError(ErrorInvalidInput, "pool and exact check/input/representation references required")
	}
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	tag, err := tx.Exec(ctx, `INSERT INTO external_check_representation_links(check_id,input_id,representation_id) VALUES($1,$2,$3) ON CONFLICT(check_id,input_id) DO NOTHING`, link.CheckID, link.InputID, link.RepresentationID)
	if err != nil {
		return false, err
	}
	var stored string
	if err := tx.QueryRow(ctx, `SELECT representation_id FROM external_check_representation_links WHERE check_id=$1 AND input_id=$2`, link.CheckID, link.InputID).Scan(&stored); err != nil {
		return false, err
	}
	if stored != link.RepresentationID {
		return false, newDomainError(ErrorIdempotencyKeyReused, "check input already indexes a different representation")
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return tag.RowsAffected() == 0, nil
}

// ReadExternalDependencyUsers locates declared users of exactly one dependency tuple.
// CheckInputs covers only the returned representations and explicitly recorded links.
// No semantics are inferred from unlisted JSON values and no result is recomputed.
func ReadExternalDependencyUsers(ctx context.Context, pool *pgxpool.Pool, key ExternalDependencyKey, limit int) (ExternalDependencyUsers, error) {
	out := ExternalDependencyUsers{Representations: []ExternalRepresentationRecord{}, CheckInputs: []ExternalRepresentationLink{}}
	if pool == nil || limit < 1 || limit > 100 {
		return out, newDomainError(ErrorInvalidInput, "pool and limit 1..100 required")
	}
	if err := key.validate(); err != nil {
		return out, err
	}
	encoded, err := json.Marshal(key)
	if err != nil {
		return out, err
	}
	err = withExternalRecordRead(ctx, pool, func(tx sqlTx) error {
		rows, err := tx.query(ctx, `SELECT representation_id,body,recorded_at FROM external_representation_records r
 WHERE EXISTS(SELECT 1 FROM jsonb_array_elements(COALESCE(NULLIF(r.body::jsonb->'dependencies','null'::jsonb),'[]'::jsonb)) d WHERE d->'key'=$1::jsonb)
 ORDER BY representation_id COLLATE "C" LIMIT $2`, string(encoded), limit+1)
		if err != nil {
			return err
		}
		for rows.Next() {
			r, err := scanExternalRepresentation(rows)
			if err != nil {
				rows.Close()
				return err
			}
			out.Representations = append(out.Representations, r)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if len(out.Representations) > limit {
			out.RepresentationsTruncated = true
			out.Representations = out.Representations[:limit]
		}
		ids := make([]string, 0, len(out.Representations))
		for _, r := range out.Representations {
			ids = append(ids, r.ID)
		}
		links, err := tx.query(ctx, `SELECT check_id,input_id,representation_id FROM external_check_representation_links WHERE representation_id=ANY($1::text[]) ORDER BY check_id COLLATE "C",input_id COLLATE "C" LIMIT $2`, ids, limit+1)
		if err != nil {
			return err
		}
		defer links.Close()
		for links.Next() {
			var link ExternalRepresentationLink
			if err := links.Scan(&link.CheckID, &link.InputID, &link.RepresentationID); err != nil {
				return err
			}
			out.CheckInputs = append(out.CheckInputs, link)
		}
		return links.Err()
	})
	if err != nil {
		return ExternalDependencyUsers{}, err
	}
	if len(out.CheckInputs) > limit {
		out.CheckInputsTruncated = true
		out.CheckInputs = out.CheckInputs[:limit]
	}
	return out, nil
}
