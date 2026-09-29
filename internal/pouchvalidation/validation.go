// Package pouchvalidation checks finite categorical path certificates against
// caller-owned authority. It has no encoder, solver, Python or database dependency.
package pouchvalidation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"maps"
	"slices"
)

const Version = "pouch-finite-validation/v1"
const MaxBytes = 4 << 20
const maxStates = 4096

// State is a complete assignment of named categorical coordinates.
type State map[string]string

// Action declares unit cost, exact guards, effects and every unchanged field.
type Action struct {
	ID     string   `json:"id"`
	Guard  State    `json:"guard"`
	Effect State    `json:"effect"`
	Frame  []string `json:"frame"`
}

// Model is a bounded, explicitly declared language, not extracted source meaning.
type Model struct {
	Fields    map[string][]string `json:"fields"`
	Forbidden []State             `json:"forbidden"`
	Actions   []Action            `json:"actions"`
}

// Contract is supplied out of band by the caller, never selected by the sender.
// SourceBinding maps native source IDs to the caller's exact snapshot digests.
type Contract struct {
	SchemaVersion string            `json:"schema_version"`
	RequestID     string            `json:"request_id"`
	SourceBinding map[string]string `json:"source_binding"`
	Assumptions   map[string]string `json:"assumptions"`
	Model         Model             `json:"model"`
	Start         State             `json:"start"`
	Goal          State             `json:"goal"`
	Baseline      State             `json:"baseline"`
	ForwardLimit  int               `json:"forward_limit"`
	ReturnLimit   int               `json:"return_limit"`
}

// Trace includes full states and action IDs; cost is the action count.
type Trace struct {
	States        []State  `json:"states"`
	Actions       []string `json:"actions"`
	ClaimShortest bool     `json:"claim_shortest"`
}

// Package is a typed path/closure certificate, not a CNF or DRAT proof.
// No-return requires the complete forward-reachable closure from the endpoint.
type Package struct {
	SchemaVersion   string            `json:"schema_version"`
	RequestID       string            `json:"request_id"`
	AuthoritySHA256 string            `json:"authority_sha256"`
	SourceBinding   map[string]string `json:"source_binding"`
	Assumptions     map[string]string `json:"assumptions"`
	Forward         Trace             `json:"forward"`
	ReturnKind      string            `json:"return_kind"`
	Return          *Trace            `json:"return"`
	ClosedStates    []State           `json:"closed_states"`
}

// Receipt is a conditional model conclusion. It never authorizes admission.
type Receipt struct {
	SchemaVersion    string `json:"schema_version"`
	RequestID        string `json:"request_id"`
	AuthoritySHA256  string `json:"authority_sha256"`
	PackageSHA256    string `json:"package_sha256"`
	ConclusionKind   string `json:"conclusion_kind"`
	ObservedFact     bool   `json:"observed_fact"`
	NativeAHEReceipt bool   `json:"native_ahe_receipt"`
	ForwardCost      int    `json:"forward_cost"`
	ForwardShortest  int    `json:"forward_shortest"`
	ReturnKind       string `json:"return_kind"`
	ReturnCost       int    `json:"return_cost"`
	ReturnShortest   int    `json:"return_shortest"`
	ReachableStates  int    `json:"reachable_states"`
}

// Authority owns a private decoded copy and is immutable after construction.
type Authority struct {
	contract Contract
	digest   string
	fields   []string
	actions  map[string]Action
}

// Rejection identifies a semantic or binding refusal; infrastructure errors are separate.
type Rejection struct{ Code string }

func (e *Rejection) Error() string { return e.Code }
func reject(code string) error     { return &Rejection{Code: code} }

// Digest identifies exact bytes, not an inferred canonical JSON representation.
func Digest(raw []byte) string { h := sha256.Sum256(raw); return hex.EncodeToString(h[:]) }
func hashValid(s string) bool {
	b, e := hex.DecodeString(s)
	return e == nil && len(b) == 32 && s == hex.EncodeToString(b)
}

