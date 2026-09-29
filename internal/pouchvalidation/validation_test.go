package pouchvalidation

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func fixture(t *testing.T) (Contract, Package) {
	t.Helper()
	s0, s1, s2 := State{"x": "0"}, State{"x": "1"}, State{"x": "2"}
	c := Contract{SchemaVersion: Version, RequestID: "q", SourceBinding: map[string]string{"canon-node:source": Digest([]byte("source"))}, Assumptions: map[string]string{"finite": "scenario_assumption"}, Model: Model{Fields: map[string][]string{"x": {"0", "1", "2"}}, Forbidden: []State{}, Actions: []Action{
		{ID: "first", Guard: s0, Effect: s1, Frame: []string{}}, {ID: "second", Guard: s1, Effect: s2, Frame: []string{}}, {ID: "direct", Guard: s0, Effect: s2, Frame: []string{}}, {ID: "restore", Guard: s2, Effect: s0, Frame: []string{}}}}, Start: s0, Goal: s2, Baseline: s0, ForwardLimit: 2, ReturnLimit: 2}
	p := Package{SchemaVersion: Version, RequestID: "q", SourceBinding: c.SourceBinding, Assumptions: c.Assumptions, Forward: Trace{States: []State{s0, s2}, Actions: []string{"direct"}, ClaimShortest: true}, ReturnKind: "return", Return: &Trace{States: []State{s2, s0}, Actions: []string{"restore"}, ClaimShortest: true}, ClosedStates: []State{}}
	return c, p
}
func bytesOf(t *testing.T, v any) []byte {
	t.Helper()
	b, e := json.Marshal(v)
	if e != nil {
		t.Fatal(e)
	}
	return b
}
func bind(t *testing.T, c Contract, p *Package) *Authority {
	t.Helper()
	b := bytesOf(t, c)
	a, e := NewAuthority(b, Digest(b))
	if e != nil {
		t.Fatal(e)
	}
	p.AuthoritySHA256 = Digest(b)
	return a
}
func code(t *testing.T, err error, want string) {
	t.Helper()
	var e *Rejection
	if !errors.As(err, &e) || e.Code != want {
		t.Fatalf("got %v, want %s", err, want)
	}
}
func TestValidationCertificates(t *testing.T) {
	t.Run("roundtrip", func(t *testing.T) {
		c, p := fixture(t)
		a := bind(t, c, &p)
		r, e := a.Validate(t.Context(), "q", bytesOf(t, p))
		if e != nil || r.ForwardShortest != 1 || r.ReturnShortest != 1 || r.ObservedFact || r.NativeAHEReceipt {
			t.Fatalf("%+v %v", r, e)
		}
	})
	t.Run("longer legal path is not shortest", func(t *testing.T) {
		c, p := fixture(t)
		a := bind(t, c, &p)
		p.Forward = Trace{States: []State{{"x": "0"}, {"x": "1"}, {"x": "2"}}, Actions: []string{"first", "second"}, ClaimShortest: true}
		_, e := a.Validate(t.Context(), "q", bytesOf(t, p))
		code(t, e, "FORWARD_NOT_SHORTEST")
		p.Forward.ClaimShortest = false
		if _, e = a.Validate(t.Context(), "q", bytesOf(t, p)); e != nil {
			t.Fatal(e)
		}
	})
	t.Run("true no return", func(t *testing.T) {
		c, p := fixture(t)
		c.Model.Actions = c.Model.Actions[:3]
		p.ReturnKind = "no_return"
		p.Return = nil
		p.ClosedStates = []State{{"x": "2"}}
		a := bind(t, c, &p)
		r, e := a.Validate(t.Context(), "q", bytesOf(t, p))
		if e != nil || r.ReturnShortest != -1 {
			t.Fatalf("%+v %v", r, e)
		}
		p.ClosedStates = []State{}
		_, e = a.Validate(t.Context(), "q", bytesOf(t, p))
		code(t, e, "CLOSURE_INCOMPLETE")
	})
	t.Run("short horizon is not no return", func(t *testing.T) {
		c, p := fixture(t)
		c.ReturnLimit = 0
		p.ReturnKind = "no_return"
		p.Return = nil
		p.ClosedStates = []State{{"x": "2"}}
		a := bind(t, c, &p)
		_, e := a.Validate(t.Context(), "q", bytesOf(t, p))
		code(t, e, "FALSE_NO_RETURN")
	})
}
func TestRefusals(t *testing.T) {
	cases := []struct {
		name, want string
		mutate     func(*Package)
	}{
		{"authority", "PACKAGE_BINDING", func(p *Package) { p.AuthoritySHA256 = strings.Repeat("0", 64) }},
		{"source", "SOURCE_BINDING", func(p *Package) { p.SourceBinding = map[string]string{"canon-node:old": Digest([]byte("source"))} }},
		{"assumption", "ASSUMPTION_BINDING", func(p *Package) { p.Assumptions = map[string]string{"finite": "observed_fact"} }},
		{"request", "PACKAGE_BINDING", func(p *Package) { p.RequestID = "another" }},
		{"effect", "TRACE_EFFECT_OR_FRAME", func(p *Package) { p.Forward.States[1] = State{"x": "1"} }},
		{"extra coordinate", "TRACE_STATE_DOMAIN", func(p *Package) { p.Forward.States[1] = State{"x": "2", "hidden": "0"} }},
		{"missing coordinate", "TRACE_STATE_DOMAIN", func(p *Package) { p.Forward.States[1] = State{} }},
		{"action", "UNKNOWN_ACTION", func(p *Package) { p.Forward.Actions[0] = "invented" }},
		{"return junction", "TRACE_SHAPE_OR_START", func(p *Package) { p.Return.States[0] = State{"x": "1"} }},
		{"return certificate", "RETURN_CERTIFICATE_SHAPE", func(p *Package) { p.Return = nil }},
		{"return kind", "RETURN_KIND", func(p *Package) { p.ReturnKind = "unknown" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, p := fixture(t)
			a := bind(t, c, &p)
			tc.mutate(&p)
			_, e := a.Validate(t.Context(), "q", bytesOf(t, p))
			code(t, e, tc.want)
		})
	}
}
func TestStrictTypesAndCaller(t *testing.T) {
	c, p := fixture(t)
	a := bind(t, c, &p)
	raw := string(bytesOf(t, p))
	for _, bad := range []string{strings.Replace(raw, `"claim_shortest":true`, `"claim_shortest":1`, 1), strings.Replace(raw, `"claim_shortest":true`, `"claim_shortest":null`, 1), strings.Replace(raw, `"request_id":"q"`, `"Request_ID":"q"`, 1), strings.Replace(raw, `"request_id":"q"`, `"request_id":"q","request_id":"q"`, 1), strings.Replace(raw, `"x":"0"`, `"x":0`, 1), strings.Replace(raw, `"request_id":"q",`, "", 1), raw + raw} {
		if _, e := a.Validate(t.Context(), "q", []byte(bad)); e == nil {
			t.Fatal("accepted malformed typed package")
		}
	}
	_, e := a.Validate(t.Context(), "wrong", []byte(raw))
	code(t, e, "CALLER_REQUEST_MISMATCH")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, e = a.Validate(ctx, "q", []byte(raw))
	if !errors.Is(e, context.Canceled) {
		t.Fatalf("%v", e)
	}
	_, e = NewAuthority(bytesOf(t, c), strings.Repeat("0", 64))
	code(t, e, "AUTHORITY_HASH_MISMATCH")
}
func TestAuthorityModelBounds(t *testing.T) {
	for _, mutate := range []func(*Contract){func(c *Contract) { c.Model.Actions[0].Frame = []string{"x"} }, func(c *Contract) { c.Model.Actions[0].Effect = State{} }, func(c *Contract) { c.Model.Fields["x"] = []string{"0", "0"} }, func(c *Contract) { c.Model.Forbidden = []State{{}} }, func(c *Contract) { c.Start = State{} }, func(c *Contract) { c.Model.Fields["huge"] = make([]string, 65) }} {
		c, _ := fixture(t)
		mutate(&c)
		raw := bytesOf(t, c)
		if _, e := NewAuthority(raw, Digest(raw)); e == nil {
			t.Fatal("invalid authority accepted")
		}
	}
}
