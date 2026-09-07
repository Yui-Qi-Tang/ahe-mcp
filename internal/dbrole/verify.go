package dbrole

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"

	"github.com/jackc/pgx/v5"
)

const postgresPrivilegeMaintain = "MAINTAIN"

type roleAttributes struct {
	name        string
	canLogin    bool
	inherit     bool
	superuser   bool
	bypassRLS   bool
	createDB    bool
	createRole  bool
	replication bool
}

type roleMembership struct {
	role          string
	adminOption   bool
	inheritOption bool
	setOption     bool
}

type ownershipState struct {
	database     int
	schema       int
	relations    int
	functions    int
	types        int
	largeObjects int
}

func (s ownershipState) any() bool {
	return s.database != 0 || s.schema != 0 || s.relations != 0 || s.functions != 0 ||
		s.types != 0 || s.largeObjects != 0
}

type columnPrivilege struct {
	table     string
	column    string
	privilege string
}

type principalSnapshot struct {
	role              roleAttributes
	memberships       []roleMembership
	adminMembers      []string
	ownership         ownershipState
	databaseConnect   bool
	databaseCreate    bool
	databaseTemporary bool
	schemaUsage       bool
	schemaCreate      bool
	tablePrivileges   map[string]map[string]bool
	columnPrivileges  []columnPrivilege
	functionExecute   []string
	grantOptions      []string
	unmanifested      []string
	configuration     []string
}

type principalExpectation struct {
	name               string
	login              bool
	exactPrivileges    bool
	requireSchemaUse   bool
	allowedMembership  string
	rejectAdminMembers bool
}

func supportedTablePrivileges(serverVersionNum int) []string {
	privileges := make([]string, 0, len(tablePrivileges)+1)
	for _, privilege := range allTablePrivileges() {
		if string(privilege) == postgresPrivilegeMaintain && serverVersionNum < 170000 {
			continue
		}
		privileges = append(privileges, string(privilege))
	}
	if serverVersionNum >= 170000 && !slices.Contains(privileges, postgresPrivilegeMaintain) {
		privileges = append(privileges, postgresPrivilegeMaintain)
	}
	return privileges
}

func loadRoleAttributes(ctx context.Context, db policyDB, role string) (roleAttributes, error) {
	var result roleAttributes
	err := db.QueryRow(ctx, `
		SELECT
			rolname,
			rolcanlogin,
			rolinherit,
			rolsuper,
			rolbypassrls,
			rolcreatedb,
			rolcreaterole,
			rolreplication
		FROM pg_catalog.pg_roles
		WHERE rolname = $1
	`, role).Scan(
		&result.name,
		&result.canLogin,
		&result.inherit,
		&result.superuser,
		&result.bypassRLS,
		&result.createDB,
		&result.createRole,
		&result.replication,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return roleAttributes{}, policyViolation("PostgreSQL role %q does not exist", role)
	}
	if err != nil {
		return roleAttributes{}, fmt.Errorf("loading PostgreSQL role %q: %w", role, err)
	}
	return result, nil
}

func loadRoleMemberships(ctx context.Context, db policyDB, member string) ([]roleMembership, error) {
	rows, err := db.Query(ctx, `
		SELECT
			parent.rolname,
			membership.admin_option,
			membership.inherit_option,
			membership.set_option
		FROM pg_catalog.pg_auth_members AS membership
		JOIN pg_catalog.pg_roles AS parent
		  ON parent.oid = membership.roleid
		JOIN pg_catalog.pg_roles AS child
		  ON child.oid = membership.member
		WHERE child.rolname = $1
		ORDER BY parent.rolname
	`, member)
	if err != nil {
		return nil, fmt.Errorf("loading PostgreSQL memberships for %q: %w", member, err)
	}
	defer rows.Close()
	var result []roleMembership
	for rows.Next() {
		var membership roleMembership
		if err := rows.Scan(
			&membership.role,
			&membership.adminOption,
			&membership.inheritOption,
			&membership.setOption,
		); err != nil {
			return nil, fmt.Errorf("scanning PostgreSQL membership for %q: %w", member, err)
		}
		result = append(result, membership)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating PostgreSQL memberships for %q: %w", member, err)
	}
	return result, nil
}

