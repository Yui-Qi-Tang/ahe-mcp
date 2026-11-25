package evidenceingestion

import (
	"context"
	"fmt"
	"slices"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceuse"
)

// endpointUseCut is private to one endpoint review/admission transaction. The
// committing path creates it after taking the proposal and governance locks.
// Review and commit assess at their own current time, sharing only the verified
// snapshot of one scope: the reviewed derivation parents. Never retain this
// value across transactions or serialization retries.
// Until the final assessment, the transaction may reserve a request ID but must
// not write any material used by the snapshot. Canonical writes follow the gate.
type endpointUseCut struct {
	tx    sqlTx
	roots []string
	state *evidenceuse.Snapshot
}

// snapshot loads the scope for roots once. A later request for a different
// root set fails, because review and commit must assess the same material.
func (cut *endpointUseCut) snapshot(ctx context.Context, roots []string) (evidenceuse.Snapshot, error) {
	roots = slices.Compact(slices.Sorted(slices.Values(roots)))
	if cut.state == nil {
		s, err := loadEvidenceUseSnapshot(ctx, cut.tx, roots)
		if err != nil {
			return evidenceuse.Snapshot{}, err
		}
		cut.roots, cut.state = roots, &s
	} else if !slices.Equal(cut.roots, roots) {
		return evidenceuse.Snapshot{}, fmt.Errorf("endpoint use scope differs from the reviewed derivation parents")
	}
	return *cut.state, nil
}
