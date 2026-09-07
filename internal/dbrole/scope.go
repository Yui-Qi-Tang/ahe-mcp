package dbrole

import (
	"context"
	"fmt"
	"sort"
)

// loadUnmanifestedAuthorities inventories effective authority that cannot be
// represented by the selected AHE-schema table manifest. PostgreSQL system
// schemas are excluded because every ordinary session needs their built-in
// catalog and function surfaces. All user-created schemas remain in scope.
func loadUnmanifestedAuthorities(
	ctx context.Context,
	db policyDB,
	schema string,
	role string,
	serverVersionNum int,
) ([]string, error) {
	rows, err := db.Query(ctx, `
		WITH non_system_schemas AS (
			SELECT oid, nspname
			FROM pg_catalog.pg_namespace
			WHERE nspname <> 'information_schema'
			  AND nspname !~ '^pg_'
		),
		table_privileges(name) AS (
			SELECT pg_catalog.unnest($3::TEXT[])
		),
		column_privileges(name) AS (
			VALUES ('SELECT'::TEXT), ('INSERT'), ('UPDATE'), ('REFERENCES')
		),
		sequence_privileges(name) AS (
			VALUES ('SELECT'::TEXT), ('USAGE'), ('UPDATE')
		),
		large_object_privileges(name) AS (
			VALUES ('SELECT'::TEXT), ('UPDATE')
		)
		SELECT authority
		FROM (
			SELECT 'SCHEMA ' || namespace.nspname || ' ' || privilege.name AS authority
			FROM non_system_schemas AS namespace
			CROSS JOIN (VALUES ('USAGE'::TEXT), ('CREATE')) AS privilege(name)
			WHERE namespace.nspname <> $2
			  AND pg_catalog.has_schema_privilege($1, namespace.oid, privilege.name)

			UNION ALL

			SELECT 'TABLE ' || namespace.nspname || '.' || relation.relname || ' ' || privilege.name
			FROM pg_catalog.pg_class AS relation
			JOIN non_system_schemas AS namespace ON namespace.oid = relation.relnamespace
			CROSS JOIN table_privileges AS privilege
			WHERE namespace.nspname <> $2
			  AND relation.relkind IN ('r', 'p', 'v', 'm', 'f')
			  AND pg_catalog.has_table_privilege($1, relation.oid, privilege.name)

			UNION ALL

			SELECT 'COLUMN ' || namespace.nspname || '.' || relation.relname || '.' ||
			       attribute.attname || ' ' || privilege.name
			FROM pg_catalog.pg_attribute AS attribute
			JOIN pg_catalog.pg_class AS relation ON relation.oid = attribute.attrelid
			JOIN non_system_schemas AS namespace ON namespace.oid = relation.relnamespace
			CROSS JOIN column_privileges AS privilege
			WHERE namespace.nspname <> $2
			  AND relation.relkind IN ('r', 'p', 'v', 'm', 'f')
			  AND attribute.attnum > 0
			  AND NOT attribute.attisdropped
			  AND pg_catalog.has_column_privilege(
				$1,
				relation.oid,
				attribute.attnum,
				privilege.name
			  )

			UNION ALL

			SELECT 'SEQUENCE ' || namespace.nspname || '.' || relation.relname || ' ' || privilege.name
			FROM pg_catalog.pg_class AS relation
			JOIN non_system_schemas AS namespace ON namespace.oid = relation.relnamespace
			CROSS JOIN sequence_privileges AS privilege
			WHERE relation.relkind = 'S'
			  AND pg_catalog.has_sequence_privilege($1, relation.oid, privilege.name)

			UNION ALL

			SELECT 'FUNCTION ' || namespace.nspname || '.' || function_row.proname ||
			       '(' || pg_catalog.oidvectortypes(function_row.proargtypes) || ') EXECUTE'
			FROM pg_catalog.pg_proc AS function_row
			JOIN non_system_schemas AS namespace ON namespace.oid = function_row.pronamespace
			WHERE namespace.nspname <> $2
			  AND pg_catalog.has_function_privilege($1, function_row.oid, 'EXECUTE')

			UNION ALL

			SELECT 'LARGE OBJECT ' || large_object.oid::TEXT || ' ' || privilege.name
			FROM pg_catalog.pg_largeobject_metadata AS large_object
			CROSS JOIN large_object_privileges AS privilege
			WHERE pg_catalog.has_largeobject_privilege(
				$1,
				large_object.oid,
				privilege.name
			)
		) AS unmanifested
		ORDER BY authority
	`, role, schema, supportedTablePrivileges(serverVersionNum))
	if err != nil {
		return nil, fmt.Errorf("loading unmanifested authority for %q: %w", role, err)
	}
	defer rows.Close()

	var result []string
	for rows.Next() {
		var authority string
		if err := rows.Scan(&authority); err != nil {
			return nil, fmt.Errorf("scanning unmanifested authority for %q: %w", role, err)
		}
		result = append(result, authority)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating unmanifested authority for %q: %w", role, err)
	}
	systemAuthority, err := loadSystemAndConfigurationAuthorities(
		ctx,
		db,
		role,
		serverVersionNum,
	)
	if err != nil {
		return nil, err
	}
	result = append(result, systemAuthority...)
	sort.Strings(result)
	return result, nil
}