func loadRoleAdminMembers(ctx context.Context, db policyDB, role string) ([]string, error) {
	rows, err := db.Query(ctx, `
		SELECT member_role.rolname
		FROM pg_catalog.pg_auth_members AS membership
		JOIN pg_catalog.pg_roles AS parent_role
		  ON parent_role.oid = membership.roleid
		JOIN pg_catalog.pg_roles AS member_role
		  ON member_role.oid = membership.member
		WHERE parent_role.rolname = $1
		  AND membership.admin_option
		ORDER BY member_role.rolname
	`, role)
	if err != nil {
		return nil, fmt.Errorf("loading PostgreSQL admin members for %q: %w", role, err)
	}
	defer rows.Close()
	var result []string
	for rows.Next() {
		var member string
		if err := rows.Scan(&member); err != nil {
			return nil, fmt.Errorf("scanning PostgreSQL admin member for %q: %w", role, err)
		}
		result = append(result, member)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating PostgreSQL admin members for %q: %w", role, err)
	}
	return result, nil
}

func loadOwnership(
	ctx context.Context,
	db policyDB,
	database string,
	role string,
) (ownershipState, error) {
	var result ownershipState
	if err := db.QueryRow(ctx, `
		SELECT
			(SELECT count(*)
			 FROM pg_catalog.pg_database AS database_row
			 JOIN pg_catalog.pg_roles AS owner_role ON owner_role.oid = database_row.datdba
			 WHERE database_row.datname = $1 AND owner_role.rolname = $2),
			(SELECT count(*)
			 FROM pg_catalog.pg_namespace AS schema_row
			 JOIN pg_catalog.pg_roles AS owner_role ON owner_role.oid = schema_row.nspowner
			 WHERE owner_role.rolname = $2),
			(SELECT count(*)
			 FROM pg_catalog.pg_class AS relation_row
			 JOIN pg_catalog.pg_roles AS owner_role ON owner_role.oid = relation_row.relowner
			 WHERE owner_role.rolname = $2),
			(SELECT count(*)
			 FROM pg_catalog.pg_proc AS function_row
			 JOIN pg_catalog.pg_roles AS owner_role ON owner_role.oid = function_row.proowner
			 WHERE owner_role.rolname = $2),
			(SELECT count(*)
			 FROM pg_catalog.pg_type AS type_row
			 JOIN pg_catalog.pg_roles AS owner_role ON owner_role.oid = type_row.typowner
			 WHERE owner_role.rolname = $2),
			(SELECT count(*)
			 FROM pg_catalog.pg_largeobject_metadata AS large_object
			 JOIN pg_catalog.pg_roles AS owner_role ON owner_role.oid = large_object.lomowner
			 WHERE owner_role.rolname = $2)
	`, database, role).Scan(
		&result.database,
		&result.schema,
		&result.relations,
		&result.functions,
		&result.types,
		&result.largeObjects,
	); err != nil {
		return ownershipState{}, fmt.Errorf("loading PostgreSQL ownership for %q: %w", role, err)
	}
	return result, nil
}

