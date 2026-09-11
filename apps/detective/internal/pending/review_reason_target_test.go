//go:build darwin || linux

package pending

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/ahemcp"
)

func TestReasonTargetRequestPreservesInputsAndGenerationContract(t *testing.T) {
	_, decision := reasonDecisionFixture(t, "admit", "僅接受來源所報告的界定能力；不證明部署、語意真實性或人工核准。")
	inputs, err := buildReasonInputs(decision)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint := decision.ReviewBundle.Checkpoint
	claim, err := ahemcp.ProposalStatement(checkpoint.Batch.Extractor, checkpoint.Batch.Rows[0].Result.Records[0])
	if err != nil || inputs.Source != checkpoint.RawText || inputs.Claim != claim || inputs.Reason != decision.Reason || inputs.Review != decision.ReviewBundle.Review.Display.PayloadUTF8 || inputs.Decision != decision.Decision {
		t.Fatal("reason targeting changed a complete saved input")
	}
	inputJSON, err := json.Marshal(inputs)
	if err != nil {
		t.Fatal(err)
	}
	body, err := reasonRequest(reasonTestModel, inputs)
	if err != nil {
		t.Fatal(err)
	}
	var request map[string]json.RawMessage
	if err := json.Unmarshal(body, &request); err != nil {
		t.Fatal(err)
	}
	keys := []string{"format", "messages", "model", "options", "shift", "stream", "think", "truncate"}
	if len(request) != len(keys) {
		t.Fatal("request gained or lost a field, including possible tool authority")
	}
	for _, key := range keys {
		if _, ok := request[key]; !ok {
			t.Fatalf("request lost explicit field %q", key)
		}
	}
	if string(request["model"]) != strconv.Quote(reasonTestModel) {
		t.Fatal("request changed the explicitly selected model")
	}
	for _, key := range []string{"shift", "stream", "think", "truncate"} {
		if string(request[key]) != "false" {
			t.Fatalf("request changed explicit false field %q", key)
		}
	}
	var options map[string]json.RawMessage
	if err := json.Unmarshal(request["options"], &options); err != nil || len(options) != 2 || string(options["temperature"]) != "0" || string(options["num_predict"]) != "2048" {
		t.Fatal("prompt-only change altered the original generation options")
	}
	var messages []map[string]string
	if err := json.Unmarshal(request["messages"], &messages); err != nil || len(messages) != 2 || len(messages[0]) != 2 || len(messages[1]) != 2 || messages[0]["role"] != "system" || messages[0]["content"] != reasonInstruction || messages[1]["role"] != "user" || !bytes.Equal([]byte(messages[1]["content"]), inputJSON) {
		t.Fatal("request changed message roles or the exact complete input JSON")
	}
	var schema map[string]json.RawMessage
	if err := json.Unmarshal(request["format"], &schema); err != nil || len(schema) != 1 {
		t.Fatal("prompt-only change altered the root schema")
	}
	var branches []struct {
		Properties json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(schema["oneOf"], &branches); err != nil || len(branches) != 2 {
		t.Fatal("request lost its two original schema branches")
	}
	for _, branch := range branches {
		decoder := json.NewDecoder(bytes.NewReader(branch.Properties))
		if token, err := decoder.Token(); err != nil || token != json.Delim('{') {
			t.Fatal("branch properties are not an ordered JSON object")
		}
		var order []string
		for decoder.More() {
			token, err := decoder.Token()
			key, ok := token.(string)
			if err != nil || !ok {
				t.Fatal("invalid property key")
			}
			order = append(order, key)
			var value json.RawMessage
			if err := decoder.Decode(&value); err != nil {
				t.Fatal(err)
			}
		}
		if token, err := decoder.Token(); err != nil || token != json.Delim('}') {
			t.Fatal("incomplete properties object")
		}
		if _, err := decoder.Token(); err != io.EOF || !reflect.DeepEqual(order, []string{"concerns", "summary", "verdict"}) {
			t.Fatal("prompt-only change altered the concerns-first wire order")
		}
	}
}

func TestReasonTargetV6PreservesStructuralAdviceWithoutJudgingMeaning(t *testing.T) {
	if reasonPromptVersion != "detective-reason-adviser/v6" {
		t.Fatal("new reason-targeting advice must identify prompt v6")
	}
	// These replies deliberately miss the intended semantic rubric. Passing
	// structural validation must not translate, repair, or endorse their meaning.
	for _, tt := range []struct {
		name   string
		reason string
		reply  reasonModelReply
	}{
		{"citation_only_accepted", "因為有附來源，所以採納。", reasonModelReply{
			Verdict: "no_specific_concern", Summary: "A citation alone proves deployment and makes this admission approved.", Concerns: []reasonModelConcern{},
		}},
		{"bounded_report_overchallenged", "只接受來源報告的界定能力，不證明正式部署。", reasonModelReply{
			Verdict: "follow_up_needed", Summary: "A bounded report cannot be admitted without production deployment.",
			Concerns: []reasonModelConcern{{Kind: "missing_justification", Explanation: "Every reported capability requires deployment proof.", Question: "Where is proof of production deployment?", AnchorIDs: []string{"a0001", "a0000"}}},
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			decisionPath, decision := reasonDecisionFixture(t, "admit", tt.reason)
			before, err := os.ReadFile(decisionPath)
			if err != nil {
				t.Fatal(err)
			}
			replyJSON, err := json.Marshal(tt.reply)
			if err != nil {
				t.Fatal(err)
			}
			server, calls := reasonServer(t, reasonInventoryFixture(), reasonEnvelopeFixture(string(replyJSON)), nil)
			path := filepath.Join(filepath.Dir(decisionPath), "assessment.json")
			assessment, err := AssessReviewReason(t.Context(), decisionPath, server.URL, reasonTestModel, path)
			if err != nil {
				t.Fatal(err)
			}
			if calls.Load() != 1 || assessment.SchemaVersion != "detective-reason-assessment/v2" || assessment.PromptVersion != "detective-reason-adviser/v6" || assessment.AuthorityEffect != "none" || assessment.Validation != "closed_schema_and_exact_quotes_only" || assessment.DecisionDigest != decision.Digest {
				t.Fatal("new advice changed version, request count, or authority")
			}
			if assessment.Verdict != tt.reply.Verdict || assessment.Summary != tt.reply.Summary || len(assessment.Concerns) != len(tt.reply.Concerns) {
				t.Fatal("structural validation silently repaired or translated model advice")
			}
			for i, concern := range assessment.Concerns {
				original := tt.reply.Concerns[i]
				if concern.Kind != original.Kind || concern.Explanation != original.Explanation || concern.Question != original.Question {
					t.Fatal("structural validation rewrote the model concern")
				}
			}
			loaded, err := LoadReasonAssessment(path)
			if err != nil || !reflect.DeepEqual(loaded, assessment) {
				t.Fatal("saved v6 advice did not round trip exactly", err)
			}
			var display bytes.Buffer
			if err := WriteReasonAssessmentText(&display, loaded); err != nil || !strings.Contains(display.String(), strconv.Quote(tt.reply.Summary)) || !strings.Contains(display.String(), "未驗證模型的語意判斷") {
				t.Fatal("display translated the model or lost the semantic validation boundary", err)
			}
			if _, err := LoadReviewDecision(path); err == nil {
				t.Fatal("an advisory was accepted as an executable decision")
			}
			after, err := os.ReadFile(decisionPath)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("assessing or displaying advice changed the saved decision", err)
			}
		})
	}
}