// NewAuthority verifies the external digest and rejects unsupported model inputs.
func NewAuthority(raw []byte, expected string) (*Authority, error) {
	if !hashValid(expected) || Digest(raw) != expected {
		return nil, reject("AUTHORITY_HASH_MISMATCH")
	}
	var c Contract
	if err := decode(raw, &c); err != nil {
		return nil, err
	}
	if c.SchemaVersion != Version || c.RequestID == "" || len(c.RequestID) > 200 || len(c.SourceBinding) == 0 || len(c.SourceBinding) > 8 {
		return nil, reject("AUTHORITY_CONTRACT")
	}
	for id, hash := range c.SourceBinding {
		if id == "" || len(id) > 200 || !hashValid(hash) {
			return nil, reject("SOURCE_BINDING")
		}
	}
	if c.Assumptions == nil {
		return nil, reject("ASSUMPTIONS_REQUIRED")
	}
	a := &Authority{contract: c, digest: expected, actions: map[string]Action{}}
	if len(c.Model.Fields) == 0 || len(c.Model.Fields) > 16 || len(c.Model.Actions) == 0 || len(c.Model.Actions) > 64 || len(c.Model.Forbidden) > 256 || c.ForwardLimit < 0 || c.ForwardLimit >= maxStates || c.ReturnLimit < 0 || c.ReturnLimit >= maxStates {
		return nil, reject("MODEL_BOUND")
	}
	product := 1
	for f, values := range c.Model.Fields {
		if f == "" || len(f) > 200 || len(values) == 0 || len(values) > 64 {
			return nil, reject("FIELD_DOMAIN")
		}
		seen := map[string]bool{}
		for _, v := range values {
			if v == "" || len(v) > 200 || seen[v] {
				return nil, reject("FIELD_DOMAIN")
			}
			seen[v] = true
		}
		product *= len(values)
		if product > maxStates {
			return nil, reject("MODEL_BOUND")
		}
		a.fields = append(a.fields, f)
	}
	slices.Sort(a.fields)
	for _, pattern := range c.Model.Forbidden {
		if len(pattern) == 0 || !a.partial(pattern) {
			return nil, reject("FORBIDDEN_DOMAIN")
		}
	}
	for _, act := range c.Model.Actions {
		if act.ID == "" || len(act.ID) > 200 || !a.partial(act.Guard) || len(act.Effect) == 0 || !a.partial(act.Effect) {
			return nil, reject("ACTION_SCHEMA")
		}
		if _, ok := a.actions[act.ID]; ok {
			return nil, reject("DUPLICATE_ACTION")
		}
		covered := map[string]bool{}
		for f := range act.Effect {
			covered[f] = true
		}
		for _, f := range act.Frame {
			if _, ok := c.Model.Fields[f]; !ok || covered[f] {
				return nil, reject("FRAME_PARTITION")
			}
			covered[f] = true
		}
		if len(covered) != len(a.fields) {
			return nil, reject("FRAME_PARTITION")
		}
		a.actions[act.ID] = act
	}
	if !a.valid(c.Start) || !a.valid(c.Baseline) || len(c.Goal) == 0 || !a.partial(c.Goal) {
		return nil, reject("QUERY_DOMAIN")
	}
	return a, nil
}
func (a *Authority) partial(s State) bool {
	if s == nil {
		return false
	}
	for f, v := range s {
		if !slices.Contains(a.contract.Model.Fields[f], v) {
			return false
		}
	}
	return true
}
func matches(s, pattern State) bool {
	for f, v := range pattern {
		if s[f] != v {
			return false
		}
	}
	return true
}
func (a *Authority) valid(s State) bool {
	if len(s) != len(a.fields) || !a.partial(s) {
		return false
	}
	for _, p := range a.contract.Model.Forbidden {
		if matches(s, p) {
			return false
		}
	}
	return true
}
func (a *Authority) key(s State) string {
	v := make([]string, len(a.fields))
	for i, f := range a.fields {
		v[i] = s[f]
	}
	b, _ := json.Marshal(v)
	return string(b)
}
func (a *Authority) step(s State, act Action) (State, bool) {
	if !matches(s, act.Guard) {
		return nil, false
	}
	out := maps.Clone(s)
	for f, v := range act.Effect {
		out[f] = v
	}
	return out, a.valid(out)
}
func (a *Authority) replay(ctx context.Context, t Trace, start, goal State, limit int) (State, error) {
	if len(t.Actions) > limit || len(t.States) != len(t.Actions)+1 || !maps.Equal(t.States[0], start) {
		return nil, reject("TRACE_SHAPE_OR_START")
	}
	for _, s := range t.States {
		if !a.valid(s) {
			return nil, reject("TRACE_STATE_DOMAIN")
		}
	}
	seen := map[string]bool{}
	for i, s := range t.States {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !a.valid(s) {
			return nil, reject("TRACE_STATE_DOMAIN")
		}
		key := a.key(s)
		if seen[key] {
			return nil, reject("TRACE_REPEATED_STATE")
		}
		seen[key] = true
		if i == len(t.Actions) {
			if !matches(s, goal) {
				return nil, reject("TRACE_GOAL")
			}
			return s, nil
		}
		if matches(s, goal) {
			return nil, reject("TRACE_AFTER_GOAL")
		}
		action, ok := a.actions[t.Actions[i]]
		if !ok {
			return nil, reject("UNKNOWN_ACTION")
		}
		next, enabled := a.step(s, action)
		if !enabled {
			return nil, reject("ACTION_DISABLED")
		}
		if !maps.Equal(next, t.States[i+1]) {
			return nil, reject("TRACE_EFFECT_OR_FRAME")
		}
	}
	return nil, reject("TRACE_SHAPE_OR_START")
}