func loadPrincipalSnapshot(
	ctx context.Context,
	db policyDB,
	environment connectionEnvironment,
	schema string,
	role string,
) (principalSnapshot, error) {
	attributes, err := loadRoleAttributes(ctx, db, role)
	if err != nil {
		return principalSnapshot{}, err
	}
	memberships, err := loadRoleMemberships(ctx, db, role)
	if err != nil {
		return principalSnapshot{}, err
	}
	adminMembers, err := loadRoleAdminMembers(ctx, db, role)
	if err != nil {
		return principalSnapshot{}, err
	}
	ownership, err := loadOwnership(ctx, db, environment.database, role)
	if err != nil {
		return principalSnapshot{}, err
	}
	snapshot := principalSnapshot{
		role:         attributes,
		memberships:  memberships,
		adminMembers: adminMembers,
		ownership:    ownership,
	}
	if err := db.QueryRow(ctx, `
		SELECT
			pg_catalog.has_database_privilege($1, $2, 'CONNECT'),
			pg_catalog.has_database_privilege($1, $2, 'CREATE'),
			pg_catalog.has_database_privilege($1, $2, 'TEMPORARY'),
			pg_catalog.has_schema_privilege($1, $3, 'USAGE'),
			pg_catalog.has_schema_privilege($1, $3, 'CREATE')
	`, role, environment.database, schema).Scan(
		&snapshot.databaseConnect,
		&snapshot.databaseCreate,
		&snapshot.databaseTemporary,
		&snapshot.schemaUsage,
		&snapshot.schemaCreate,
	); err != nil {
		return principalSnapshot{}, fmt.Errorf("loading database/schema privileges for %q: %w", role, err)
	}
	snapshot.tablePrivileges, err = loadTablePrivileges(
		ctx,
		db,
		schema,
		role,
		supportedTablePrivileges(environment.serverVersionNum),
	)
	if err != nil {
		return principalSnapshot{}, err
	}
	snapshot.columnPrivileges, err = loadColumnPrivileges(ctx, db, schema, role)
	if err != nil {
		return principalSnapshot{}, err
	}
	snapshot.functionExecute, err = loadExecutableFunctions(ctx, db, schema, role)
	if err != nil {
		return principalSnapshot{}, err
	}
	snapshot.grantOptions, err = loadGrantOptions(ctx, db, environment.database, schema, role)
	if err != nil {
		return principalSnapshot{}, err
	}
	snapshot.unmanifested, err = loadUnmanifestedAuthorities(
		ctx,
		db,
		schema,
		role,
		environment.serverVersionNum,
	)
	if err != nil {
		return principalSnapshot{}, err
	}
	snapshot.configuration, err = loadRoleConfiguration(ctx, db, environment.database, role)
	if err != nil {
		return principalSnapshot{}, err
	}
	return snapshot, nil
}

func loadTablePrivileges(
	ctx context.Context,
	db policyDB,
	schema string,
	role string,
	privileges []string,
) (map[string]map[string]bool, error) {
	rows, err := db.Query(ctx, `
		SELECT
			relation.relname,
			privilege.name,
			pg_catalog.has_table_privilege($1, relation.oid, privilege.name)
		FROM pg_catalog.pg_class AS relation
		JOIN pg_catalog.pg_namespace AS relation_ns
		  ON relation_ns.oid = relation.relnamespace
		CROSS JOIN pg_catalog.unnest($3::TEXT[]) AS privilege(name)
		WHERE relation_ns.nspname = $2
		  AND relation.relkind IN ('r', 'p', 'v', 'm', 'f')
		ORDER BY relation.relname, privilege.name
	`, role, schema, privileges)
	if err != nil {
		return nil, fmt.Errorf("loading table privileges for %q: %w", role, err)
	}
	defer rows.Close()
	result := make(map[string]map[string]bool)
	for rows.Next() {
		var table, privilege string
		var allowed bool
		if err := rows.Scan(&table, &privilege, &allowed); err != nil {
			return nil, fmt.Errorf("scanning table privilege for %q: %w", role, err)
		}
		if result[table] == nil {
			result[table] = make(map[string]bool)
		}
		result[table][privilege] = allowed
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating table privileges for %q: %w", role, err)
	}
	return result, nil
}

