package taskextract_test

import (
	"reflect"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/taskextract"
)

func contractInput() (taskextract.Task, taskextract.Source) {
	return taskextract.Task{
		ID: "task-test-7", Revision: 1, Objective: "找出上線條件及未完成事項",
		SourceID: "ticket-test-7", Parts: []string{"description"},
	}, taskextract.Source{
		ID: "ticket-test-7", Revision: "revision-1", Title: "Synthetic release ticket",
		Location: "https://example.invalid/tickets/TEST-7", Coverage: "full_document",
		Limitations: []string{}, Parts: []taskextract.SourcePart{
			{Name: "description", Text: "The release requires a passing smoke test.\nThe team has not approved deployment.\n\nThe operator restores revision A if the smoke test fails."},
			{Name: "history", Text: "The team opened this synthetic ticket yesterday."},
		},
	}
}

func prepareContract(t *testing.T, task taskextract.Task, source taskextract.Source, modelName string) *taskextract.Request {
	t.Helper()
	request, err := taskextract.Prepare(task, source, modelName)
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	if request == nil || request.InputID() == "" {
		t.Fatal("Prepare() returned no bound input")
	}
	return request
}

func TestPrepareBindsTheCompleteTaskSourceAndModel(t *testing.T) {
	task, source := contractInput()
	before := prepareContract(t, task, source, "synthetic-selector").InputID()
	if repeated := prepareContract(t, task, source, "synthetic-selector").InputID(); repeated != before {
		t.Fatal("identical frozen input changed its identity")
	}
	tests := []struct {
		name   string
		mutate func(*taskextract.Task, *taskextract.Source, *string)
	}{
		{"task ID", func(task *taskextract.Task, _ *taskextract.Source, _ *string) { task.ID = "task-test-8" }},
		{"task revision", func(task *taskextract.Task, _ *taskextract.Source, _ *string) { task.Revision++ }},
		{"objective", func(task *taskextract.Task, _ *taskextract.Source, _ *string) {
			task.Objective = "找出回復方案及適用條件"
		}},
		{"requested parts", func(task *taskextract.Task, _ *taskextract.Source, _ *string) {
			task.Parts = append(task.Parts, "history")
		}},
		{"source identity", func(task *taskextract.Task, source *taskextract.Source, _ *string) {
			task.SourceID, source.ID = "ticket-test-8", "ticket-test-8"
		}},
		{"source revision", func(_ *taskextract.Task, source *taskextract.Source, _ *string) { source.Revision = "revision-2" }},
		{"source title", func(_ *taskextract.Task, source *taskextract.Source, _ *string) { source.Title += " updated" }},
		{"source location", func(_ *taskextract.Task, source *taskextract.Source, _ *string) { source.Location += "/details" }},
		{"source coverage", func(_ *taskextract.Task, source *taskextract.Source, _ *string) {
			source.Coverage = "exact_excerpt"
			source.Limitations = []string{"Only the supplied fields were collected."}
		}},
		{"provided original bytes", func(_ *taskextract.Task, source *taskextract.Source, _ *string) { source.Parts[0].Text += " " }},
		{"unprovided original bytes", func(_ *taskextract.Task, source *taskextract.Source, _ *string) { source.Parts[1].Text += " " }},
		{"model", func(_ *taskextract.Task, _ *taskextract.Source, modelName *string) {
			*modelName = "another-synthetic-selector"
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			task, source := contractInput()
			modelName := "synthetic-selector"
			test.mutate(&task, &source, &modelName)
			if got := prepareContract(t, task, source, modelName).InputID(); got == before {
				t.Fatal("changed extraction input retained the old identity")
			}
		})
	}

	task, source = contractInput()
	source.Coverage, source.Limitations = "exact_excerpt", []string{"Comments were not collected."}
	before = prepareContract(t, task, source, "synthetic-selector").InputID()
	source.Limitations[0] = "History was not collected."
	if prepareContract(t, task, source, "synthetic-selector").InputID() == before {
		t.Fatal("changed source limitations retained the old identity")
	}
}

