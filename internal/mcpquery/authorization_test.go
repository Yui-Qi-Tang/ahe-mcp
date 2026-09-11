package mcpquery

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/dbrole"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencequerymcp"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/mcpstdio"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/runtimeauth"
)

type recordingQueryBackend struct {
	tools         []mcpstdio.Tool
	calls         int
	name          string
	expectedOwner string
	ownerMatched  bool
}

func (b *recordingQueryBackend) Tools() []mcpstdio.Tool {
	return append([]mcpstdio.Tool(nil), b.tools...)
}

func (b *recordingQueryBackend) CallTool(
	ctx context.Context,
	name string,
	_ json.RawMessage,
) (json.RawMessage, error) {
	b.calls++
	b.name = name
	if b.expectedOwner != "" {
		b.ownerMatched = evidencequerymcp.CanonicalReadViewOwnerMatches(ctx, b.expectedOwner)
	}
	return json.RawMessage(`{"ok":true}`), nil
}

func TestQueryRuntimeAuthorizationBindsExactPrincipalAndVisibilityCut(t *testing.T) {
	databaseBinding := validQueryDatabaseBinding(t)
	authorization, err := NewQueryRuntimeAuthorization("consumer:one", databaseBinding)
	if err != nil {
		t.Fatal(err)
	}
	if got := authorization.Principal().ID; got != "consumer:one" {
		t.Fatalf("Principal().ID = %q", got)
	}
	want := QueryRuntimeBinding{
		SchemaVersion: QueryRuntimeAuthorizationVersion,
		PrincipalID:   "consumer:one",
		VisibilityCut: QueryVisibilityCut{
			Database:     databaseBinding.Database,
			Schema:       databaseBinding.Schema,
			DatabaseRole: databaseBinding.Role,
			Profile:      databaseBinding.Profile,
			ManifestHash: databaseBinding.ManifestHash,
		},
	}
	if got := authorization.Binding(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Binding() = %+v, want %+v", got, want)
	}
	if !strings.HasPrefix(authorization.readViewOwner, "query-read-view-owner:") ||
		len(authorization.readViewOwner) != len("query-read-view-owner:")+64 {
		t.Fatalf("read-view owner = %q", authorization.readViewOwner)
	}
	var absent *QueryRuntimeAuthorization
	if absent.Principal() != (runtimeauth.Principal{}) || absent.Binding() != (QueryRuntimeBinding{}) {
		t.Fatal("nil authorization returned a non-zero value")
	}
}

