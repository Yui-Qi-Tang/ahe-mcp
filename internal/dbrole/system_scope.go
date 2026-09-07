package dbrole

import (
	"context"
	"fmt"
)

// loadSystemAndConfigurationAuthorities rejects explicit authority that is
// not part of PostgreSQL's ordinary PUBLIC catalog surface. Effective system
// relation writes are also rejected, except the built-in pg_settings UPDATE
// interface, which enforces the same GUC permissions as SET. Explicit grants
// on that view are still rejected by the direct-ACL branches above.
func loadSystemAndConfigurationAuthorities(
	ctx context.Context,
	db policyDB,
	role string,
	serverVersionNum int,
) ([]string, error) {
	rows, err := db.Query(ctx, `
		WITH target_role AS (
			SELECT oid FROM pg_catalog.pg_roles WHERE rolname = $1
		),
		system_schemas AS (
			SELECT oid, nspname
			FROM pg_catalog.pg_namespace
			WHERE nspname = 'information_schema' OR nspname ~ '^pg_'
		),
		table_write_privileges(name) AS (
			SELECT privilege
			FROM pg_catalog.unnest($2::TEXT[]) AS privilege
			WHERE privilege <> 'SELECT'
		),
		column_write_privileges(name) AS (
			VALUES ('INSERT'::TEXT), ('UPDATE'), ('REFERENCES')
		),
		sequence_privileges(name) AS (
			VALUES ('SELECT'::TEXT), ('USAGE'), ('UPDATE')
		)
		SELECT authority
		FROM (
			SELECT 'SYSTEM SCHEMA ' || namespace.nspname || ' ' || acl.privilege_type AS authority
			FROM pg_catalog.pg_namespace AS namespace
			JOIN system_schemas ON system_schemas.oid = namespace.oid
			CROSS JOIN LATERAL pg_catalog.aclexplode(namespace.nspacl) AS acl
			JOIN target_role ON target_role.oid = acl.grantee

			UNION ALL

			SELECT 'SYSTEM RELATION ' || namespace.nspname || '.' || relation.relname ||
			       ' ' || acl.privilege_type
			FROM pg_catalog.pg_class AS relation
			JOIN system_schemas AS namespace ON namespace.oid = relation.relnamespace
			CROSS JOIN LATERAL pg_catalog.aclexplode(relation.relacl) AS acl
			JOIN target_role ON target_role.oid = acl.grantee

			UNION ALL

			SELECT 'SYSTEM COLUMN ' || namespace.nspname || '.' || relation.relname || '.' ||
			       attribute.attname || ' ' || acl.privilege_type
			FROM pg_catalog.pg_attribute AS attribute
			JOIN pg_catalog.pg_class AS relation ON relation.oid = attribute.attrelid
			JOIN system_schemas AS namespace ON namespace.oid = relation.relnamespace
			CROSS JOIN LATERAL pg_catalog.aclexplode(attribute.attacl) AS acl
			JOIN target_role ON target_role.oid = acl.grantee

			UNION ALL

			SELECT 'SYSTEM FUNCTION ' || namespace.nspname || '.' || function_row.proname ||
			       '(' || pg_catalog.oidvectortypes(function_row.proargtypes) || ') ' ||
			       acl.privilege_type
			FROM pg_catalog.pg_proc AS function_row
			JOIN system_schemas AS namespace ON namespace.oid = function_row.pronamespace
			CROSS JOIN LATERAL pg_catalog.aclexplode(function_row.proacl) AS acl
			JOIN target_role ON target_role.oid = acl.grantee

			UNION ALL

			SELECT 'SYSTEM TYPE ' || namespace.nspname || '.' || type_row.typname ||
			       ' ' || acl.privilege_type
			FROM pg_catalog.pg_type AS type_row
			JOIN system_schemas AS namespace ON namespace.oid = type_row.typnamespace
			CROSS JOIN LATERAL pg_catalog.aclexplode(type_row.typacl) AS acl
			JOIN target_role ON target_role.oid = acl.grantee

			UNION ALL

			SELECT 'SYSTEM RELATION ' || namespace.nspname || '.' || relation.relname ||
			       ' ' || privilege.name
			FROM pg_catalog.pg_class AS relation
			JOIN system_schemas AS namespace ON namespace.oid = relation.relnamespace
			CROSS JOIN table_write_privileges AS privilege
			WHERE relation.relkind IN ('r', 'p', 'v', 'm', 'f')
			  AND pg_catalog.has_table_privilege($1, relation.oid, privilege.name)
			  AND NOT (namespace.nspname = 'pg_catalog' AND relation.relname = 'pg_settings'
			           AND privilege.name = 'UPDATE')

			UNION ALL

			SELECT 'SYSTEM COLUMN ' || namespace.nspname || '.' || relation.relname || '.' ||
			       attribute.attname || ' ' || privilege.name
			FROM pg_catalog.pg_attribute AS attribute
			JOIN pg_catalog.pg_class AS relation ON relation.oid = attribute.attrelid
			JOIN system_schemas AS namespace ON namespace.oid = relation.relnamespace
			CROSS JOIN column_write_privileges AS privilege
			WHERE relation.relkind IN ('r', 'p', 'v', 'm', 'f')
			  AND attribute.attnum > 0
			  AND NOT attribute.attisdropped
			  AND pg_catalog.has_column_privilege(
				$1,
				relation.oid,
				attribute.attnum,
				privilege.name
			  )
			  AND NOT (namespace.nspname = 'pg_catalog' AND relation.relname = 'pg_settings'
			           AND privilege.name = 'UPDATE')

			UNION ALL

			SELECT 'SYSTEM SEQUENCE ' || namespace.nspname || '.' || relation.relname ||
			       ' ' || privilege.name
			FROM pg_catalog.pg_class AS relation
			JOIN system_schemas AS namespace ON namespace.oid = relation.relnamespace
			CROSS JOIN sequence_privileges AS privilege
			WHERE relation.relkind = 'S'
			  AND pg_catalog.has_sequence_privilege($1, relation.oid, privilege.name)

			UNION ALL

			SELECT 'LANGUAGE ' || language.lanname || ' ' || acl.privilege_type
			FROM pg_catalog.pg_language AS language
			CROSS JOIN LATERAL pg_catalog.aclexplode(language.lanacl) AS acl
			JOIN target_role ON target_role.oid = acl.grantee

			UNION ALL

			SELECT 'TABLESPACE ' || tablespace.spcname || ' ' || acl.privilege_type
			FROM pg_catalog.pg_tablespace AS tablespace
			CROSS JOIN LATERAL pg_catalog.aclexplode(tablespace.spcacl) AS acl
			JOIN target_role ON target_role.oid = acl.grantee

			UNION ALL

			SELECT 'FOREIGN DATA WRAPPER ' || wrapper.fdwname || ' ' || acl.privilege_type
			FROM pg_catalog.pg_foreign_data_wrapper AS wrapper
			CROSS JOIN LATERAL pg_catalog.aclexplode(wrapper.fdwacl) AS acl
			JOIN target_role ON target_role.oid = acl.grantee

			UNION ALL

			SELECT 'FOREIGN SERVER ' || server.srvname || ' ' || acl.privilege_type
			FROM pg_catalog.pg_foreign_server AS server
			CROSS JOIN LATERAL pg_catalog.aclexplode(server.srvacl) AS acl
			JOIN target_role ON target_role.oid = acl.grantee

			UNION ALL

			SELECT 'PARAMETER ' || parameter.parname || ' ' || acl.privilege_type
			FROM pg_catalog.pg_parameter_acl AS parameter
			CROSS JOIN LATERAL pg_catalog.aclexplode(parameter.paracl) AS acl
			LEFT JOIN target_role ON target_role.oid = acl.grantee
			WHERE acl.grantee = 0 OR target_role.oid IS NOT NULL

			UNION ALL

			SELECT 'DEFAULT ACL ' || default_acl.defaclobjtype::TEXT || ' ' || acl.privilege_type
			FROM pg_catalog.pg_default_acl AS default_acl
			CROSS JOIN LATERAL pg_catalog.aclexplode(default_acl.defaclacl) AS acl
			LEFT JOIN target_role ON target_role.oid = acl.grantee
			WHERE acl.grantee = 0 OR target_role.oid IS NOT NULL
		) AS forbidden
		ORDER BY authority
	`, role, supportedTablePrivileges(serverVersionNum))
	if err != nil {
		return nil, fmt.Errorf("loading system/configuration authority for %q: %w", role, err)
	}
	defer rows.Close()

	var result []string
	for rows.Next() {
		var authority string
		if err := rows.Scan(&authority); err != nil {
			return nil, fmt.Errorf("scanning system/configuration authority for %q: %w", role, err)
		}
		result = append(result, authority)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating system/configuration authority for %q: %w", role, err)
	}
	return result, nil
}
