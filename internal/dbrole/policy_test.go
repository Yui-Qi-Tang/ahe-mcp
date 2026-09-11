package dbrole

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
)

func TestPolicyEntryPointsRejectMissingPhysicalConnection(t *testing.T) {
	_, err := InstallPolicy(context.Background(), nil, InstallInput{
		Role:    "ahe_query",
		Schema:  "public",
		Profile: ProfileQuery,
	})
	if err == nil || !strings.Contains(err.Error(), "connection") {
		t.Fatalf("InstallPolicy() error = %v, want missing connection", err)
	}

	_, err = VerifyRuntimeConnection(context.Background(), nil, RuntimeVerificationInput{
		Role:        "ahe_query",
		SessionUser: "ahe_query_login",
		Schema:      "public",
		Profile:     ProfileQuery,
	})
	if err == nil || !strings.Contains(err.Error(), "connection") {
		t.Fatalf("VerifyRuntimeConnection() error = %v, want missing connection", err)
	}
}

func TestPolicyInputsRejectAmbiguousAuthority(t *testing.T) {
	for _, test := range []struct {
		name  string
		input InstallInput
	}{
		{name: "blank role", input: InstallInput{Schema: "ahe", Profile: ProfileQuery}},
		{name: "public role", input: InstallInput{Role: "PUBLIC", Schema: "ahe", Profile: ProfileQuery}},
		{name: "trimmed role", input: InstallInput{Role: " ahe_query", Schema: "ahe", Profile: ProfileQuery}},
		{name: "nul schema", input: InstallInput{Role: "ahe_query", Schema: "ahe\x00unsafe", Profile: ProfileQuery}},
		{name: "unknown profile", input: InstallInput{Role: "ahe_query", Schema: "ahe", Profile: "unknown"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := validateInstallInput(test.input); err == nil {
				t.Fatal("validateInstallInput() accepted ambiguous authority")
			}
		})
	}

	err := validateRuntimeInput(RuntimeVerificationInput{
		Role:        "ahe_query",
		SessionUser: "ahe_query",
		Schema:      "ahe",
		Profile:     ProfileQuery,
	})
	if err == nil || !strings.Contains(err.Error(), "distinct") {
		t.Fatalf("validateRuntimeInput() error = %v, want distinct identities", err)
	}
}

func TestPolicyViolationSupportsErrorsIs(t *testing.T) {
	err := policyViolation("forbidden %s", "authority")
	if !errors.Is(err, ErrPolicyViolation) {
		t.Fatalf("errors.Is(%v, ErrPolicyViolation) = false", err)
	}
}

func TestPolicyStatementsApplyClosedACLWithoutRoleLifecycleMutation(t *testing.T) {
	manifest, err := BuildManifest(ProfileQuery)
	if err != nil {
		t.Fatal(err)
	}
	relations := make([]relationInventory, 0, len(manifest.Tables))
	for _, rule := range manifest.Tables {
		relation := relationInventory{name: rule.Table, columns: []string{"id"}}
		if rule.Table == "proposal_occurrences" {
			relation.columns = []string{"proposal_occurrence_id", "statement_text"}
		}
		relations = append(relations, relation)
	}
	statements, err := policyStatements("ahe-db", "tenant weird", "runtime\"query", manifest, relations)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(statements, "\n")
	for _, required := range []string{
		`REVOKE ALL PRIVILEGES ON SCHEMA "tenant weird" FROM PUBLIC`,
		`REVOKE ALL PRIVILEGES ON ALL TABLES IN SCHEMA "tenant weird" FROM PUBLIC`,
		`REVOKE ALL PRIVILEGES ON ALL FUNCTIONS IN SCHEMA "tenant weird" FROM PUBLIC`,
		`GRANT USAGE ON SCHEMA "tenant weird" TO "runtime""query"`,
		`GRANT SELECT ON TABLE "tenant weird"."proposal_occurrences" TO "runtime""query"`,
		`REVOKE ALL PRIVILEGES ("proposal_occurrence_id", "statement_text") ON TABLE "tenant weird"."proposal_occurrences" FROM PUBLIC`,
	} {
		if !strings.Contains(joined, required) {
			t.Errorf("policy statements omit %q:\n%s", required, joined)
		}
	}
	if strings.Contains(joined, `ON DATABASE "ahe-db" FROM PUBLIC`) {
		t.Fatalf("policy statements mutate database-wide PUBLIC authority:\n%s", joined)
	}
	for _, forbidden := range []string{"CREATE ROLE", "ALTER ROLE", "CREATE USER", "ALTER USER", "SET ROLE"} {
		if strings.Contains(joined, forbidden) {
			t.Errorf("policy statements contain role lifecycle operation %q", forbidden)
		}
	}
}

