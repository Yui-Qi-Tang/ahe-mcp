package evidenceingestion

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencegraph"
)

// The row boundary isolates the two production preflights from later native
// materialization. Complete native source/endpoint roundtrips run separately.
func TestImplementsSourceRefBound(t *testing.T) {
	for _, refs := range []int{64, 65, 128, 129} {
		for _, recursive := range []bool{false, true} {
			t.Run(fmt.Sprintf("refs_%d/recursive_%t", refs, recursive), func(t *testing.T) {
				tx := &sourceRefBoundTx{refs: refs, size: 1024}
				err := runSourceRefBound(context.Background(), tx, recursive)
				if refs > 128 {
					assertKind(t, err, ErrorInvalidInput)
					if tx.calls != 1 {
						t.Fatalf("oversized refs reached materialization: %d queries", tx.calls)
					}
					return
				}
				if recursive {
					if err != nil || tx.calls != 1 {
						t.Fatalf("bounded source header: error=%v, queries=%d", err, tx.calls)
					}
				} else if !errors.Is(err, errSourceRefMaterialization) || tx.calls != 2 {
					t.Fatalf("bounded source did not reach materialization: error=%v, queries=%d", err, tx.calls)
				}
			})
		}
	}
}

func TestImplementsOtherReviewBoundsRemain(t *testing.T) {
	for _, tc := range []struct {
		name                    string
		size, parents, incoming int
	}{
		{name: "record_bytes", size: (128 << 10) + 1},
		{name: "parent_count", size: 1024, parents: 9, incoming: 9},
		{name: "unregistered_edge", size: 1024, incoming: 1},
	} {
		for _, recursive := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/recursive_%t", tc.name, recursive), func(t *testing.T) {
				tx := &sourceRefBoundTx{refs: 128, size: tc.size, parents: tc.parents, incoming: tc.incoming}
				assertKind(t, runSourceRefBound(context.Background(), tx, recursive), ErrorInvalidInput)
				if tx.calls != 1 {
					t.Fatalf("rejected header reached materialization: %d queries", tx.calls)
				}
			})
		}
	}
}

func runSourceRefBound(ctx context.Context, tx *sourceRefBoundTx, recursive bool) error {
	if recursive {
		_, err := loadRecursiveImplementsHeader(ctx, tx, "canon-node:source-ref-bound")
		return err
	}
	_, _, err := loadDerivedImplementsAdmittedNode(ctx, tx, "canon-node:source-ref-bound", evidencegraph.CanonicalSourceClaim)
	return err
}

var errSourceRefMaterialization = errors.New("source-ref test reached later materialization")

type sourceRefBoundTx struct {
	sqlTx
	refs, size, parents, incoming, calls int
}

func (tx *sourceRefBoundTx) queryRow(context.Context, string, ...any) sqlRow {
	tx.calls++
	return sourceRefBoundRow{tx: tx, first: tx.calls == 1}
}

type sourceRefBoundRow struct {
	tx    *sourceRefBoundTx
	first bool
}

func (r sourceRefBoundRow) Scan(dest ...any) error {
	if !r.first {
		return errSourceRefMaterialization
	}
	if len(dest) == 6 {
		*dest[0].(*string) = string(evidencegraph.CanonicalSourceClaim)
		dest = dest[1:]
	}
	*dest[0].(*int) = r.tx.size
	*dest[1].(*int) = r.tx.refs
	*dest[2].(*int) = r.tx.parents
	*dest[3].(*int) = r.tx.incoming
	if len(dest) == 5 {
		*dest[4].(*int) = 0
	}
	return nil
}
