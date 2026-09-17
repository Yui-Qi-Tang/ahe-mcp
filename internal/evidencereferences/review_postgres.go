package evidencereferences

import (
	"context"
	"fmt"
	"time"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestion"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// LoadReview observes immutable evidence and the entire target cut in one RR tx.
func LoadReview(ctx context.Context, pool *pgxpool.Pool, req ReviewRequest) (Review, error) {
	if pool == nil {
		return Review{}, fmt.Errorf("%w: postgres pool is required", ErrUnresolved)
	}
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return Review{}, fmt.Errorf("beginning references review: %w", err)
	}
	defer rollback(ctx, tx)
	if _, err = tx.Exec(ctx, `SET LOCAL row_security=off`); err != nil {
		return Review{}, fmt.Errorf("setting references review policy: %w", err)
	}
	basis, err := evidenceingestion.LoadReferencesNativeBasisInTx(ctx, tx, req.FromNodeID, req.ToNodeID)
	if err != nil {
		return Review{}, err
	}
	review, err := buildReview(req, basis)
	if err != nil {
		return Review{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return Review{}, fmt.Errorf("finishing references review: %w", err)
	}
	return review, nil
}

func rollback(ctx context.Context, tx pgx.Tx) {
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	_ = tx.Rollback(cleanup)
}
