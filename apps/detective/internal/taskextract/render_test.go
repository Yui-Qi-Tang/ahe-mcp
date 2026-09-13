package taskextract_test

import (
	"bytes"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/taskextract"
)

func TestTaskRecordTextPreservesReviewContextWithoutAuthority(t *testing.T) {
	request, _, record := recordFixture(t, `{"outcome":"selected","ranges":[{"start_id":"u001","end_id":"u002"}],"reason":"MODEL_NOTE_NOT_SOURCE"}`)
	before := recordJSON(t, record)
	var out bytes.Buffer
	if err := taskextract.WriteRecordText(&out, record); err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{
		"未經人工審閱", "沒有 AHE／DB 寫入", "MODEL_NOTE_NOT_SOURCE", "非人工理由",
		"未取得：comments", "已取得但未提供選段：history", "HISTORY_NOT_FOR_MODEL",
		"Synthetic release ticket", "https://example.invalid/tickets/TEST-7", "revision-1",
		"full_document", "未選段落：u003", "事實完整率：未評估", "The latest recovery rehearsal failed",
		"The team has not completed that verification.", "The design team will choose a new banner color",
	} {
		if !strings.Contains(out.String(), required) {
			t.Errorf("human display omitted %q", required)
		}
	}
	if !bytes.Equal(before, recordJSON(t, record)) {
		t.Fatal("rendering modified saved source or candidate bytes")
	}
	out.Reset()
	if err := taskextract.WriteInspectionText(&out, request); err != nil || strings.Contains(out.String(), "候選 1") || !strings.Contains(out.String(), "HISTORY_NOT_FOR_MODEL") || !strings.Contains(out.String(), "尚未執行選段的段落") {
		t.Fatal("offline inspection fabricated selection or omitted source", err)
	}
}

func TestTaskDisplayEscapesTerminalControlsWithoutChangingSource(t *testing.T) {
	task, source := contractInput()
	source.Parts[0].Text = "Literal <script> text.\r\nNew line.\n\nA\x1b[31mB\u202eC\bD\tE.\rLone carriage return."
	request := prepareContract(t, task, source, "synthetic-selector")
	var out bytes.Buffer
	if err := taskextract.WriteInspectionText(&out, request); err != nil {
		t.Fatal(err)
	}
	for _, unsafe := range []string{"\r", "\x1b", "\u202e", "\b"} {
		if strings.Contains(out.String(), unsafe) {
			t.Fatal("terminal control escaped its display boundary")
		}
	}
	for _, escaped := range []string{`\u000d`, `\u001b`, `\u202e`, `\u0008`, "\n    New line."} {
		if !strings.Contains(out.String(), escaped) {
			t.Errorf("display omitted escaped control or readable line break %q", escaped)
		}
	}
	if !strings.Contains(out.String(), "Literal <script> text.\n    New line.") ||
		!strings.Contains(out.String(), `E.\u000dLone carriage return.`) {
		t.Fatal("paired line breaks were noisy or a lone cursor control was unescaped")
	}
	if !reflect.DeepEqual(source, request.Source()) {
		t.Fatal("display escaping changed source")
	}
}

type taskShortWriter struct{}

func (taskShortWriter) Write(raw []byte) (int, error) { return len(raw) - 1, nil }

func TestTaskDisplayFailureHasNoCandidatesAndHandlesWriterErrors(t *testing.T) {
	request, result, _ := recordFixture(t, `{"outcome":"selected","ranges":[{"start_id":"u001","end_id":"u002"}],"reason":""}`)
	record, err := taskextract.NewRecord(request, result, errors.New("transport failed"))
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := taskextract.WriteRecordText(&out, record); err != nil || strings.Contains(out.String(), "候選 1") || !strings.Contains(out.String(), "失敗不等於成功棄答") || !strings.Contains(out.String(), "尚未完成選段的段落") {
		t.Fatal("failure rendered candidate or lost its status", err)
	}
	if err := taskextract.WriteRecordText(taskShortWriter{}, record); !errors.Is(err, io.ErrShortWrite) {
		t.Fatal("short write ignored", err)
	}
	if taskextract.WriteInspectionText(nil, request) == nil || taskextract.WriteInspectionText(&out, nil) == nil {
		t.Fatal("invalid display request accepted")
	}
}