func TestSupportedTablePrivilegesHandlesPostgreSQL16And17(t *testing.T) {
	pg16 := supportedTablePrivileges(160000)
	pg17 := supportedTablePrivileges(170000)
	if slices.Contains(pg16, postgresPrivilegeMaintain) {
		t.Fatalf("PostgreSQL 16 privileges contain %s: %v", postgresPrivilegeMaintain, pg16)
	}
	if !slices.Contains(pg17, postgresPrivilegeMaintain) {
		t.Fatalf("PostgreSQL 17 privileges omit %s: %v", postgresPrivilegeMaintain, pg17)
	}
	if len(pg17) != len(pg16)+1 {
		t.Fatalf("PostgreSQL privilege sets = %v / %v", pg16, pg17)
	}
}

func TestValidatePrincipalSnapshotAcceptsExactGroupAndBoundedSession(t *testing.T) {
	manifest, err := BuildManifest(ProfileQuery)
	if err != nil {
		t.Fatal(err)
	}
	group := validPrincipalSnapshot(t, manifest, "ahe_query", false, true)
	if err := validatePrincipalSnapshot(group, manifest, principalExpectation{
		name:               "ahe_query",
		login:              false,
		exactPrivileges:    true,
		requireSchemaUse:   true,
		rejectAdminMembers: true,
	}); err != nil {
		t.Fatalf("validate exact group: %v", err)
	}

	session := validPrincipalSnapshot(t, manifest, "ahe_query_login", true, false)
	session.memberships = []roleMembership{{role: "ahe_query", setOption: true}}
	if err := validatePrincipalSnapshot(session, manifest, principalExpectation{
		name:              "ahe_query_login",
		login:             true,
		allowedMembership: "ahe_query",
	}); err != nil {
		t.Fatalf("validate bounded session: %v", err)
	}
}

func TestValidatePrincipalSnapshotRejectsEscalationAndDrift(t *testing.T) {
	manifest, err := BuildManifest(ProfileQuery)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		mutate func(*principalSnapshot)
		want   string
	}{
		{name: "superuser", mutate: func(s *principalSnapshot) { s.role.superuser = true }, want: "SUPERUSER"},
		{name: "bypass rls", mutate: func(s *principalSnapshot) { s.role.bypassRLS = true }, want: "BYPASSRLS"},
		{name: "create db", mutate: func(s *principalSnapshot) { s.role.createDB = true }, want: "CREATEDB"},
		{name: "create role", mutate: func(s *principalSnapshot) { s.role.createRole = true }, want: "CREATEROLE"},
		{name: "replication", mutate: func(s *principalSnapshot) { s.role.replication = true }, want: "REPLICATION"},
		{name: "ownership", mutate: func(s *principalSnapshot) { s.ownership.relations = 1 }, want: "owns"},
		{name: "large object ownership", mutate: func(s *principalSnapshot) { s.ownership.largeObjects = 1 }, want: "owns"},
		{name: "membership", mutate: func(s *principalSnapshot) {
			s.memberships = []roleMembership{{role: "pg_read_all_data", setOption: true}}
		}, want: "membership"},
		{name: "membership administrator", mutate: func(s *principalSnapshot) {
			s.adminMembers = []string{"unsafe_login"}
		}, want: "membership administrators"},
		{name: "schema create", mutate: func(s *principalSnapshot) { s.schemaCreate = true }, want: "schema CREATE"},
		{name: "database create", mutate: func(s *principalSnapshot) { s.databaseCreate = true }, want: "database CREATE"},
		{name: "database temp", mutate: func(s *principalSnapshot) { s.databaseTemporary = true }, want: "database TEMPORARY"},
		{name: "function execute", mutate: func(s *principalSnapshot) {
			s.functionExecute = []string{"unsafe_function()"}
		}, want: "function EXECUTE"},
		{name: "unknown table", mutate: func(s *principalSnapshot) {
			s.tablePrivileges["rogue_table"] = map[string]bool{string(PrivilegeSelect): true}
		}, want: "rogue_table"},
		{name: "excess table", mutate: func(s *principalSnapshot) {
			s.tablePrivileges["proposal_occurrences"][string(PrivilegeInsert)] = true
		}, want: "INSERT"},
		{name: "missing table", mutate: func(s *principalSnapshot) {
			s.tablePrivileges["proposal_occurrences"][string(PrivilegeSelect)] = false
		}, want: "missing"},
		{name: "maintain", mutate: func(s *principalSnapshot) {
			s.tablePrivileges["proposal_occurrences"][postgresPrivilegeMaintain] = true
		}, want: postgresPrivilegeMaintain},
		{name: "column privilege", mutate: func(s *principalSnapshot) {
			s.columnPrivileges = []columnPrivilege{{table: "proposal_occurrences", column: "statement_text", privilege: string(PrivilegeUpdate)}}
		}, want: "column"},
		{name: "grant option", mutate: func(s *principalSnapshot) {
			s.grantOptions = []string{"TABLE proposal_occurrences SELECT"}
		}, want: "grant option"},
		{name: "outside schema authority", mutate: func(s *principalSnapshot) {
			s.unmanifested = []string{"TABLE public.other_table UPDATE"}
		}, want: "outside the selected AHE schema manifest"},
		{name: "persistent configuration", mutate: func(s *principalSnapshot) {
			s.configuration = []string{"ROLE session_replication_role=replica"}
		}, want: "persistent configuration"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			snapshot := validPrincipalSnapshot(t, manifest, "ahe_query", false, true)
			test.mutate(&snapshot)
			err := validatePrincipalSnapshot(snapshot, manifest, principalExpectation{
				name:               "ahe_query",
				login:              false,
				exactPrivileges:    true,
				requireSchemaUse:   true,
				rejectAdminMembers: true,
			})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("validate error = %v, want detail %q", err, test.want)
			}
		})
	}
}

