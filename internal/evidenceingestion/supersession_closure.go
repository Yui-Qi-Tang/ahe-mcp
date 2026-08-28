package evidenceingestion

import (
	"context"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencesupersession"

	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	canonicalSupersessionSnapshotLimitation                 = "currentness is limited to this exact admitted PostgreSQL snapshot; it does not prove provider freshness or global truth"
	canonicalSupersessionUnclassifiedObjectClaimsLimitation = "one or more admitted external source_claim records for this source object have no reviewed lineage assignment"
)

// CanonicalSupersessionCurrentness is a read-consistent derived projection.
// Limitations records the admitted-snapshot boundary and any additional reason
// the valid topology cannot prove currentness. Projection and witness are not
// stored.
type CanonicalSupersessionCurrentness struct {
	Projection  evidencesupersession.CurrentnessProjection `json:"projection"`
	Limitations []string                                   `json:"limitations,omitempty"`
}

// GetCanonicalSupersessionCurrentness derives one lineage projection from an
// unfiltered, repeatable-read PostgreSQL snapshot. It accepts no caller claim
// of completeness, member set, winner, status, or hash.
func GetCanonicalSupersessionCurrentness(
	ctx context.Context,
	pool *pgxpool.Pool,
	lineageKey string,
) (CanonicalSupersessionCurrentness, error) {
	if pool == nil {
		return CanonicalSupersessionCurrentness{}, newDomainError(ErrorInvalidInput, "postgres pool is required")
	}
	return getCanonicalSupersessionCurrentness(ctx, pgxDB{pool: pool}, lineageKey)
}

func getCanonicalSupersessionCurrentness(
	ctx context.Context,
	db sqlDB,
	lineageKey string,
) (CanonicalSupersessionCurrentness, error) {
	if !validCanonicalSupersessionLineageKey(lineageKey) {
		return CanonicalSupersessionCurrentness{}, newDomainError(
			ErrorInvalidRecordID,
			"lineage_key %q must be a canonical lineage:v1 SHA-256 ID",
			lineageKey,
		)
	}

	var result CanonicalSupersessionCurrentness
	err := withReadOnlyTx(ctx, db, func(tx sqlTx) error {
		// row_security=off does not bypass PostgreSQL RLS for an unprivileged
		// role. It makes a policy-filtered read fail instead of silently
		// returning a partial authority cut.
		if _, err := tx.exec(ctx, `SET LOCAL row_security = off`); err != nil {
			return fmt.Errorf("requiring an unfiltered supersession closure read: %w", err)
		}
		input, err := loadCanonicalSupersessionClosureInput(ctx, tx, lineageKey)
		if err != nil {
			return err
		}
		projection, err := evidencesupersession.ProjectCurrentness(input)
		if err != nil {
			return canonicalSupersessionClosureInvariantError(err)
		}
		result.Projection = projection
		result.Limitations = []string{canonicalSupersessionSnapshotLimitation}
		if !projection.ClosureAvailable {
			result.Limitations = append(
				result.Limitations,
				canonicalSupersessionUnclassifiedObjectClaimsLimitation,
			)
		}
		return nil
	})
	if err != nil {
		return CanonicalSupersessionCurrentness{}, err
	}
	return result, nil
}

func validCanonicalSupersessionLineageKey(value string) bool {
	const prefix = "lineage:v1:sha256:"
	if !strings.HasPrefix(value, prefix) || len(value) != len(prefix)+64 || value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(value, prefix))
	return err == nil
}

func canonicalSupersessionClosureInvariantError(err error) error {
	if _, exists := KindOf(err); exists {
		return err
	}
	return newDomainError(ErrorSupersessionInvariant, "%v", err)
}
