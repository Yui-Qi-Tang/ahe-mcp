package migrations

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"
)

type requiredFixedCoreTrigger struct {
	Migration         string
	Name              string
	Table             string
	Function          string
	TriggerType       int
	Constraint        bool
	Deferrable        bool
	InitiallyDeferred bool
}

var requiredFixedCoreTriggers = append(append(slices.Clone(propositionBindingTriggers), externalCheckTriggers...), externalRepresentationTriggers...)

type requiredFixedCoreFunction struct {
	Migration                   string
	Name                        string
	Arguments                   string
	Result                      string
	SecurityDefiner             bool
	Config                      []string
	RequirePublicExecuteRevoked bool
}

var requiredFixedCoreFunctions = append(append(slices.Clone(propositionBindingFunctions), externalCheckFunctions...), externalRepresentationFunctions...)

type requiredFixedCoreColumn struct {
	Migration string
	Table     string
	Name      string
	DataType  string
	NotNull   bool
	Default   string
}

var requiredFixedCoreColumns = append(append(slices.Clone(propositionBindingColumns), externalCheckColumns...), externalRepresentationColumns...)

type requiredFixedCoreConstraint struct {
	Migration         string
	Name              string
	Table             string
	Type              string
	Deferrable        bool
	InitiallyDeferred bool
	Definition        string
}

var requiredFixedCoreConstraints = append(append(slices.Clone(propositionBindingConstraints), externalCheckConstraints...), externalRepresentationConstraints...)