func loadColumnPrivileges(
	ctx context.Context,
	db policyDB,
	schema string,
	role string,
) ([]columnPrivilege, error) {
	privileges := []string{
		string(PrivilegeSelect),
		string(PrivilegeInsert),
		string(PrivilegeUpdate),
		string(PrivilegeReferences),
	}
	rows, err := db.Query(ctx, `
		SELECT relation.relname, attribute.attname, privilege.name
		FROM pg_catalog.pg_class AS relation
		JOIN pg_catalog.pg_namespace AS relation_ns
		  ON relation_ns.oid = relation.relnamespace
		JOIN pg_catalog.pg_attribute AS attribute
		  ON attribute.attrelid = relation.oid
		 AND attribute.attnum > 0
		 AND NOT attribute.attisdropped
		CROSS JOIN pg_catalog.unnest($3::TEXT[]) AS privilege(name)
		WHERE relation_ns.nspname = $2
		  AND relation.relkind IN ('r', 'p', 'v', 'm', 'f')
		  AND pg_catalog.has_column_privilege($1, relation.oid, attribute.attnum, privilege.name)
		ORDER BY relation.relname, attribute.attnum, privilege.name
	`, role, schema, privileges)
	if err != nil {
		return nil, fmt.Errorf("loading column privileges for %q: %w", role, err)
	}
	defer rows.Close()
	var result []columnPrivilege
	for rows.Next() {
		var privilege columnPrivilege
		if err := rows.Scan(&privilege.table, &privilege.column, &privilege.privilege); err != nil {
			return nil, fmt.Errorf("scanning column privilege for %q: %w", role, err)
		}
		result = append(result, privilege)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating column privileges for %q: %w", role, err)
	}
	return result, nil
}

func loadExecutableFunctions(
	ctx context.Context,
	db policyDB,
	schema string,
	role string,
) ([]string, error) {
	rows, err := db.Query(ctx, `
		SELECT function_row.proname || '(' || pg_catalog.oidvectortypes(function_row.proargtypes) || ')'
		FROM pg_catalog.pg_proc AS function_row
		JOIN pg_catalog.pg_namespace AS function_ns
		  ON function_ns.oid = function_row.pronamespace
		WHERE function_ns.nspname = $2
		  AND pg_catalog.has_function_privilege($1, function_row.oid, 'EXECUTE')
		ORDER BY function_row.proname, function_row.proargtypes
	`, role, schema)
	if err != nil {
		return nil, fmt.Errorf("loading function privileges for %q: %w", role, err)
	}
	defer rows.Close()
	var result []string
	for rows.Next() {
		var function string
		if err := rows.Scan(&function); err != nil {
			return nil, fmt.Errorf("scanning function privilege for %q: %w", role, err)
		}
		result = append(result, function)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating function privileges for %q: %w", role, err)
	}
	return result, nil
}

