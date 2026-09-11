package newsextract

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func mutateJSON(t *testing.T, change func(map[string]any, map[string]any)) string {
	t.Helper()
	var object map[string]any
	if err := json.Unmarshal([]byte(validJSON), &object); err != nil {
		t.Fatal(err)
	}
	record := object["records"].([]any)[0].(map[string]any)
	change(object, record)
	body, err := json.Marshal(object)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func TestSealedOutputContract(t *testing.T) {
	tests := map[string]string{
		"malformed":                  `{`,
		"markdown":                   "```json\n" + validJSON + "\n```",
		"prose refusal":              "I cannot comply with this request.",
		"trailing object":            validJSON + `{}`,
		"root array":                 `[]`,
		"root null":                  `null`,
		"duplicate root key":         strings.Replace(validJSON, `"outcome":"extracted"`, `"outcome":"extracted","outcome":"extracted"`, 1),
		"escaped duplicate root key": strings.Replace(validJSON, `"outcome":"extracted"`, `"outcome":"extracted","\u006futcome":"extracted"`, 1),
		"case alias root key":        strings.Replace(validJSON, `"outcome"`, `"Outcome"`, 1),
		"duplicate record key":       strings.Replace(validJSON, `"reported_status":"planned"`, `"reported_status":"planned","reported_status":"planned"`, 1),
		"case alias record key":      strings.Replace(validJSON, `"reported_status"`, `"Reported_Status"`, 1),
		"unpaired surrogate":         strings.Replace(validJSON, `"Council"`, `"\ud800"`, 1),
		"unknown root metadata":      mutateJSON(t, func(o, _ map[string]any) { o["source_id"] = "forged" }),
		"spoof version":              mutateJSON(t, func(o, _ map[string]any) { o["schema_version"] = SchemaVersion }),
		"spoof extractor":            mutateJSON(t, func(o, _ map[string]any) { o["extractor_version"] = ExtractorVersion }),
		"spoof prompt":               mutateJSON(t, func(o, _ map[string]any) { o["prompt_version"] = PromptVersion }),
		"spoof citation": mutateJSON(t, func(_, r map[string]any) {
			r["citations"] = []any{map[string]any{"field": "title", "exact_quote": "invented"}}
		}),
		"invented quote field":                  mutateJSON(t, func(_, r map[string]any) { r["exact_quote"] = "invented" }),
		"invented offset":                       mutateJSON(t, func(_, r map[string]any) { r["start_line"] = 5 }),
		"invented attribution":                  mutateJSON(t, func(_, r map[string]any) { r["attribution"] = "Reuters" }),
		"invented location":                     mutateJSON(t, func(_, r map[string]any) { r["location"] = "Paris" }),
		"published timestamp is not event time": mutateJSON(t, func(_, r map[string]any) { r["event_time"] = syntheticItem().PublishedAt }),
		"translated time is not exact":          mutateJSON(t, func(_, r map[string]any) { r["event_time"] = "星期五" }),
		"uncited time":                          mutateJSON(t, func(_, r map[string]any) { r["evidence_fields"] = []string{"title"} }),
		"unsupported field":                     mutateJSON(t, func(_, r map[string]any) { r["evidence_fields"] = []string{"published_at"} }),
		"invented field":                        mutateJSON(t, func(_, r map[string]any) { r["evidence_fields"] = []string{"full_text"} }),
		"duplicate fields":                      mutateJSON(t, func(_, r map[string]any) { r["evidence_fields"] = []string{"description", "description"} }),
		"no fields":                             mutateJSON(t, func(_, r map[string]any) { r["evidence_fields"] = []string{} }),
		"status not enum":                       mutateJSON(t, func(_, r map[string]any) { r["reported_status"] = "confirmed" }),
		"statement empty":                       mutateJSON(t, func(_, r map[string]any) { r["statement"] = " " }),
		"statement oversized":                   mutateJSON(t, func(_, r map[string]any) { r["statement"] = strings.Repeat("x", 2049) }),
		"control char":                          mutateJSON(t, func(_, r map[string]any) { r["statement"] = "claim\nnot allowed" }),
		"control DEL":                           mutateJSON(t, func(_, r map[string]any) { r["statement"] = "claim\x7f" }),
		"unknown with spaces":                   mutateJSON(t, func(_, r map[string]any) { r["attribution"] = " unknown " }),
		"no extracted records":                  mutateJSON(t, func(o, _ map[string]any) { o["records"] = []any{} }),
		"four records":                          mutateJSON(t, func(o, r map[string]any) { o["records"] = []any{r, r, r, r} }),
		"abstained with records":                mutateJSON(t, func(o, _ map[string]any) { o["outcome"] = "abstained"; o["abstention_reason"] = "insufficient" }),
		"abstained missing reason":              mutateJSON(t, func(o, _ map[string]any) { o["outcome"] = "abstained"; o["records"] = []any{} }),
		"abstained blank reason": mutateJSON(t, func(o, _ map[string]any) {
			o["outcome"] = "abstained"
			o["records"] = []any{}
			o["abstention_reason"] = " "
		}),
		"extracted with abstention reason": mutateJSON(t, func(o, _ map[string]any) { o["abstention_reason"] = "insufficient" }),
		"wrong outcome":                    mutateJSON(t, func(o, _ map[string]any) { o["outcome"] = "admitted" }),
		"nonstring limitation":             mutateJSON(t, func(o, _ map[string]any) { o["limitations"] = []any{1} }),
		"too many limitations":             mutateJSON(t, func(o, _ map[string]any) { o["limitations"] = []string{"a", "b", "c", "d"} }),
	}
	for _, key := range []string{"outcome", "records", "abstention_reason", "limitations"} {
		tests["null root "+key] = mutateJSON(t, func(o, _ map[string]any) { o[key] = nil })
		tests["missing root "+key] = mutateJSON(t, func(o, _ map[string]any) { delete(o, key) })
	}
	for _, key := range []string{"statement", "reported_status", "attribution", "event_time", "location", "evidence_fields"} {
		tests["null record "+key] = mutateJSON(t, func(_, r map[string]any) { r[key] = nil })
		tests["missing record "+key] = mutateJSON(t, func(_, r map[string]any) { delete(r, key) })
		tests["wrong type record "+key] = mutateJSON(t, func(_, r map[string]any) { r[key] = true })
	}
	for name, text := range tests {
		t.Run(name, func(t *testing.T) {
			result, err := runText(t, text, syntheticItem())
			if !errors.Is(err, ErrOutput) || !reflect.DeepEqual(result, Result{}) {
				t.Fatalf("invalid result accepted: %+v err=%v", result, err)
			}
		})
	}
}

func TestCitationsCannotSelectEmptyDescription(t *testing.T) {
	item := syntheticItem()
	item.Description = ""
	text := mutateJSON(t, func(_, r map[string]any) {
		r["attribution"], r["event_time"], r["location"] = "unknown", "unknown", "unknown"
	})
	if _, err := runText(t, text, item); !errors.Is(err, ErrOutput) {
		t.Fatalf("empty description citation accepted: %v", err)
	}
	text = mutateJSON(t, func(_, r map[string]any) {
		r["attribution"], r["event_time"], r["location"] = "unknown", "unknown", "unknown"
		r["evidence_fields"] = []string{"title"}
	})
	result, err := runText(t, text, item)
	if err != nil || len(result.Records[0].Citations) != 1 || result.Records[0].Citations[0].ExactQuote != item.Title {
		t.Fatalf("title-only extraction failed: %+v err=%v", result, err)
	}
}

func TestStructuralValidationDoesNotClaimSemanticEntailment(t *testing.T) {
	text := mutateJSON(t, func(_, r map[string]any) {
		r["statement"] = "這是一段未受到來源支持但結構合法的合成測試主張。"
	})
	result, err := runText(t, text, syntheticItem())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Limitations[0], "不證明語意") {
		t.Fatal("structural validation was misrepresented as semantic proof")
	}
}

