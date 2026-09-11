package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestSourceInspectStaticHelp(t *testing.T) {
	for _, flag := range []string{"-h", "--help"} {
		var stdout, stderr bytes.Buffer
		err := runSource([]string{"inspect", flag}, strings.NewReader(""), &stdout, &stderr)
		if err != nil || !strings.Contains(stdout.String(), "source inspect -receipt") || !strings.Contains(stdout.String(), "offline") || stderr.Len() != 0 {
			t.Fatalf("inspect help was not available without I/O: %v", err)
		}
	}
}

func sourceInspectFixture(t *testing.T) (*sourceCLIHTTP, sourceCollection, string) {
	t.Helper()
	f := sourceCLIFixture(t, "")
	rawArgs := " {\n  \"key\": \"retained original arguments\"\n}\n"
	configPath, argsPath, outDir := sourceCLIInputs(t, f.config, rawArgs)
	var input, stdout bytes.Buffer
	stderr := &sourcePromptAnswer{input: &input}
	if err := runSource(sourceCollectArgs(configPath, argsPath, outDir), &input, &stdout, stderr); err != nil {
		t.Fatal(err)
	}
	var capture sourceCollection
	if json.Unmarshal(stdout.Bytes(), &capture) != nil || capture.State != "captured_locally" || f.calls.Load() != 1 {
		t.Fatal("could not prepare synthetic source capture")
	}
	// Inspection must use only the retained receipt and its captures, not the
	// original configuration/arguments or even the collector's settings file.
	for _, path := range []string{configPath, argsPath} {
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(outDir, "settings.json"), []byte("not valid settings; must not be loaded"), 0o600); err != nil {
		t.Fatal(err)
	}
	return f, capture, rawArgs
}

type sourceSavedFileState struct {
	SHA256   string
	Mode     os.FileMode
	Modified int64
}

func sourceInspectionFiles(t *testing.T, dir string) map[string]sourceSavedFileState {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	files := make(map[string]sourceSavedFileState)
	for _, entry := range entries {
		path := filepath.Join(dir, entry.Name())
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		info, err := os.Lstat(path)
		if err != nil {
			t.Fatal(err)
		}
		files[entry.Name()] = sourceSavedFileState{SHA256: sourceSHA256(body), Mode: info.Mode(), Modified: info.ModTime().UnixNano()}
	}
	return files
}

func TestSourceInspectReadsOnlyRetainedCapture(t *testing.T) {
	f, capture, rawArgs := sourceInspectFixture(t)
	beforeFiles := sourceInspectionFiles(t, filepath.Dir(capture.Receipt.Path))
	beforeRequests, beforeCalls := f.requests.Load(), f.calls.Load()
	rawResult, err := os.ReadFile(capture.RawResult.Path)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name    string
		flags   []string
		content bool
	}{
		{"default", nil, false}, {"bare", []string{"-include-content"}, true},
		{"bare_long", []string{"--include-content"}, true}, {"explicit_true", []string{"-include-content=true"}, true},
		{"explicit_false", []string{"-include-content=false"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := append([]string{"inspect", "-receipt", capture.Receipt.Path}, tc.flags...)
			var stdout, stderr bytes.Buffer
			if err := runSource(args, nil, &stdout, &stderr); err != nil {
				t.Fatal(err)
			}
			var result sourceInspection
			if json.Unmarshal(stdout.Bytes(), &result) != nil || result.SchemaVersion != "detective-source-inspection/v1" || result.State != "matched_saved_receipt" || result.AuthorityEffect != "none" || result.AHESubmitted || result.Revision != "unknown" || result.CapturedAt != capture.CapturedAt || result.Text != capture.Text || result.RawResult != capture.RawResult || result.Receipt != capture.Receipt {
				t.Fatal("inspection did not return the verified saved coordinates and authority boundary")
			}
			if _, err := time.Parse(time.RFC3339Nano, result.VerifiedAt); err != nil {
				t.Fatal("inspection did not identify its offline verification time")
			}
			if !reflect.DeepEqual(result.Config, f.config) || result.Tool.Name != "read_status" || len(result.Tool.ConfigSHA256) != 64 || result.ArgumentsJSON != rawArgs {
				t.Fatal("inspection lost recorded configuration, tool, or exact arguments")
			}
			var fields map[string]json.RawMessage
			if json.Unmarshal(stdout.Bytes(), &fields) != nil {
				t.Fatal("inspection output was not JSON")
			}
			_, hasText := fields["raw_text"]
			_, hasRawResult := fields["raw_result_json"]
			if hasText != tc.content || hasRawResult != tc.content {
				t.Fatal("raw content visibility did not match the explicit flag")
			}
			if tc.content && (result.RawText == nil || *result.RawText != sourceCLIText || result.RawResultJSON == nil || *result.RawResultJSON != string(rawResult)) {
				t.Fatal("included content was not exact saved content")
			}
			if !tc.content && (strings.Contains(stdout.String(), "Exact source text") || result.RawText != nil || result.RawResultJSON != nil) {
				t.Fatal("default inspection exposed raw source content")
			}
			if stderr.Len() != 0 || strings.ContainsAny(stdout.String(), "\u009b\u202e") || f.requests.Load() != beforeRequests || f.calls.Load() != beforeCalls {
				t.Fatal("offline inspection reconnected or exposed terminal controls")
			}
			if after := sourceInspectionFiles(t, filepath.Dir(capture.Receipt.Path)); !reflect.DeepEqual(beforeFiles, after) {
				t.Fatal("inspection modified saved files or created new outputs")
			}
		})
	}
}