func TestPrepareCopiesCallerAndReturnedSlices(t *testing.T) {
	task, source := contractInput()
	task.Parts = append(task.Parts, "comments")
	source.Coverage, source.Limitations = "exact_excerpt", []string{"Comments were not collected."}
	request := prepareContract(t, task, source, "synthetic-selector")
	wantID, wantUnits, wantScope := request.InputID(), request.Units(), request.Scope()
	wantTask, wantSource := request.Task(), request.Source()
	task.Parts[0] = "modified-task-part"
	source.Parts[0].Name, source.Parts[0].Text = "modified-source-part", "Modified source text."
	source.Limitations[0] = "Modified limitation."
	if request.InputID() != wantID || !reflect.DeepEqual(request.Units(), wantUnits) || !reflect.DeepEqual(request.Scope(), wantScope) {
		t.Fatal("caller-owned mutation changed prepared input")
	}
	units := request.Units()
	units[0].ID, units[0].Text = "modified-unit", "Modified quote."
	scope := request.Scope()
	for _, values := range [][]string{scope.RequestedParts, scope.ProvidedParts, scope.NotCollectedParts, scope.NotProvidedParts, scope.UnselectedUnitIDs} {
		if len(values) > 0 {
			values[0] = "modified-scope"
		}
	}
	if request.InputID() != wantID || !reflect.DeepEqual(request.Units(), wantUnits) || !reflect.DeepEqual(request.Scope(), wantScope) {
		t.Fatal("returned slice mutation changed prepared input")
	}
	taskCopy, sourceCopy := request.Task(), request.Source()
	taskCopy.Parts[0] = "modified-copy"
	sourceCopy.Parts[0].Text = "Modified copied source."
	sourceCopy.Limitations[0] = "Modified copied limitation."
	if !reflect.DeepEqual(request.Task(), wantTask) || !reflect.DeepEqual(request.Source(), wantSource) {
		t.Fatal("frozen task/source metadata was changed by caller or getter mutation")
	}
}

func TestPreparePreservesByteExactReadableParagraphs(t *testing.T) {
	task, source := contractInput()
	body := "  The operator keeps deployment disabled.\r\n\tUnless the owner approves, it remains disabled.\r\n \t\r\n  團隊會保留回復方案 🧪。\r\n<br> is literal source text, not a separator.\r\n```go\r\n\treturn false\r\n```"
	source.Parts = []taskextract.SourcePart{{Name: "description", Text: body}}
	units := prepareContract(t, task, source, "synthetic-selector").Units()
	if len(units) != 2 {
		t.Fatalf("got %d units, want two paragraphs, not lines or HTML fragments", len(units))
	}
	previousEnd := 0
	for _, unit := range units {
		if unit.ID == "" || unit.Part != "description" || unit.StartByte < previousEnd || unit.EndByte <= unit.StartByte || unit.EndByte > len(body) {
			t.Fatalf("invalid source coordinate: %+v", unit)
		}
		if unit.Text != body[unit.StartByte:unit.EndByte] || !utf8.ValidString(unit.Text) {
			t.Fatal("unit text is not the exact original UTF-8 byte slice")
		}
		previousEnd = unit.EndByte
	}
	if !strings.HasPrefix(units[0].Text, "  The operator") || !strings.Contains(units[0].Text, "\r\n\tUnless") {
		t.Fatal("paragraph indentation or CRLF condition was rewritten")
	}
	if !strings.Contains(units[1].Text, "🧪") || !strings.Contains(units[1].Text, "<br> is literal") || !strings.Contains(units[1].Text, "```go\r\n\treturn false\r\n```") {
		t.Fatal("Unicode, literal markup, or code text was rewritten")
	}
}

func TestPrepareKeepsIdenticalTextOccurrencesDistinct(t *testing.T) {
	task, source := contractInput()
	source.Parts = []taskextract.SourcePart{{Name: "description", Text: "Deployment is not approved.\n\nDeployment is not approved."}}
	units := prepareContract(t, task, source, "synthetic-selector").Units()
	if len(units) != 2 || units[0].ID == units[1].ID || units[0].StartByte == units[1].StartByte {
		t.Fatalf("same-text occurrences collapsed: %+v", units)
	}
}

