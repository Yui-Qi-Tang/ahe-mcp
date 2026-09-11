package sourcepilot

import (
	"strings"
	"testing"
)

func TestNeutralInstructionOnlyRemovesPersona(t *testing.T) {
	const persona = "你是來源閱讀助手，"
	if !strings.HasPrefix(SegmentInstruction, persona) {
		t.Fatal("historical instruction no longer has the frozen persona prefix")
	}
	if got := strings.TrimPrefix(SegmentInstruction, persona); got != NeutralInstruction {
		t.Fatal("neutral baseline changed more than the persona prefix")
	}
	if NeutralPromptVersion == SegmentPromptVersion || strings.Contains(NeutralInstruction, persona) {
		t.Fatal("neutral baseline must have its own version and no persona prefix")
	}
}