func loadGrantOptions(
	ctx context.Context,
	db policyDB,
	database string,
	schema string,
	role string,
) ([]string, error) {
	rows, err := db.Query(ctx, `
		WITH target_role AS (
			SELECT oid FROM pg_catalog.pg_roles WHERE rolname = $3
		)
		SELECT object_kind || ' ' || object_name || ' ' || privilege_type
		FROM (
			SELECT 'DATABASE'::TEXT AS object_kind, database_row.datname AS object_name, acl.privilege_type
			FROM pg_catalog.pg_database AS database_row
			CROSS JOIN LATERAL pg_catalog.aclexplode(
				COALESCE(database_row.datacl, pg_catalog.acldefault('d', database_row.datdba))
			) AS acl
			JOIN target_role ON target_role.oid = acl.grantee
			WHERE database_row.datname = $1 AND acl.is_grantable
			UNION ALL
			SELECT 'SCHEMA', schema_row.nspname, acl.privilege_type
			FROM pg_catalog.pg_namespace AS schema_row
			CROSS JOIN LATERAL pg_catalog.aclexplode(
				COALESCE(schema_row.nspacl, pg_catalog.acldefault('n', schema_row.nspowner))
			) AS acl
			JOIN target_role ON target_role.oid = acl.grantee
			WHERE schema_row.nspname = $2 AND acl.is_grantable
			UNION ALL
			SELECT 'TABLE', relation.relname, acl.privilege_type
			FROM pg_catalog.pg_class AS relation
			JOIN pg_catalog.pg_namespace AS relation_ns ON relation_ns.oid = relation.relnamespace
			CROSS JOIN LATERAL pg_catalog.aclexplode(
				COALESCE(relation.relacl, pg_catalog.acldefault('r', relation.relowner))
			) AS acl
			JOIN target_role ON target_role.oid = acl.grantee
			WHERE relation_ns.nspname = $2 AND acl.is_grantable
			UNION ALL
			SELECT 'COLUMN', relation.relname || '.' || attribute.attname, acl.privilege_type
			FROM pg_catalog.pg_attribute AS attribute
			JOIN pg_catalog.pg_class AS relation ON relation.oid = attribute.attrelid
			JOIN pg_catalog.pg_namespace AS relation_ns ON relation_ns.oid = relation.relnamespace
			CROSS JOIN LATERAL pg_catalog.aclexplode(attribute.attacl) AS acl
			JOIN target_role ON target_role.oid = acl.grantee
			WHERE relation_ns.nspname = $2 AND acl.is_grantable
			UNION ALL
			SELECT 'FUNCTION', function_row.proname, acl.privilege_type
			FROM pg_catalog.pg_proc AS function_row
			JOIN pg_catalog.pg_namespace AS function_ns ON function_ns.oid = function_row.pronamespace
			CROSS JOIN LATERAL pg_catalog.aclexplode(
				COALESCE(function_row.proacl, pg_catalog.acldefault('f', function_row.proowner))
			) AS acl
			JOIN target_role ON target_role.oid = acl.grantee
			WHERE function_ns.nspname = $2 AND acl.is_grantable
		) AS grant_option
		ORDER BY object_kind, object_name, privilege_type
	`, database, schema, role)
	if err != nil {
		return nil, fmt.Errorf("loading grant options for %q: %w", role, err)
	}
	defer rows.Close()
	var result []string
	for rows.Next() {
		var grant string
		if err := rows.Scan(&grant); err != nil {
			return nil, fmt.Errorf("scanning grant option for %q: %w", role, err)
		}
		result = append(result, grant)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating grant options for %q: %w", role, err)
	}
	return result, nil
}