func TestPrepareScopeSeparatesMissingFromNotProvided(t *testing.T) {
	task, source := contractInput()
	task.Parts = []string{"description", "comments"}
	request := prepareContract(t, task, source, "synthetic-selector")
	scope := request.Scope()
	for name, pair := range map[string][2][]string{
		"requested":     {scope.RequestedParts, []string{"description", "comments"}},
		"provided":      {scope.ProvidedParts, []string{"description"}},
		"not collected": {scope.NotCollectedParts, []string{"comments"}},
		"not provided":  {scope.NotProvidedParts, []string{"history"}},
	} {
		if !sameStrings(pair[0], pair[1]) {
			t.Errorf("%s = %v, want %v", name, pair[0], pair[1])
		}
	}
	if scope.FactCompletenessAssessed {
		t.Fatal("preparing a source claimed factual completeness")
	}
	for _, unit := range request.Units() {
		if unit.Part != "description" {
			t.Fatal("an unrequested source part entered the model projection")
		}
	}

	task.Parts = []string{"comments"}
	request = prepareContract(t, task, source, "synthetic-selector")
	if len(request.Units()) != 0 || !sameStrings(request.Scope().NotCollectedParts, []string{"comments"}) {
		t.Fatal("all-missing source scope was not preserved for the caller")
	}
}

func TestPreparePreservesCollectedEmptyPartAndUnknownRevision(t *testing.T) {
	task, source := contractInput()
	source.Revision = "unknown"
	source.Parts = []taskextract.SourcePart{{Name: "description", Text: ""}}
	request := prepareContract(t, task, source, "synthetic-selector")
	if len(request.Units()) != 0 || len(request.Scope().NotCollectedParts) != 0 || !sameStrings(request.Scope().ProvidedParts, []string{"description"}) {
		t.Fatal("an observed empty field was mistaken for an uncollected field")
	}
}

func TestPrepareRejectsInvalidContracts(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*taskextract.Task, *taskextract.Source, *string)
	}{
		{"empty task ID", func(task *taskextract.Task, _ *taskextract.Source, _ *string) { task.ID = "" }},
		{"zero task revision", func(task *taskextract.Task, _ *taskextract.Source, _ *string) { task.Revision = 0 }},
		{"negative task revision", func(task *taskextract.Task, _ *taskextract.Source, _ *string) { task.Revision = -1 }},
		{"empty objective", func(task *taskextract.Task, _ *taskextract.Source, _ *string) { task.Objective = "" }},
		{"invalid objective UTF-8", func(task *taskextract.Task, _ *taskextract.Source, _ *string) { task.Objective = string([]byte{0xff}) }},
		{"NUL objective", func(task *taskextract.Task, _ *taskextract.Source, _ *string) { task.Objective += "\x00" }},
		{"source mismatch", func(task *taskextract.Task, _ *taskextract.Source, _ *string) { task.SourceID = "another-source" }},
		{"empty task source ID", func(task *taskextract.Task, _ *taskextract.Source, _ *string) { task.SourceID = "" }},
		{"missing task parts", func(task *taskextract.Task, _ *taskextract.Source, _ *string) { task.Parts = nil }},
		{"duplicate task parts", func(task *taskextract.Task, _ *taskextract.Source, _ *string) {
			task.Parts = []string{"description", "description"}
		}},
		{"empty task part", func(task *taskextract.Task, _ *taskextract.Source, _ *string) { task.Parts = []string{""} }},
		{"empty source ID", func(_ *taskextract.Task, source *taskextract.Source, _ *string) { source.ID = "" }},
		{"empty source revision", func(_ *taskextract.Task, source *taskextract.Source, _ *string) { source.Revision = "" }},
		{"missing source parts", func(_ *taskextract.Task, source *taskextract.Source, _ *string) { source.Parts = nil }},
		{"duplicate source parts", func(_ *taskextract.Task, source *taskextract.Source, _ *string) {
			source.Parts = append(source.Parts, source.Parts[0])
		}},
		{"empty source part", func(_ *taskextract.Task, source *taskextract.Source, _ *string) { source.Parts[0].Name = "" }},
		{"invalid source UTF-8", func(_ *taskextract.Task, source *taskextract.Source, _ *string) {
			source.Parts[0].Text = string([]byte{0xff})
		}},
		{"NUL source", func(_ *taskextract.Task, source *taskextract.Source, _ *string) { source.Parts[0].Text += "\x00" }},
		{"unknown coverage", func(_ *taskextract.Task, source *taskextract.Source, _ *string) { source.Coverage = "complete" }},
		{"null limitations", func(_ *taskextract.Task, source *taskextract.Source, _ *string) { source.Limitations = nil }},
		{"full source limitations", func(_ *taskextract.Task, source *taskextract.Source, _ *string) {
			source.Limitations = []string{"Something was not collected."}
		}},
		{"excerpt without limitations", func(_ *taskextract.Task, source *taskextract.Source, _ *string) { source.Coverage = "exact_excerpt" }},
		{"truncated without limitations", func(_ *taskextract.Task, source *taskextract.Source, _ *string) {
			source.Coverage = "truncated_document"
		}},
		{"empty model", func(_ *taskextract.Task, _ *taskextract.Source, modelName *string) { *modelName = "" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			task, source := contractInput()
			modelName := "synthetic-selector"
			test.mutate(&task, &source, &modelName)
			if _, err := taskextract.Prepare(task, source, modelName); err == nil {
				t.Fatal("Prepare() accepted an invalid contract")
			}
		})
	}
}