func TestQueryRuntimeAuthorizationRejectsUntrustedConfiguration(t *testing.T) {
	valid := validQueryDatabaseBinding(t)
	for _, test := range []struct {
		name      string
		principal string
		binding   dbrole.RuntimeBinding
		want      string
	}{
		{name: "missing principal", binding: valid, want: "unauthenticated"},
		{name: "principal whitespace", principal: " consumer:one ", binding: valid, want: "unauthenticated"},
		{name: "principal invalid UTF-8", principal: string([]byte{0xff}), binding: valid, want: "unauthenticated"},
		{name: "zero binding", principal: "consumer:one", want: "schema_version"},
		{name: "wrong profile", principal: "consumer:one", binding: withQueryBindingProfile(valid, dbrole.Profile("intake")), want: "profile"},
		{name: "forged manifest", principal: "consumer:one", binding: withQueryBindingHash(valid, "sha256:forged"), want: "manifest_hash"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NewQueryRuntimeAuthorization(test.principal, test.binding); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("NewQueryRuntimeAuthorization() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestQueryRuntimeAuthorizationCutChangesWithEveryDatabaseCoordinate(t *testing.T) {
	base := validQueryDatabaseBinding(t)
	baseAuthorization, err := NewQueryRuntimeAuthorization("consumer:one", base)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		mutate func(*dbrole.RuntimeBinding)
	}{
		{name: "database", mutate: func(binding *dbrole.RuntimeBinding) { binding.Database = "ahe_other" }},
		{name: "schema", mutate: func(binding *dbrole.RuntimeBinding) { binding.Schema = "ahe_other" }},
		{name: "role", mutate: func(binding *dbrole.RuntimeBinding) { binding.Role = "ahe_query_other" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := base
			test.mutate(&candidate)
			authorization, err := NewQueryRuntimeAuthorization("consumer:one", candidate)
			if err != nil {
				t.Fatal(err)
			}
			if reflect.DeepEqual(authorization.Binding().VisibilityCut, baseAuthorization.Binding().VisibilityCut) {
				t.Fatal("changed database coordinate produced the same visibility cut")
			}
			if authorization.readViewOwner == baseAuthorization.readViewOwner {
				t.Fatal("changed database coordinate produced the same read-view owner")
			}
		})
	}
}

func TestQueryReadViewOwnerFingerprintIncludesCompleteRuntimeBinding(t *testing.T) {
	base := QueryRuntimeBinding{
		SchemaVersion: QueryRuntimeAuthorizationVersion,
		PrincipalID:   "consumer:one",
		VisibilityCut: QueryVisibilityCut{
			Database:     "ahe",
			Schema:       "public",
			DatabaseRole: "ahe_query_runtime",
			Profile:      dbrole.ProfileQuery,
			ManifestHash: "sha256:manifest",
		},
	}
	baseOwner, err := queryRuntimeReadViewOwner(base)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		mutate func(*QueryRuntimeBinding)
	}{
		{name: "schema version", mutate: func(binding *QueryRuntimeBinding) { binding.SchemaVersion = "other" }},
		{name: "principal", mutate: func(binding *QueryRuntimeBinding) { binding.PrincipalID = "consumer:two" }},
		{name: "database", mutate: func(binding *QueryRuntimeBinding) { binding.VisibilityCut.Database = "other" }},
		{name: "schema", mutate: func(binding *QueryRuntimeBinding) { binding.VisibilityCut.Schema = "other" }},
		{name: "role", mutate: func(binding *QueryRuntimeBinding) { binding.VisibilityCut.DatabaseRole = "other" }},
		{name: "profile", mutate: func(binding *QueryRuntimeBinding) { binding.VisibilityCut.Profile = dbrole.Profile("intake") }},
		{name: "manifest", mutate: func(binding *QueryRuntimeBinding) { binding.VisibilityCut.ManifestHash = "sha256:other" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := base
			test.mutate(&candidate)
			owner, err := queryRuntimeReadViewOwner(candidate)
			if err != nil {
				t.Fatal(err)
			}
			if owner == baseOwner {
				t.Fatal("changed binding produced the same owner fingerprint")
			}
		})
	}
}

func TestAuthorizedQueryBackendExposesExactClosedRegistry(t *testing.T) {
	authorization := newTestQueryRuntimeAuthorization(t)
	underlying := &recordingQueryBackend{tools: queryTools(), expectedOwner: authorization.readViewOwner}
	wrapped, err := NewAuthorizedQueryBackend(underlying, authorization)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := toolNames(wrapped.Tools()), toolNames(queryTools()); !reflect.DeepEqual(got, want) {
		t.Fatalf("Tools() = %v, want %v", got, want)
	}
	if got := wrapped.Binding(); !reflect.DeepEqual(got, authorization.Binding()) {
		t.Fatalf("Binding() = %+v, want %+v", got, authorization.Binding())
	}
	inbound, err := evidencequerymcp.BindCanonicalReadViewOwner(t.Context(), "caller-selected-owner")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wrapped.CallTool(inbound, evidencequerymcp.ToolGetEvidenceRecord, json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	if underlying.calls != 1 || underlying.name != evidencequerymcp.ToolGetEvidenceRecord || !underlying.ownerMatched {
		t.Fatalf("underlying call = (%d, %q)", underlying.calls, underlying.name)
	}
}

func TestQueryToolInventoryMatchesCoreTransportAndAuthorization(t *testing.T) {
	coreDefinitions := new(evidencequerymcp.Server).Tools()
	coreNames := make([]string, 0, len(coreDefinitions))
	for _, definition := range coreDefinitions {
		coreNames = append(coreNames, definition.Name)
	}
	transportNames := toolNames(queryTools())
	if !reflect.DeepEqual(transportNames, coreNames) {
		t.Fatalf("transport tools = %v, core tools = %v", transportNames, coreNames)
	}
	if !reflect.DeepEqual(queryRuntimeToolNames, coreNames) {
		t.Fatalf("authorization tools = %v, core tools = %v", queryRuntimeToolNames, coreNames)
	}
}

func TestAuthorizedQueryBackendFailsClosedOnInventoryDrift(t *testing.T) {
	authorization := newTestQueryRuntimeAuthorization(t)
	readOnly := true
	nonDestructive := false
	write := false
	destructive := true
	wrongReadOnly := append([]mcpstdio.Tool(nil), queryTools()...)
	wrongReadOnly[0].Annotations.ReadOnlyHint = &write
	wrongDestructive := append([]mcpstdio.Tool(nil), queryTools()...)
	wrongDestructive[0].Annotations.DestructiveHint = &destructive
	duplicate := append([]mcpstdio.Tool(nil), queryTools()...)
	duplicate[len(duplicate)-1] = duplicate[0]
	unknown := append([]mcpstdio.Tool(nil), queryTools()...)
	unknown[0] = mcpstdio.Tool{
		Name: "new_query_tool",
		Annotations: mcpstdio.Annotations{
			ReadOnlyHint:    &readOnly,
			DestructiveHint: &nonDestructive,
		},
	}
	missing := append([]mcpstdio.Tool(nil), queryTools()[:len(queryTools())-1]...)
	for _, test := range []struct {
		name  string
		tools []mcpstdio.Tool
	}{
		{name: "missing", tools: missing},
		{name: "unknown", tools: unknown},
		{name: "duplicate", tools: duplicate},
		{name: "write", tools: wrongReadOnly},
		{name: "destructive", tools: wrongDestructive},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NewAuthorizedQueryBackend(
				&recordingQueryBackend{tools: test.tools},
				authorization,
			); err == nil {
				t.Fatal("NewAuthorizedQueryBackend() error = nil")
			}
		})
	}
	if _, err := NewAuthorizedQueryBackend(nil, authorization); err == nil {
		t.Fatal("nil backend accepted")
	}
	if _, err := NewAuthorizedQueryBackend(&recordingQueryBackend{tools: queryTools()}, nil); err == nil {
		t.Fatal("nil authorization accepted")
	}
}

