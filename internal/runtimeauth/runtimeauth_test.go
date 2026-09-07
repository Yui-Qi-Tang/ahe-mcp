package runtimeauth

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestNewPrincipalRequiresExactBoundedLauncherIdentity(t *testing.T) {
	for _, id := range []string{"", " ", " reviewer", "reviewer ", "review\ner", "review\x00er", strings.Repeat("x", PrincipalIDMaxBytes+1), string([]byte{0xff})} {
		principal, err := NewPrincipal(id)
		if !errors.Is(err, ErrUnauthenticated) || principal.ID != "" {
			t.Errorf("invalid principal returned identity or wrong error: %+v %v", principal, err)
		}
	}
	for _, id := range []string{"detective:local", "reviewer:台灣", strings.Repeat("x", PrincipalIDMaxBytes)} {
		principal, err := NewPrincipal(id)
		if err != nil || principal.ID != id {
			t.Errorf("valid principal was not preserved: %+v %v", principal, err)
		}
	}
}

func TestAuthorizationErrorsKeepStableSafeTransport(t *testing.T) {
	for _, tc := range []struct {
		err  error
		kind error
		code string
	}{
		{NewUnauthenticatedError("identity unavailable"), ErrUnauthenticated, "unauthenticated"},
		{NewUnauthorizedError("operation unavailable"), ErrUnauthorized, "permission_denied"},
	} {
		if !errors.Is(tc.err, tc.kind) {
			t.Fatalf("error does not retain its kind: %v", tc.err)
		}
		encoded, err := json.Marshal(tc.err)
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]string
		if err := json.Unmarshal(encoded, &fields); err != nil || len(fields) != 2 || fields["code"] != tc.code || fields["message"] == "" {
			t.Fatalf("unexpected transport fields: %s %v", encoded, err)
		}
	}
	_, err := NewPrincipal(" secret-invalid-identity ")
	encoded, marshalErr := json.Marshal(err)
	if marshalErr != nil || strings.Contains(string(encoded), "secret-invalid-identity") || strings.Contains(err.Error(), "secret-invalid-identity") {
		t.Fatal("rejected launcher identity escaped through the error")
	}
}