func verifyPublicPolicy(ctx context.Context, db policyDB, database, schema string) error {
	rows, err := db.Query(ctx, `
		SELECT object_kind || ' ' || object_name || ' ' || privilege_type
		FROM (
			SELECT 'DATABASE'::TEXT AS object_kind, database_row.datname AS object_name, acl.privilege_type
			FROM pg_catalog.pg_database AS database_row
			CROSS JOIN LATERAL pg_catalog.aclexplode(
				COALESCE(database_row.datacl, pg_catalog.acldefault('d', database_row.datdba))
			) AS acl
			WHERE database_row.datname = $1
			  AND acl.grantee = 0
			  AND acl.privilege_type IN ('CREATE', 'TEMPORARY', 'TEMP')
			UNION ALL
			SELECT 'SCHEMA', schema_row.nspname, acl.privilege_type
			FROM pg_catalog.pg_namespace AS schema_row
			CROSS JOIN LATERAL pg_catalog.aclexplode(
				COALESCE(schema_row.nspacl, pg_catalog.acldefault('n', schema_row.nspowner))
			) AS acl
			WHERE schema_row.nspname = $2 AND acl.grantee = 0
			UNION ALL
			SELECT 'TABLE', relation.relname, acl.privilege_type
			FROM pg_catalog.pg_class AS relation
			JOIN pg_catalog.pg_namespace AS relation_ns ON relation_ns.oid = relation.relnamespace
			CROSS JOIN LATERAL pg_catalog.aclexplode(
				COALESCE(relation.relacl, pg_catalog.acldefault('r', relation.relowner))
			) AS acl
			WHERE relation_ns.nspname = $2 AND acl.grantee = 0
			UNION ALL
			SELECT 'COLUMN', relation.relname || '.' || attribute.attname, acl.privilege_type
			FROM pg_catalog.pg_attribute AS attribute
			JOIN pg_catalog.pg_class AS relation ON relation.oid = attribute.attrelid
			JOIN pg_catalog.pg_namespace AS relation_ns ON relation_ns.oid = relation.relnamespace
			CROSS JOIN LATERAL pg_catalog.aclexplode(attribute.attacl) AS acl
			WHERE relation_ns.nspname = $2 AND acl.grantee = 0
			UNION ALL
			SELECT 'FUNCTION', function_row.proname, acl.privilege_type
			FROM pg_catalog.pg_proc AS function_row
			JOIN pg_catalog.pg_namespace AS function_ns ON function_ns.oid = function_row.pronamespace
			CROSS JOIN LATERAL pg_catalog.aclexplode(
				COALESCE(function_row.proacl, pg_catalog.acldefault('f', function_row.proowner))
			) AS acl
			WHERE function_ns.nspname = $2 AND acl.grantee = 0
		) AS public_authority
		ORDER BY object_kind, object_name, privilege_type
	`, database, schema)
	if err != nil {
		return fmt.Errorf("checking PUBLIC database role policy: %w", err)
	}
	defer rows.Close()
	var authority []string
	for rows.Next() {
		var privilege string
		if err := rows.Scan(&privilege); err != nil {
			return fmt.Errorf("scanning PUBLIC database role policy: %w", err)
		}
		authority = append(authority, privilege)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterating PUBLIC database role policy: %w", err)
	}
	if len(authority) != 0 {
		return policyViolation("PUBLIC retains forbidden authority %v", authority)
	}
	return nil
}

func validateSafeRoleAttributes(role roleAttributes, expectedName string, expectedLogin bool) error {
	if role.name != expectedName {
		return policyViolation("role identity = %q, want %q", role.name, expectedName)
	}
	if role.canLogin != expectedLogin {
		return policyViolation("role %q LOGIN = %t, want %t", role.name, role.canLogin, expectedLogin)
	}
	if expectedLogin && role.inherit {
		return policyViolation("LOGIN role %q must be NOINHERIT", role.name)
	}
	for _, capability := range []struct {
		name    string
		enabled bool
	}{
		{name: "SUPERUSER", enabled: role.superuser},
		{name: "BYPASSRLS", enabled: role.bypassRLS},
		{name: "CREATEDB", enabled: role.createDB},
		{name: "CREATEROLE", enabled: role.createRole},
		{name: "REPLICATION", enabled: role.replication},
	} {
		if capability.enabled {
			return policyViolation("role %q has forbidden %s", role.name, capability.name)
		}
	}
	return nil
}