func TestAuthorizedQueryBackendRejectsUnregisteredDirectCall(t *testing.T) {
	for _, name := range []string{"unregistered_query_tool"} {
		t.Run(name, func(t *testing.T) {
			underlying := &recordingQueryBackend{tools: queryTools()}
			wrapped, err := NewAuthorizedQueryBackend(underlying, newTestQueryRuntimeAuthorization(t))
			if err != nil {
				t.Fatal(err)
			}
			_, err = wrapped.CallTool(t.Context(), name, json.RawMessage(`{}`))
			if !errors.Is(err, runtimeauth.ErrUnauthorized) {
				t.Fatalf("CallTool() error = %v, want ErrUnauthorized", err)
			}
			if underlying.calls != 0 {
				t.Fatalf("underlying calls = %d, want zero", underlying.calls)
			}
		})
	}
}

func TestAuthorizedQueryBackendStdioListsExactClosedRegistry(t *testing.T) {
	underlying := &recordingQueryBackend{tools: queryTools()}
	wrapped, err := NewAuthorizedQueryBackend(underlying, newTestQueryRuntimeAuthorization(t))
	if err != nil {
		t.Fatal(err)
	}
	server, err := mcpstdio.NewServer("query-auth-test", "test", wrapped)
	if err != nil {
		t.Fatal(err)
	}
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"open_canonical_read_view","arguments":{}}}`,
	}, "\n") + "\n"
	var output bytes.Buffer
	if err := server.Serve(t.Context(), strings.NewReader(input), &output); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("stdio lines = %d, want 2: %s", len(lines), output.String())
	}
	for _, name := range queryRuntimeToolNames {
		if !strings.Contains(lines[0], `"name":"`+name+`"`) {
			t.Fatalf("tools/list is missing %q: %s", name, lines[0])
		}
	}
	for _, name := range []string{
		evidencequerymcp.ToolOpenCanonicalReadView,
		evidencequerymcp.ToolFindCanonicalPath,
		evidencequerymcp.ToolGetCanonicalTopologyDiagnostics,
		evidencequerymcp.ToolGetCanonicalSupersessionHead,
	} {
		if !strings.Contains(lines[0], `"name":"`+name+`"`) {
			t.Fatalf("tools/list is missing topology tool %q: %s", name, lines[0])
		}
	}
	if !strings.Contains(lines[1], `"ok":true`) {
		t.Fatalf("topology stdio call response = %s", lines[1])
	}
	if underlying.calls != 1 || underlying.name != evidencequerymcp.ToolOpenCanonicalReadView {
		t.Fatalf("underlying call = %d/%q", underlying.calls, underlying.name)
	}
}

func validQueryDatabaseBinding(t *testing.T) dbrole.RuntimeBinding {
	t.Helper()
	manifest, err := dbrole.BuildManifest(dbrole.ProfileQuery)
	if err != nil {
		t.Fatal(err)
	}
	hash, err := manifest.Hash()
	if err != nil {
		t.Fatal(err)
	}
	return dbrole.RuntimeBinding{
		SchemaVersion: dbrole.PolicyVersion,
		Profile:       dbrole.ProfileQuery,
		Role:          "ahe_query_runtime",
		Database:      "ahe",
		Schema:        "ahe",
		ManifestHash:  hash,
	}
}

func newTestQueryRuntimeAuthorization(t *testing.T) *QueryRuntimeAuthorization {
	t.Helper()
	authorization, err := NewQueryRuntimeAuthorization("consumer:one", validQueryDatabaseBinding(t))
	if err != nil {
		t.Fatal(err)
	}
	return authorization
}

func withQueryBindingProfile(binding dbrole.RuntimeBinding, profile dbrole.Profile) dbrole.RuntimeBinding {
	binding.Profile = profile
	return binding
}

func withQueryBindingHash(binding dbrole.RuntimeBinding, hash string) dbrole.RuntimeBinding {
	binding.ManifestHash = hash
	return binding
}

func toolNames(tools []mcpstdio.Tool) []string {
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		names = append(names, tool.Name)
	}
	return names
}
