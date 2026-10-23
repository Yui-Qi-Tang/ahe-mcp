package evidenceingestion

import (
	"context"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceuse"
)

// endpointUseCut is private to one endpoint review/admission transaction. The
// committing path creates it after taking the proposal and governance locks.
// Review and commit assess at their own current time, sharing only the verified
// snapshot. Never retain this value across transactions or serialization retries.
// Until the final assessment, the transaction may reserve a request ID but must
// not write any material used by the snapshot. Canonical writes follow the gate.
type endpointUseCut struct {
	tx    sqlTx
	state *evidenceuse.Snapshot
}

func (cut *endpointUseCut) snapshot(ctx context.Context) (evidenceuse.Snapshot, error) {
	if cut.state == nil {
		s, err := loadEvidenceUseSnapshot(ctx, cut.tx)
		if err != nil {
			return evidenceuse.Snapshot{}, err
		}
		cut.state = &s
	}
	return *cut.state, nil
}