func TestReasonTargetHistoricalV4EnglishAdviceLoadsAndDisplaysUnchanged(t *testing.T) {
	_, decision := reasonDecisionFixture(t, "admit", "因為有附來源，所以採納。")
	assessment := ReasonAssessment{
		SchemaVersion: "detective-reason-assessment/v1", PromptVersion: "detective-reason-adviser/v4",
		Model: reasonTestModel, ModelDigest: reasonTestDigest, DecisionDigest: decision.Digest,
		ReviewDisplayID: decision.ConfirmedDisplayID, ReviewDecision: decision,
		Verdict: "no_specific_concern", Summary: "The source citation alone is sufficient to admit this claim.", Concerns: []ReasonConcern{},
		AuthorityEffect: "none", Validation: "closed_schema_and_exact_quotes_only",
	}
	var err error
	assessment.Digest, err = reasonAssessmentHash(assessment)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(checkpointDirectory(t), "historical-v4-assessment.json")
	before := saveReviewTestJSON(t, path, assessment)
	loaded, err := LoadReasonAssessment(path)
	if err != nil || !reflect.DeepEqual(loaded, assessment) {
		t.Fatal("v5 compatibility rejected or rewrote historical English v4 advice", err)
	}
	var display bytes.Buffer
	if err := WriteReasonAssessmentText(&display, loaded); err != nil || !strings.Contains(display.String(), strconv.Quote(assessment.Summary)) || !strings.Contains(display.String(), "未驗證模型的語意判斷") {
		t.Fatal("historical English advice was translated or treated as semantic approval", err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("offline load or display upgraded historical assessment bytes", err)
	}
}
