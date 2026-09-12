package desktop

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/sourcemcp"
)

func atlassianSettings(s *Service) Settings {
	settings := s.Snapshot().Settings
	settings.Mode = "local"
	settings.Connections = []Connection{{ID: "atlassian", Name: "Atlassian", Transport: "atlassian-oauth", URL: sourcemcp.AtlassianEndpoint, AllowedTools: []string{"getJiraIssue"}}}
	return settings
}

func TestAtlassianLoginIsExplicitAndFailsWithoutLeaking(t *testing.T) {
	s := newTestService(t)
	calls := 0
	s.beginAtlassian = func(context.Context) (*sourcemcp.AtlassianAuthorization, error) {
		calls++
		return nil, errors.New("synthetic-private-provider-error")
	}
	if _, err := s.BeginAtlassianLogin(t.Context(), "atlassian"); err == nil || calls != 0 {
		t.Fatal("offline login attempted")
	}
	if _, err := s.SaveSettings(atlassianSettings(s)); err != nil || calls != 0 {
		t.Fatal("settings started login")
	}
	if _, err := s.DiscoverTools(t.Context(), "atlassian"); err == nil || calls != 0 {
		t.Fatal("discovery started implicit login")
	}
	if _, err := s.BeginAtlassianLogin(t.Context(), "other"); err == nil || calls != 0 {
		t.Fatal("unconfigured login attempted")
	}
	state, err := s.BeginAtlassianLogin(t.Context(), "atlassian")
	if err == nil || calls != 1 || state.Busy || strings.Contains(state.Error, "synthetic-private") {
		t.Fatal("login failure leaked or did not finish")
	}
}

func TestAtlassianSettingsBindEndpointAndClearSessions(t *testing.T) {
	s := newTestService(t)
	settings := atlassianSettings(s)
	if _, err := s.SaveSettings(settings); err != nil {
		t.Fatal(err)
	}
	for _, endpoint := range []string{"https://untrusted.invalid/mcp", sourcemcp.AtlassianEndpoint + "?token=synthetic"} {
		wrong := atlassianSettings(s)
		wrong.Connections[0].URL = endpoint
		if _, err := s.SaveSettings(wrong); err == nil {
			t.Fatal("alternate endpoint saved")
		}
	}
	for _, action := range []string{"settings", "demo", "disconnect", "close"} {
		t.Run(action, func(t *testing.T) {
			s := newTestService(t)
			if _, err := s.SaveSettings(atlassianSettings(s)); err != nil {
				t.Fatal(err)
			}
			s.oauthSessions = map[string]*sourcemcp.OAuthSession{"atlassian": {}}
			s.state.SourceAuth = map[string]SourceAuthView{"atlassian": {Status: "connected"}}
			s.state.Tools = []Tool{{ConnectionID: "atlassian", Name: "getJiraIssue"}}
			switch action {
			case "settings":
				_, _ = s.SaveSettings(s.Snapshot().Settings)
			case "demo":
				_, _ = s.LoadDemo()
			case "disconnect":
				_, _ = s.DisconnectAtlassian("atlassian")
			case "close":
				s.Close()
			}
			if s.sourceLogin("atlassian") != nil || len(s.Snapshot().SourceAuth) != 0 {
				t.Fatal("credentials survived lifecycle boundary")
			}
			if action != "close" && len(s.Snapshot().Tools) != 0 {
				t.Fatal("old inventory survived logout")
			}
		})
	}
}
