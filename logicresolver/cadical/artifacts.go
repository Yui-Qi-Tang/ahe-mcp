package cadical

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/Yui-Qi-Tang/ahe-mcp/logicresolver"
)

// Identity binds tool binaries and effective limits, excluding storage paths.
// Consumers can use it to invalidate results when execution settings change.
func (r *Runner) Identity() string {
	b, _ := json.Marshal(struct {
		Solver, Checker string
		Limits          logicresolver.Limits
		ProofLimit      int64
	}{r.config.SolverSHA256, r.config.CheckerSHA256, r.config.Limits, r.config.MaxProofBytes})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// ReadArtifact reads a retained artifact through the runner's root and verifies
// its recorded size and digest. It supports durable consumer-owned archival.
func (r *Runner) ReadArtifact(a logicresolver.Artifact) ([]byte, error) {
	limit := max(r.config.MaxProofBytes, int64(r.config.Limits.MaxInputBytes), int64(r.config.Limits.MaxOutputBytes))
	if a.Bytes < 0 || a.Bytes > limit {
		return nil, logicresolver.ErrLimit
	}
	f, err := os.OpenInRoot(r.root, a.ID)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() != a.Bytes {
		return nil, fmt.Errorf("%w: artifact size", logicresolver.ErrValidation)
	}
	b, err := io.ReadAll(io.LimitReader(f, a.Bytes+1))
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(b)
	if int64(len(b)) != a.Bytes || hex.EncodeToString(sum[:]) != a.SHA256 {
		return nil, fmt.Errorf("%w: artifact digest", logicresolver.ErrValidation)
	}
	return b, nil
}
