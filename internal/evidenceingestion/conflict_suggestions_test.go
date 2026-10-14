package evidenceingestion

import (
	"strings"
	"testing"
)

func TestConflictIdentityDoesNotCollapseUnknownSources(t *testing.T) {
	meta := map[string]string{externalSourceOriginSystemKey: "jira", externalSourceOriginNamespaceKey: "example", externalSourceOriginObjectTypeKey: "issue", externalSourceOriginObjectIDKey: "unknown"}
	if _, ok := conflictObjectIdentity(meta); ok {
		t.Fatal("unknown identity treated as shared object")
	}
	meta[externalSourceOriginObjectIDKey] = "ABC-1"
	a, ok := conflictObjectIdentity(meta)
	if !ok {
		t.Fatal("known identity lost")
	}
	meta[externalSourceOriginNamespaceKey] = "another"
	b, _ := conflictObjectIdentity(meta)
	if sameConflictObject(a, b) {
		t.Fatal("cross-namespace object collision")
	}
}
func TestConflictLexicalBoundsAndHan(t *testing.T) {
	terms, _ := conflictTerms("Claim CLAIM withdrawal 停止使用 證據")
	if !terms["claim"] || !terms["停止"] || !terms["止使"] || !terms["使用"] {
		t.Fatal(terms)
	}
	text := []string{}
	for i := 0; i < 300; i++ {
		text = append(text, string(rune(0x4e00+i))+string(rune(0x5000+i)))
	}
	got, cut := conflictTerms(strings.Join(text, " "))
	if !cut || len(got) != 256 {
		t.Fatal("unbounded lexical vocabulary")
	}
}