func verifyFixedCoreSchemaObjects(ctx context.Context, db tableQueryer, migration string) error {
	for _, required := range requiredFixedCoreTriggers {
		if !fixedCoreMigrationSelected(migration, required.Migration) {
			continue
		}
		var (
			tableName         string
			functionName      string
			triggerType       int
			enabled           string
			constraintTrigger bool
			deferrable        bool
			initiallyDeferred bool
			argumentBytes     int
			hasWhen           bool
		)
		err := db.QueryRow(ctx, `
			SELECT
				table_rel.relname,
				function_proc.proname,
				trigger_row.tgtype::INTEGER,
				trigger_row.tgenabled::TEXT,
				trigger_row.tgconstraint <> 0,
				trigger_row.tgdeferrable,
				trigger_row.tginitdeferred,
				pg_catalog.octet_length(trigger_row.tgargs),
				trigger_row.tgqual IS NOT NULL
			FROM pg_trigger AS trigger_row
			JOIN pg_class AS table_rel ON table_rel.oid = trigger_row.tgrelid
			JOIN pg_namespace AS table_ns ON table_ns.oid = table_rel.relnamespace
			JOIN pg_proc AS function_proc ON function_proc.oid = trigger_row.tgfoid
			JOIN pg_namespace AS function_ns ON function_ns.oid = function_proc.pronamespace
			WHERE table_ns.nspname = current_schema()
			  AND function_ns.nspname = current_schema()
			  AND trigger_row.tgname = $1
			  AND table_rel.relname = $2
			  AND NOT trigger_row.tgisinternal
		`, required.Name, required.Table).Scan(
			&tableName,
			&functionName,
			&triggerType,
			&enabled,
			&constraintTrigger,
			&deferrable,
			&initiallyDeferred,
			&argumentBytes,
			&hasWhen,
		)
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("required fixed-core trigger %s is missing", required.Name)
		}
		if err != nil {
			return fmt.Errorf("checking fixed-core trigger %s: %w", required.Name, err)
		}
		if tableName != required.Table || functionName != required.Function ||
			triggerType != required.TriggerType || enabled != "O" ||
			constraintTrigger != required.Constraint || deferrable != required.Deferrable ||
			initiallyDeferred != required.InitiallyDeferred || argumentBytes != 0 || hasWhen {
			return fmt.Errorf("required fixed-core trigger %s does not match its authority contract", required.Name)
		}
	}

	for _, required := range requiredFixedCoreFunctions {
		if !fixedCoreMigrationSelected(migration, required.Migration) {
			continue
		}
		var (
			language     string
			arguments    string
			result       string
			kind         string
			volatility   string
			parallel     string
			strict       bool
			security     bool
			leakproof    bool
			config       []string
			publicExec   bool
			source       string
			ownerMatches bool
		)
		ownerTable := "canonical_propositions"
		switch required.Migration {
		case consistencyMigration:
			ownerTable = "consistency_watches"
		case externalCheckMigration:
			ownerTable = "external_check_records"
		case externalRepresentationMigration:
			ownerTable = "external_representation_records"
		}
		err := db.QueryRow(ctx, `
			SELECT
				language_row.lanname,
				oidvectortypes(function_row.proargtypes),
				pg_get_function_result(function_row.oid),
				function_row.prokind::TEXT,
				function_row.provolatile::TEXT,
				function_row.proparallel::TEXT,
				function_row.proisstrict,
				function_row.prosecdef,
				function_row.proleakproof,
				COALESCE(function_row.proconfig, '{}'::TEXT[]),
				EXISTS (
					SELECT 1
					FROM pg_catalog.aclexplode(
						COALESCE(
							function_row.proacl,
							pg_catalog.acldefault('f', function_row.proowner)
						)
					) AS function_acl
					WHERE function_acl.grantee = 0
					  AND function_acl.privilege_type = 'EXECUTE'
				),
				function_row.prosrc,
				function_row.proowner = (
					SELECT c.relowner FROM pg_catalog.pg_class AS c
					WHERE c.relnamespace = function_row.pronamespace AND c.relname = $3
				)
			FROM pg_proc AS function_row
			JOIN pg_namespace AS function_ns ON function_ns.oid = function_row.pronamespace
			JOIN pg_language AS language_row ON language_row.oid = function_row.prolang
			WHERE function_ns.nspname = current_schema()
			  AND function_row.proname = $1
			  AND oidvectortypes(function_row.proargtypes) = $2
		`, required.Name, required.Arguments, ownerTable).Scan(
			&language,
			&arguments,
			&result,
			&kind,
			&volatility,
			&parallel,
			&strict,
			&security,
			&leakproof,
			&config,
			&publicExec,
			&source,
			&ownerMatches,
		)
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("required fixed-core function %s(%s) is missing", required.Name, required.Arguments)
		}
		if err != nil {
			return fmt.Errorf("checking fixed-core function %s: %w", required.Name, err)
		}
		if language != "plpgsql" || arguments != required.Arguments || result != required.Result ||
			kind != "f" || volatility != "v" || parallel != "u" || strict || leakproof {
			return fmt.Errorf("required fixed-core function %s does not match its signature contract", required.Name)
		}
		if !ownerMatches || security != required.SecurityDefiner || !slices.Equal(config, required.Config) ||
			(required.RequirePublicExecuteRevoked && publicExec) {
			return fmt.Errorf("required fixed-core function %s does not match its execution contract", required.Name)
		}
		expectedSource, err := embeddedMigrationFunctionSource(required.Migration, required.Name)
		if err != nil {
			return err
		}
		if source != expectedSource {
			return fmt.Errorf("required fixed-core function %s does not match its definition contract", required.Name)
		}
	}

	for _, required := range requiredFixedCoreColumns {
		if !fixedCoreMigrationSelected(migration, required.Migration) {
			continue
		}
		var (
			dataType     string
			notNull      bool
			defaultValue string
		)
		err := db.QueryRow(ctx, `
			SELECT
				format_type(attribute_row.atttypid, attribute_row.atttypmod),
				attribute_row.attnotnull,
				COALESCE(pg_get_expr(default_row.adbin, default_row.adrelid), '')
			FROM pg_attribute AS attribute_row
			JOIN pg_class AS table_row
			  ON table_row.oid = attribute_row.attrelid
			JOIN pg_namespace AS table_ns
			  ON table_ns.oid = table_row.relnamespace
			LEFT JOIN pg_attrdef AS default_row
			  ON default_row.adrelid = attribute_row.attrelid
			 AND default_row.adnum = attribute_row.attnum
			WHERE table_ns.nspname = current_schema()
			  AND table_row.relname = $1
			  AND attribute_row.attname = $2
			  AND attribute_row.attnum > 0
			  AND NOT attribute_row.attisdropped
		`, required.Table, required.Name).Scan(&dataType, &notNull, &defaultValue)
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("required fixed-core column %s.%s is missing", required.Table, required.Name)
		}
		if err != nil {
			return fmt.Errorf("checking fixed-core column %s.%s: %w", required.Table, required.Name, err)
		}
		if dataType != required.DataType || notNull != required.NotNull ||
			normalizeSchemaDefinition(defaultValue) != normalizeSchemaDefinition(required.Default) {
			return fmt.Errorf("required fixed-core column %s.%s does not match its definition contract", required.Table, required.Name)
		}
	}

	for _, required := range requiredFixedCoreConstraints {
		if !fixedCoreMigrationSelected(migration, required.Migration) {
			continue
		}
		var (
			tableName         string
			constraintType    string
			deferrable        bool
			initiallyDeferred bool
			validated         bool
			definition        string
		)
		err := db.QueryRow(ctx, `
			SELECT
				table_rel.relname,
				constraint_row.contype::TEXT,
				constraint_row.condeferrable,
				constraint_row.condeferred,
				constraint_row.convalidated,
				pg_get_constraintdef(constraint_row.oid)
			FROM pg_constraint AS constraint_row
			JOIN pg_class AS table_rel ON table_rel.oid = constraint_row.conrelid
			JOIN pg_namespace AS table_ns ON table_ns.oid = table_rel.relnamespace
			WHERE table_ns.nspname = current_schema()
			  AND table_rel.relname = $1
			  AND constraint_row.conname = $2
		`, required.Table, required.Name).Scan(
			&tableName,
			&constraintType,
			&deferrable,
			&initiallyDeferred,
			&validated,
			&definition,
		)
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("required fixed-core constraint %s is missing", required.Name)
		}
		if err != nil {
			return fmt.Errorf("checking fixed-core constraint %s: %w", required.Name, err)
		}
		if tableName != required.Table || constraintType != required.Type ||
			deferrable != required.Deferrable || initiallyDeferred != required.InitiallyDeferred || !validated ||
			normalizeSchemaDefinition(definition) != normalizeSchemaDefinition(required.Definition) {
			return fmt.Errorf("required fixed-core constraint %s does not match its definition contract", required.Name)
		}
	}

	return nil
}

func fixedCoreMigrationSelected(requested, required string) bool {
	return requested == "" || requested == required
}
func embeddedMigrationFunctionSource(migration, name string) (string, error) {
	data, err := upFiles.ReadFile(migration)
	if err != nil {
		return "", fmt.Errorf("reading migration %s: %w", migration, err)
	}
	sql := string(data)
	declarationIndex := strings.Index(sql, "CREATE FUNCTION "+name+"(")
	if declarationIndex < 0 {
		declarationIndex = strings.Index(sql, "CREATE OR REPLACE FUNCTION "+name+"(")
	}
	if declarationIndex < 0 {
		return "", fmt.Errorf("migration %s does not define function %s", migration, name)
	}
	bodyMarker := "AS $$"
	bodyStart := strings.Index(sql[declarationIndex:], bodyMarker)
	if bodyStart < 0 {
		return "", fmt.Errorf("migration function %s has no dollar-quoted body", name)
	}
	bodyStart += declarationIndex + len(bodyMarker)
	bodyEnd := strings.Index(sql[bodyStart:], "$$;")
	if bodyEnd < 0 {
		return "", fmt.Errorf("migration function %s has an unterminated body", name)
	}
	return sql[bodyStart : bodyStart+bodyEnd], nil
}