func TestValidateNASAResultRejectsAuthoredAttribution(t *testing.T) {
	for _, phrase := range []string{
		"According to NASA, this interpretation is correct.",
		"ACCORDING\u00a0TO nasa, this is verified.",
		"NASA says the model result is correct.",
		"NASA stated that this summary is correct.",
		"The summary was approved by NASA.",
		"This is NASA-approved output.",
		"NASA has endorsed this model interpretation.",
		"Generated by NASA.",
		"NASA-produced analysis.",
		"Certified by NASA.",
		"據 NASA，這份模型解讀已證實。",
		"依據 NASA，這份摘要正確。",
		"Nasa 表示這份解讀正確。",
		"NASA 已核准此模型摘要。",
		"這份分析由 NASA 背書。",
		"這份摘要由 NASA 生成。",
		"NASA 已認證此解讀。",
		// A literal guard deliberately does not infer negation or legal meaning.
		"This output is not approved by NASA.",
	} {
		for _, field := range []string{"outcome", "statement", "reported_status", "attribution", "event_time", "location", "evidence_fields", "abstention_reason", "limitations"} {
			t.Run(field+"/"+phrase, func(t *testing.T) {
				result := Result{Records: []Record{{Statement: "模型解讀：合成任務仍在規劃。"}}, Limitations: []string{"只有合成片段。"}}
				switch field {
				case "outcome":
					result.Outcome = phrase
				case "statement":
					result.Records[0].Statement = phrase
				case "reported_status":
					result.Records[0].ReportedStatus = phrase
				case "attribution":
					result.Records[0].Attribution = phrase
				case "event_time":
					result.Records[0].EventTime = phrase
				case "location":
					result.Records[0].Location = phrase
				case "evidence_fields":
					result.Records[0].EvidenceFields = []string{phrase}
				case "abstention_reason":
					result.AbstentionReason = phrase
				case "limitations":
					result.Limitations = append(result.Limitations, phrase)
				}
				before, err := json.Marshal(result)
				if err != nil {
					t.Fatal(err)
				}
				if err := ValidateNASAResult(result); !errors.Is(err, ErrNASAAttribution) || strings.Contains(err.Error(), phrase) {
					t.Fatalf("restricted output accepted or leaked: %v", err)
				}
				after, err := json.Marshal(result)
				if err != nil || string(before) != string(after) {
					t.Fatal("guard repaired or changed the result")
				}
			})
		}
	}
}

