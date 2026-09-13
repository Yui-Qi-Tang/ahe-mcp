package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/sourcepilot"
)

func runBriefExcerpt(ctx context.Context, args []string, stdout io.Writer) error {
	selecting := args[0] == "select"
	flags := flag.NewFlagSet("detective brief "+args[0], flag.ContinueOnError)
	input := flags.String("input", "", "explicit private input JSON")
	var parentPath, out, reason string
	start, end := -1, -1
	if selecting {
		flags.StringVar(&out, "out", "", "new private excerpt file")
		flags.StringVar(&reason, "reason", "", "explicit reason for selecting this range")
		flags.IntVar(&start, "start-byte", -1, "inclusive decoded UTF-8 byte offset")
		flags.IntVar(&end, "end-byte", -1, "exclusive decoded UTF-8 byte offset")
	} else {
		flags.StringVar(&parentPath, "parent", "", "explicit private full_document parent JSON")
	}
	if parseReviewFlags(flags, args[1:]) != nil || ctx == nil || ctx.Err() != nil || *input == "" || (selecting && (out == "" || reason == "" || start < 0 || end <= start)) || (!selecting && parentPath == "") {
		return errors.New("brief excerpt requires all explicit supported flags without duplicates and an active context")
	}
	if selecting {
		parentPath = *input
	}
	raw, err := readSourcePrivateLimit(parentPath, sourcepilot.BriefParentJSONLimit)
	if err != nil {
		var limit *sourcepilot.InputLimitError
		if errors.As(err, &limit) {
			limit.Resource = "parent_json_bytes"
		}
		return briefInputError(err, "brief parent requires a private regular file at most 1 MiB")
	}
	parent, err := sourcepilot.ParseBriefSelectionSource(raw)
	if err != nil {
		return briefInputError(err, "brief parent requires a valid explicit v2 news or public_event full_document source")
	}
	if !selecting {
		raw, err := readSourcePrivate(*input)
		if err != nil {
			return briefInputError(err, "brief excerpt requires a private regular source file at most 64 KiB")
		}
		source, err := sourcepilot.ParseBriefSource(raw)
		if err != nil {
			return briefInputError(err, "brief excerpt source is invalid")
		}
		if err := sourcepilot.VerifyBriefExcerpt(parent, source); err != nil {
			return briefInputError(err, "brief excerpt does not match the supplied parent identity, digest, byte range and exact text")
		}
		return writeNewsText(stdout, "本次離線比對通過：父來源座標、全文 SHA-256、長度、UTF-8 byte 範圍與摘錄原文一致；只證明所提供兩份資料的對應，不驗證發布者、逐句支持或採納。未改寫檔案，也未保存永久 verified 狀態。\n")
	}
	source, err := sourcepilot.SelectBriefExcerpt(parent, start, end, reason)
	if err != nil {
		return briefInputError(err, "brief selection requires a valid nonempty UTF-8 byte range and bounded explicit reason")
	}
	encoded, err := json.MarshalIndent(source, "", "  ")
	if err != nil {
		return errors.New("brief selected source could not be encoded")
	}
	if len(encoded)+1 > sourceInputLimit {
		return briefInputError(&sourcepilot.InputLimitError{Resource: "source_json_bytes", Limit: sourceInputLimit, Observed: int64(len(encoded) + 1)}, "")
	}
	output, err := reserveNewsOutput(out)
	if err != nil {
		return errors.New("brief excerpt output requires a new file in an existing private directory")
	}
	defer output.close()
	if err := output.write(source); err != nil {
		return errors.New("brief excerpt publication did not complete; retain output for inspection, do not overwrite")
	}
	if err := writeNewsText(stdout, fmt.Sprintf("已保存精確摘錄：[%d, %d) UTF-8 bytes；父本文 %d bytes，SHA-256 %s。未呼叫模型或 MCP；保存座標供後續離線重驗，不代表發布者驗證或採納。\n", start, end, source.Excerpt.ParentBodyBytes, source.Excerpt.ParentBodySHA256)); err != nil {
		return errors.New("brief excerpt is saved but terminal output failed; do not repeat selection into the same output")
	}
	return nil
}
