package dbrole

import (
	"context"
	"fmt"
)

func loadRoleConfiguration(
	ctx context.Context,
	db policyDB,
	database string,
	role string,
) ([]string, error) {
	rows, err := db.Query(ctx, `
		SELECT authority
		FROM (
			SELECT 'ROLE ' || setting AS authority
			FROM pg_catalog.pg_roles AS role_row
			CROSS JOIN LATERAL pg_catalog.unnest(
				COALESCE(role_row.rolconfig, ARRAY[]::TEXT[])
			) AS setting
			WHERE role_row.rolname = $2

			UNION ALL

			SELECT 'ROLE_DATABASE ' || COALESCE(database_row.datname, 'ALL') || ' ' || setting
			FROM pg_catalog.pg_db_role_setting AS role_setting
			JOIN pg_catalog.pg_roles AS role_row ON role_row.oid = role_setting.setrole
			LEFT JOIN pg_catalog.pg_database AS database_row
			  ON database_row.oid = role_setting.setdatabase
			CROSS JOIN LATERAL pg_catalog.unnest(role_setting.setconfig) AS setting
			WHERE role_row.rolname = $2
			  AND (role_setting.setdatabase = 0 OR database_row.datname = $1)
		) AS configured
		ORDER BY authority
	`, database, role)
	if err != nil {
		return nil, fmt.Errorf("loading role configuration for %q: %w", role, err)
	}
	defer rows.Close()

	var result []string
	for rows.Next() {
		var setting string
		if err := rows.Scan(&setting); err != nil {
			return nil, fmt.Errorf("scanning role configuration for %q: %w", role, err)
		}
		result = append(result, setting)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating role configuration for %q: %w", role, err)
	}
	return result, nil
}

func validateCriticalRuntimeSettings(environment connectionEnvironment) error {
	if environment.sessionReplicationRole != "origin" {
		return policyViolation(
			"session_replication_role = %q, want origin",
			environment.sessionReplicationRole,
		)
	}
	if environment.largeObjectCompatibility != "off" {
		return policyViolation(
			"lo_compat_privileges = %q, want off",
			environment.largeObjectCompatibility,
		)
	}
	return nil
}
