// ahe-pouch-validate is a local, read-only Core path-certificate validator.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/pouchvalidation"
)

func main() { os.Exit(run()) }
func run() int {
	authorityPath := flag.String("authority", "", "caller-owned finite contract file")
	authorityHash := flag.String("authority-sha256", "", "caller-pinned SHA-256 of exact authority bytes")
	requestID := flag.String("request-id", "", "caller-selected request ID")
	flag.Parse()
	fail := func(err error) int {
		status, code, exit := "INCONCLUSIVE", "INFRASTRUCTURE", 2
		var rejection *pouchvalidation.Rejection
		if errors.As(err, &rejection) {
			status, code, exit = "REJECTED", rejection.Code, 1
		}
		_ = json.NewEncoder(os.Stdout).Encode(map[string]string{"status": status, "reason": code})
		return exit
	}
	if flag.NArg() != 0 || *authorityPath == "" || *authorityHash == "" || *requestID == "" {
		fmt.Fprintln(os.Stderr, "authority, authority-sha256 and request-id are required")
		return 2
	}
	readBounded := func(r io.Reader) ([]byte, error) {
		b, e := io.ReadAll(io.LimitReader(r, pouchvalidation.MaxBytes+1))
		if e == nil && len(b) > pouchvalidation.MaxBytes {
			e = errors.New("input exceeds bound")
		}
		return b, e
	}
	f, err := os.Open(*authorityPath)
	if err != nil {
		return fail(err)
	}
	raw, err := readBounded(f)
	closeErr := f.Close()
	if err != nil {
		return fail(err)
	}
	if closeErr != nil {
		return fail(closeErr)
	}
	authority, err := pouchvalidation.NewAuthority(raw, *authorityHash)
	if err != nil {
		return fail(err)
	}
	pkg, err := readBounded(os.Stdin)
	if err != nil {
		return fail(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	receipt, err := authority.Validate(ctx, *requestID, pkg)
	if err != nil {
		return fail(err)
	}
	if err := json.NewEncoder(os.Stdout).Encode(struct {
		Status  string                  `json:"status"`
		Receipt pouchvalidation.Receipt `json:"receipt"`
	}{"ACCEPTED", receipt}); err != nil {
		return 2
	}
	return 0
}