func TestSourceInspectRejectsCorruptionWithoutNetworkOrWrites(t *testing.T) {
	for _, mode := range []string{"duplicate_receipt", "unknown_receipt", "trailing_receipt", "missing_receipt", "text_changed", "raw_changed", "cancelled", "timeout", "stdout_error", "stdout_short"} {
		t.Run(mode, func(t *testing.T) {
			f, capture, _ := sourceInspectFixture(t)
			receiptPath := capture.Receipt.Path
			body, err := os.ReadFile(receiptPath)
			if err != nil {
				t.Fatal(err)
			}
			var editedPath string
			var editedBody []byte
			switch mode {
			case "duplicate_receipt":
				editedPath, editedBody = receiptPath, []byte(strings.Replace(string(body), `"schema_version":`, `"schema_version":"duplicate","schema_version":`, 1))
			case "unknown_receipt":
				editedPath, editedBody = receiptPath, []byte(strings.Replace(string(body), `{`, `{"private_unknown":"synthetic-private-diagnostic",`, 1))
			case "trailing_receipt":
				editedPath, editedBody = receiptPath, append(body, []byte(`{}`)...)
			case "missing_receipt":
				receiptPath = filepath.Join(filepath.Dir(receiptPath), "missing-private-receipt")
			case "text_changed":
				editedPath, editedBody = capture.Text.Path, []byte("synthetic-private-diagnostic: changed text")
			case "raw_changed":
				editedPath, editedBody = capture.RawResult.Path, []byte(`{"content":[],"isError":false}`)
			}
			if editedPath != "" {
				if err := os.WriteFile(editedPath, editedBody, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			beforeFiles := sourceInspectionFiles(t, filepath.Dir(capture.Receipt.Path))
			beforeRequests := f.requests.Load()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if mode == "cancelled" {
				cancel()
			}
			args := []string{"inspect", "-receipt", receiptPath}
			if mode == "timeout" {
				args = append(args, "-timeout", "1ns")
			}
			var stdout, stderr bytes.Buffer
			if strings.HasPrefix(mode, "stdout_") {
				err = runSourceContext(ctx, args, nil, sourceFailWriter{short: mode == "stdout_short"}, &stderr)
			} else {
				err = runSourceContext(ctx, args, nil, &stdout, &stderr)
			}
			if err == nil || stdout.Len() != 0 || stderr.Len() != 0 || strings.Contains(err.Error(), "synthetic-private") || f.requests.Load() != beforeRequests || f.calls.Load() != 1 {
				t.Fatalf("failed inspection reported success, leaked input, or connected: %v", err)
			}
			if !reflect.DeepEqual(beforeFiles, sourceInspectionFiles(t, filepath.Dir(capture.Receipt.Path))) {
				t.Fatal("failed inspection changed saved files")
			}
		})
	}
}

func TestSourceInspectClosedFlags(t *testing.T) {
	for _, flags := range [][]string{
		{}, {"-receipt", ""}, {"-receipt", "relative-private-marker"},
		{"-receipt", "/private-receipt", "-receipt", "/second"},
		{"-receipt", "/private-receipt", "-include-content", "-include-content=false"},
		{"-receipt", "/private-receipt", "-include-content", "false"},
		{"-receipt", "/private-receipt", "-include-content=synthetic-private"},
		{"-receipt", "/private-receipt", "-timeout", "121s"},
		{"-receipt", "/private-receipt", "-timeout", "0s"},
		{"-receipt", "/private-receipt", "-config", "/synthetic-private-config"},
		{"-receipt", "/private-receipt", "-tool", "read_status"},
		{"-receipt", "/private-receipt", "-out-dir", "/synthetic-private-output"},
		{"-receipt", "/private-receipt", "-model", "synthetic-private-model"},
		{"-receipt", "/private-receipt", "-yes"},
	} {
		var stdout, stderr bytes.Buffer
		err := runSource(append([]string{"inspect"}, flags...), nil, &stdout, &stderr)
		if err == nil || stdout.Len() != 0 || stderr.Len() != 0 || strings.Contains(err.Error(), "synthetic-private") {
			t.Fatalf("inspect flags were not rejected safely: %v", err)
		}
	}
}