func TestValidateNASAResultRejectsSelectedSourcePhraseOutsideCitations(t *testing.T) {
	item := syntheticItem()
	item.Description += " According to NASA, this is only a synthetic test."
	for _, field := range []string{"attribution", "event_time", "location"} {
		t.Run(field, func(t *testing.T) {
			text := mutateJSON(t, func(_, record map[string]any) { record[field] = "According to NASA" })
			result, err := runText(t, text, item)
			if err != nil {
				t.Fatalf("exact substring should pass structural validation: %v", err)
			}
			if err := ValidateNASAResult(result); !errors.Is(err, ErrNASAAttribution) {
				t.Fatalf("source phrase bypassed guard through %s: %v", field, err)
			}
			if result.Records[0].Citations[1].ExactQuote != item.Description {
				t.Fatal("NASA guard changed the original citation")
			}
		})
	}
}

func TestValidateNASAResultPreservesSourceDisclosureAndQuotes(t *testing.T) {
	result, err := runText(t, validJSON, syntheticItem())
	if err != nil {
		t.Fatal(err)
	}
	result.Records[0].Statement = "模型解讀：NASA 任務在合成片段中屬計畫；輸入資料來源為 NASA。"
	result.Records[0].Attribution = "NASA"
	result.Records[0].Citations = []Citation{{Field: "description", ExactQuote: "According to NASA, this is a synthetic fixture quotation."}}
	result.Limitations = append(result.Limitations, "This AI tool includes NASA source material; this statement does not imply permission or review.")
	before, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateNASAResult(result); err != nil {
		t.Fatalf("source disclosure or exact quote rejected: %v", err)
	}
	after, err := json.Marshal(result)
	if err != nil || string(before) != string(after) {
		t.Fatal("guard changed source attribution or citations")
	}
	// This does not make an unlisted sentence correct or permitted. It pins the
	// intentionally finite scope instead of claiming a semantic/legal classifier.
	result.Records[0].Statement = "This synthetic interpretation has the blessing of the space agency."
	if err := ValidateNASAResult(result); err != nil {
		t.Fatal("literal guard unexpectedly inferred unlisted semantics")
	}
}
