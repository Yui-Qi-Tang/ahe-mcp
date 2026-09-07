package dbrole

import (
	"context"
	"strings"
	"testing"
)

func TestProvisionInputIsClosed(t *testing.T) {
	valid := ProvisionInput{Database: "pilot", Schema: "ahe_next", Role: "query_group", SessionUser: "query_login", Profile: ProfileQuery}
	if err := validateProvisionInput(valid); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"", "PUBLIC", "information_schema", "pg_owner", "with space", "bad,pg_catalog", "1role", "bad\x00", strings.Repeat("x", 64)} {
		for index := range 4 {
			input := valid
			targets := []*string{&input.Database, &input.Schema, &input.Role, &input.SessionUser}
			*targets[index] = bad
			if err := validateProvisionInput(input); err == nil {
				t.Fatal("ambiguous, system or truncated identifier accepted")
			}
		}
	}
	for _, mutate := range []func(*ProvisionInput){
		func(input *ProvisionInput) { input.SessionUser = input.Role },
		func(input *ProvisionInput) { input.Profile = "legacy-operator" },
	} {
		input := valid
		mutate(&input)
		if err := validateProvisionInput(input); err == nil {
			t.Fatal("invalid pair or unsupported profile accepted")
		}
	}
	if _, err := ProvisionRuntime(context.Background(), nil, valid); err == nil {
		t.Fatal("nil postgres connection accepted")
	}
}
