package newsextract

import (
	"strings"
	"testing"

	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/newsfeed"
)

// This checks the instruction contract, not whether a real model obeys it.
func TestInstructionDefinesFragmentAdequacy(t *testing.T) {
	for _, rule := range []string{
		"可忠實轉述的獨立主張",
		"不是判斷事件影響、完整背景或世界真實性",
		"不得補寫截斷部分",
		"只有導覽文字、只有操作指令",
	} {
		if !strings.Contains(instruction, rule) {
			t.Errorf("missing fragment adequacy rule: %s", rule)
		}
	}
}

// Prewritten model answers exercise partial-source handling and unknown fields.
// They do not prove that an actual model extracts the right proposition.
func TestPartialSourceCandidatesKeepUnknownsAndCompleteCitations(t *testing.T) {
	for _, test := range []struct {
		name      string
		item      newsfeed.Item
		field     string
		statement string
	}{
		{"title only", newsfeed.Item{Title: "Birch Council plans a hearing", PublishedAt: "2026-09-10T00:00:00"}, "title", "模型解讀：片段描述 Birch Council 規劃一場聽證會。"},
		{"complete sentence before truncated tail", newsfeed.Item{Title: "Harbor Office update", Description: "<p>Harbor Office plans a review. The wider impact is [&hellip;]</p>\n"}, "description", "模型解讀：片段描述 Harbor Office 規劃一項審查。"},
	} {
		t.Run(test.name, func(t *testing.T) {
			answer := mutateJSON(t, func(_, record map[string]any) {
				for _, key := range []string{"attribution", "event_time", "location"} {
					record[key] = "unknown"
				}
				record["statement"] = test.statement
				record["evidence_fields"] = []string{test.field}
			})
			result, err := runText(t, answer, test.item)
			if err != nil {
				t.Fatal(err)
			}
			record := result.Records[0]
			quote := test.item.Title
			if test.field == "description" {
				quote = test.item.Description
			}
			if record.EventTime != "unknown" || record.Location != "unknown" || record.Attribution != "unknown" || len(record.Citations) != 1 || record.Citations[0].ExactQuote != quote {
				t.Fatal("partial candidate invented missing fields or rewrote its source")
			}
			if err := ValidateNASAResult(result); err != nil {
				t.Fatal(err)
			}
		})
	}
}