func TestPrepareRejectsLimitsWithoutSilentTruncation(t *testing.T) {
	task, source := contractInput()
	source.Parts = []taskextract.SourcePart{{Name: "description", Text: strings.Repeat("x", taskextract.MaxProvidedBytes)}}
	prepareContract(t, task, source, "synthetic-selector")
	source.Parts[0].Text += "x"
	if _, err := taskextract.Prepare(task, source, "synthetic-selector"); err == nil {
		t.Fatal("oversized provided text was accepted")
	}

	source.Parts = []taskextract.SourcePart{
		{Name: "description", Text: strings.Repeat("x", taskextract.MaxProvidedBytes)},
		{Name: "history", Text: strings.Repeat("y", taskextract.MaxSourceBytes-taskextract.MaxProvidedBytes)},
	}
	prepareContract(t, task, source, "synthetic-selector")
	source.Parts[1].Text += "y"
	if _, err := taskextract.Prepare(task, source, "synthetic-selector"); err == nil {
		t.Fatal("oversized frozen source was accepted because its extra text was not provided")
	}

	source.Parts = []taskextract.SourcePart{{Name: "description", Text: strings.Repeat("Readable unit.\n\n", taskextract.MaxUnits)}}
	if units := prepareContract(t, task, source, "synthetic-selector").Units(); len(units) != taskextract.MaxUnits {
		t.Fatalf("unit boundary returned %d units, want %d", len(units), taskextract.MaxUnits)
	}
	source.Parts[0].Text += "Another unit."
	if _, err := taskextract.Prepare(task, source, "synthetic-selector"); err == nil {
		t.Fatal("too many units were silently truncated")
	}

	task, source = contractInput()
	task.Parts, source.Parts = nil, nil
	for i := 0; i < taskextract.MaxParts; i++ {
		name := "part-" + string(rune('a'+i))
		task.Parts = append(task.Parts, name)
		source.Parts = append(source.Parts, taskextract.SourcePart{Name: name, Text: "A readable unit."})
	}
	prepareContract(t, task, source, "synthetic-selector")
	task.Parts = append(task.Parts, "extra-part")
	if _, err := taskextract.Prepare(task, source, "synthetic-selector"); err == nil {
		t.Fatal("too many requested parts were accepted")
	}
	task.Parts = task.Parts[:taskextract.MaxParts]
	source.Parts = append(source.Parts, taskextract.SourcePart{Name: "extra-part", Text: "Another readable unit."})
	if _, err := taskextract.Prepare(task, source, "synthetic-selector"); err == nil {
		t.Fatal("too many frozen source parts were accepted")
	}
}

func sameStrings(left, right []string) bool {
	left, right = slices.Clone(left), slices.Clone(right)
	slices.Sort(left)
	slices.Sort(right)
	return slices.Equal(left, right)
}

func TestPrepareBoundsMetadataAndEncodedInput(t *testing.T) {
	for _, field := range []string{"objective", "title", "location", "limitation", "model", "encoded input"} {
		t.Run(field, func(t *testing.T) {
			task, source := contractInput()
			modelName := "synthetic-selector"
			switch field {
			case "objective":
				task.Objective = strings.Repeat("x", 2049)
			case "title":
				source.Title = strings.Repeat("x", 1025)
			case "location":
				source.Location = strings.Repeat("x", 2049)
			case "limitation":
				source.Coverage = "exact_excerpt"
				source.Limitations = []string{strings.Repeat("x", 1025)}
			case "model":
				modelName = strings.Repeat("x", 201)
			case "encoded input":
				// JSON escapes each '<' into six ASCII bytes. A body within
				// its byte limit can still exceed the encoded prompt bound.
				source.Parts[0].Text = strings.Repeat("<", 12<<10)
			}
			if _, err := taskextract.Prepare(task, source, modelName); err == nil {
				t.Fatal("oversized metadata or encoded prompt was accepted")
			}
		})
	}
}