func TestValidateCriticalRuntimeSettings(t *testing.T) {
	valid := connectionEnvironment{
		sessionReplicationRole:   "origin",
		largeObjectCompatibility: "off",
	}
	if err := validateCriticalRuntimeSettings(valid); err != nil {
		t.Fatalf("valid critical settings rejected: %v", err)
	}
	for _, test := range []struct {
		name   string
		mutate func(*connectionEnvironment)
		want   string
	}{
		{name: "replica", mutate: func(e *connectionEnvironment) {
			e.sessionReplicationRole = "replica"
		}, want: "session_replication_role"},
		{name: "large object compatibility", mutate: func(e *connectionEnvironment) {
			e.largeObjectCompatibility = "on"
		}, want: "lo_compat_privileges"},
	} {
		t.Run(test.name, func(t *testing.T) {
			environment := valid
			test.mutate(&environment)
			if err := validateCriticalRuntimeSettings(environment); err == nil ||
				!strings.Contains(err.Error(), test.want) {
				t.Fatalf("validate error = %v, want detail %q", err, test.want)
			}
		})
	}
}

func TestValidatePrincipalSnapshotRejectsSessionMembershipAdminOrExtraRole(t *testing.T) {
	manifest, err := BuildManifest(ProfileQuery)
	if err != nil {
		t.Fatal(err)
	}
	for _, memberships := range [][]roleMembership{
		{{role: "ahe_query", adminOption: true, setOption: true}},
		{{role: "ahe_query", inheritOption: true, setOption: true}},
		{{role: "ahe_query", setOption: false}},
		{{role: "ahe_query", setOption: true}, {role: "pg_monitor", setOption: true}},
	} {
		snapshot := validPrincipalSnapshot(t, manifest, "ahe_query_login", true, false)
		snapshot.memberships = memberships
		if err := validatePrincipalSnapshot(snapshot, manifest, principalExpectation{
			name:              "ahe_query_login",
			login:             true,
			allowedMembership: "ahe_query",
		}); err == nil {
			t.Fatalf("memberships %+v accepted", memberships)
		}
	}
}

func TestValidatePrincipalSnapshotRejectsInheritingOrDirectlyPrivilegedLogin(t *testing.T) {
	manifest, err := BuildManifest(ProfileQuery)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		mutate func(*principalSnapshot)
		want   string
	}{
		{name: "role inherit", mutate: func(s *principalSnapshot) { s.role.inherit = true }, want: "NOINHERIT"},
		{name: "schema usage", mutate: func(s *principalSnapshot) { s.schemaUsage = true }, want: "schema USAGE"},
		{name: "table select", mutate: func(s *principalSnapshot) {
			s.tablePrivileges["proposal_occurrences"][string(PrivilegeSelect)] = true
		}, want: "SELECT"},
		{name: "column select", mutate: func(s *principalSnapshot) {
			s.columnPrivileges = []columnPrivilege{{
				table:     "proposal_occurrences",
				column:    "statement_text",
				privilege: string(PrivilegeSelect),
			}}
		}, want: "column"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			snapshot := validPrincipalSnapshot(t, manifest, "ahe_query_login", true, false)
			snapshot.memberships = []roleMembership{{role: "ahe_query", setOption: true}}
			test.mutate(&snapshot)
			err := validatePrincipalSnapshot(snapshot, manifest, principalExpectation{
				name:              "ahe_query_login",
				login:             true,
				allowedMembership: "ahe_query",
			})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("validate error = %v, want detail %q", err, test.want)
			}
		})
	}
}

func validPrincipalSnapshot(t *testing.T, manifest Manifest, name string, login, exact bool) principalSnapshot {
	t.Helper()
	tableACL := make(map[string]map[string]bool, len(manifest.Tables))
	for _, rule := range manifest.Tables {
		privileges := make(map[string]bool)
		for _, privilege := range supportedTablePrivileges(170000) {
			privileges[privilege] = false
		}
		if exact {
			for _, privilege := range rule.Privileges {
				privileges[string(privilege)] = true
			}
		}
		tableACL[rule.Table] = privileges
	}
	return principalSnapshot{
		role:            roleAttributes{name: name, canLogin: login},
		databaseConnect: true,
		schemaUsage:     exact,
		tablePrivileges: tableACL,
	}
}