func validatePrincipalSnapshot(
	snapshot principalSnapshot,
	manifest Manifest,
	expectation principalExpectation,
) error {
	if err := validateSafeRoleAttributes(snapshot.role, expectation.name, expectation.login); err != nil {
		return err
	}
	if expectation.allowedMembership == "" {
		if len(snapshot.memberships) != 0 {
			return policyViolation("role %q has forbidden membership %+v", expectation.name, snapshot.memberships)
		}
	} else {
		if len(snapshot.memberships) != 1 ||
			snapshot.memberships[0].role != expectation.allowedMembership ||
			snapshot.memberships[0].adminOption ||
			snapshot.memberships[0].inheritOption ||
			!snapshot.memberships[0].setOption {
			return policyViolation(
				"session role %q membership = %+v, want one non-admin SET membership in %q",
				expectation.name,
				snapshot.memberships,
				expectation.allowedMembership,
			)
		}
	}
	if expectation.rejectAdminMembers && len(snapshot.adminMembers) != 0 {
		return policyViolation("role %q has membership administrators %v", expectation.name, snapshot.adminMembers)
	}
	if snapshot.ownership.any() {
		return policyViolation("role %q owns database or schema objects: %+v", expectation.name, snapshot.ownership)
	}
	if !snapshot.databaseConnect {
		return policyViolation("role %q is missing database CONNECT", expectation.name)
	}
	if snapshot.databaseCreate {
		return policyViolation("role %q has forbidden database CREATE", expectation.name)
	}
	if snapshot.databaseTemporary {
		return policyViolation("role %q has forbidden database TEMPORARY", expectation.name)
	}
	if snapshot.schemaCreate {
		return policyViolation("role %q has forbidden schema CREATE", expectation.name)
	}
	if expectation.requireSchemaUse && !snapshot.schemaUsage {
		return policyViolation("role %q is missing schema USAGE", expectation.name)
	}
	if expectation.allowedMembership != "" && snapshot.schemaUsage {
		return policyViolation("LOGIN role %q has direct/effective schema USAGE", expectation.name)
	}
	if len(snapshot.functionExecute) != 0 {
		return policyViolation("role %q has forbidden function EXECUTE %v", expectation.name, snapshot.functionExecute)
	}
	if len(snapshot.grantOptions) != 0 {
		return policyViolation("role %q has forbidden grant option %v", expectation.name, snapshot.grantOptions)
	}
	if len(snapshot.unmanifested) != 0 {
		return policyViolation(
			"role %q has authority outside the selected AHE schema manifest %v",
			expectation.name,
			snapshot.unmanifested,
		)
	}
	if len(snapshot.configuration) != 0 {
		return policyViolation(
			"role %q has forbidden persistent configuration %v",
			expectation.name,
			snapshot.configuration,
		)
	}

	expectedTables := make(map[string]map[string]bool, len(manifest.Tables))
	for _, rule := range manifest.Tables {
		privileges := make(map[string]bool, len(rule.Privileges))
		for _, privilege := range rule.Privileges {
			privileges[string(privilege)] = true
		}
		expectedTables[rule.Table] = privileges
	}
	if expectation.exactPrivileges {
		for table := range expectedTables {
			if _, ok := snapshot.tablePrivileges[table]; !ok {
				return policyViolation("role %q is missing required table %q", expectation.name, table)
			}
		}
	}
	tables := make([]string, 0, len(snapshot.tablePrivileges))
	for table := range snapshot.tablePrivileges {
		tables = append(tables, table)
	}
	sort.Strings(tables)
	for _, table := range tables {
		actual := snapshot.tablePrivileges[table]
		expected, known := expectedTables[table]
		privileges := make([]string, 0, len(actual))
		for privilege := range actual {
			privileges = append(privileges, privilege)
		}
		sort.Strings(privileges)
		for _, privilege := range privileges {
			allowed := actual[privilege]
			wanted := expectation.allowedMembership == "" && known && expected[privilege]
			if allowed && !wanted {
				return policyViolation("role %q has forbidden %s on table %q", expectation.name, privilege, table)
			}
			if expectation.exactPrivileges && wanted && !allowed {
				return policyViolation("role %q is missing %s on table %q", expectation.name, privilege, table)
			}
		}
		if expectation.exactPrivileges {
			for privilege := range expected {
				if !actual[privilege] {
					return policyViolation("role %q is missing %s on table %q", expectation.name, privilege, table)
				}
			}
		}
	}
	for _, privilege := range snapshot.columnPrivileges {
		expected := expectedTables[privilege.table]
		if expectation.allowedMembership != "" || expected == nil || !expected[privilege.privilege] {
			return policyViolation(
				"role %q has forbidden column %s on %s.%s",
				expectation.name,
				privilege.privilege,
				privilege.table,
				privilege.column,
			)
		}
	}
	return nil
}