// distances deliberately uses its own queue and guard/effect evaluator, not the
// Pouch encoder/reference or sender-provided edges. Full finite closure makes
// a horizon-limited failure insufficient to establish no-return.
func (a *Authority) distances(ctx context.Context, start State) (map[string]int, []State, error) {
	d := map[string]int{a.key(start): 0}
	queue := []State{start}
	for head := 0; head < len(queue); head++ {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		s := queue[head]
		for _, act := range a.contract.Model.Actions {
			next, ok := a.step(s, act)
			if !ok {
				continue
			}
			key := a.key(next)
			if _, ok := d[key]; ok {
				continue
			}
			d[key] = d[a.key(s)] + 1
			queue = append(queue, next)
		}
	}
	return d, queue, nil
}
func (a *Authority) shortest(dist map[string]int, states []State, goal State) int {
	best := -1
	for _, s := range states {
		d := dist[a.key(s)]
		if matches(s, goal) && (best < 0 || d < best) {
			best = d
		}
	}
	return best
}

// Validate checks one caller-selected request and never reads paths from a package.
// A successful receipt is not proof of source entailment, runtime restore or admission.
func (a *Authority) Validate(ctx context.Context, requestID string, raw []byte) (Receipt, error) {
	var receipt Receipt
	if ctx == nil || a == nil {
		return receipt, errors.New("authority and context required")
	}
	if err := ctx.Err(); err != nil {
		return receipt, err
	}
	if requestID != a.contract.RequestID {
		return receipt, reject("CALLER_REQUEST_MISMATCH")
	}
	var p Package
	if err := decode(raw, &p); err != nil {
		return receipt, err
	}
	if p.SchemaVersion != Version || p.RequestID != requestID || p.AuthoritySHA256 != a.digest {
		return receipt, reject("PACKAGE_BINDING")
	}
	if !maps.Equal(p.SourceBinding, a.contract.SourceBinding) {
		return receipt, reject("SOURCE_BINDING")
	}
	if p.Assumptions == nil || !maps.Equal(p.Assumptions, a.contract.Assumptions) {
		return receipt, reject("ASSUMPTION_BINDING")
	}
	end, err := a.replay(ctx, p.Forward, a.contract.Start, a.contract.Goal, a.contract.ForwardLimit)
	if err != nil {
		return receipt, err
	}
	distances, states, err := a.distances(ctx, a.contract.Start)
	if err != nil {
		return receipt, err
	}
	shortest := a.shortest(distances, states, a.contract.Goal)
	if p.Forward.ClaimShortest && shortest != len(p.Forward.Actions) {
		return receipt, reject("FORWARD_NOT_SHORTEST")
	}
	rd, rs, err := a.distances(ctx, end)
	if err != nil {
		return receipt, err
	}
	returnShortest := a.shortest(rd, rs, a.contract.Baseline)
	returnCost := -1
	switch p.ReturnKind {
	case "return":
		if p.Return == nil || len(p.ClosedStates) != 0 {
			return receipt, reject("RETURN_CERTIFICATE_SHAPE")
		}
		if _, err := a.replay(ctx, *p.Return, end, a.contract.Baseline, a.contract.ReturnLimit); err != nil {
			return receipt, err
		}
		returnCost = len(p.Return.Actions)
		if p.Return.ClaimShortest && returnShortest != returnCost {
			return receipt, reject("RETURN_NOT_SHORTEST")
		}
	case "no_return":
		if len(p.ClosedStates) > maxStates {
			return receipt, reject("CLOSURE_BOUND")
		}
		if p.Return != nil {
			return receipt, reject("RETURN_CERTIFICATE_SHAPE")
		}
		if returnShortest >= 0 {
			return receipt, reject("FALSE_NO_RETURN")
		}
		closure := map[string]bool{}
		for _, s := range p.ClosedStates {
			if !a.valid(s) || closure[a.key(s)] {
				return receipt, reject("CLOSURE_DOMAIN_OR_DUPLICATE")
			}
			closure[a.key(s)] = true
		}
		if len(closure) != len(rd) {
			return receipt, reject("CLOSURE_INCOMPLETE")
		}
		for key := range rd {
			if !closure[key] {
				return receipt, reject("CLOSURE_INCOMPLETE")
			}
		}
	default:
		return receipt, reject("RETURN_KIND")
	}
	return Receipt{SchemaVersion: Version, RequestID: requestID, AuthoritySHA256: a.digest, PackageSHA256: Digest(raw), ConclusionKind: "model_conditional_conclusion", ForwardCost: len(p.Forward.Actions), ForwardShortest: shortest, ReturnKind: p.ReturnKind, ReturnCost: returnCost, ReturnShortest: returnShortest, ReachableStates: len(rs)}, nil
}

// SourceIDs returns a copy for the trusted native admission coordinator.
func (a *Authority) SourceIDs() []string {
	ids := slices.Collect(maps.Keys(a.contract.SourceBinding))
	slices.Sort(ids)
	return ids
}

// Statement serializes the verified result for exact native review and readback.
func (r Receipt) Statement() string {
	b, _ := json.Marshal(r) // This closed struct contains only strings, bools and integers.
	return string(b)
}

// SourceDigest exposes only the pinned raw-source digest for native comparison.
func (a *Authority) SourceDigest(id string) (string, bool) {
	hash, ok := a.contract.SourceBinding[id]
	return hash, ok
}
