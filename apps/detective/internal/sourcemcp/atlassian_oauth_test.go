package sourcemcp

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

type oauthFixture struct {
	mu        sync.Mutex
	mode      string
	redirect  string
	challenge string
	forms     []url.Values
	rpc       []string
	tokens    []string
}

type oauthRoute struct {
	target string
	base   http.RoundTripper
}

func (r oauthRoute) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	u := *req.URL
	clone.URL = &u
	target, _ := url.Parse(r.target)
	clone.URL.Scheme, clone.URL.Host = target.Scheme, target.Host
	return r.base.RoundTrip(clone)
}

func newOAuthFixture(t *testing.T, mode string) (*oauthFixture, *http.Client) {
	t.Helper()
	f := &oauthFixture{mode: mode}
	server := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(server.Close)
	client := &http.Client{Transport: oauthRoute{target: server.URL, base: server.Client().Transport}, Timeout: 3 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("redirect blocked") }}
	return f, client
}

func (f *oauthFixture) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	var result any
	switch r.URL.Path {
	case "/.well-known/oauth-protected-resource/v2/mcp":
		issuer := "https://auth.atlassian.com/synthetic"
		if f.mode == "foreign-issuer" {
			issuer = "https://untrusted.invalid/synthetic"
		}
		result = map[string]any{"resource": AtlassianEndpoint, "authorization_servers": []string{issuer}, "scopes_supported": strings.Fields(atlassianScopes)}
	case "/.well-known/oauth-authorization-server/synthetic":
		challenge := []string{"S256"}
		if f.mode == "no-pkce" {
			challenge = []string{"plain"}
		}
		tokenEndpoint := "https://auth.atlassian.com/oauth/token"
		if f.mode == "foreign-token" {
			tokenEndpoint = "https://untrusted.invalid/token"
		}
		result = oauthMetadata{Issuer: "https://auth.atlassian.com/synthetic", AuthorizationEndpoint: "https://auth.atlassian.com/authorize", TokenEndpoint: tokenEndpoint, RegistrationEndpoint: "https://auth.atlassian.com/register", CodeChallengeMethods: challenge, TokenAuthMethods: []string{"none"}}
	case "/register":
		var request struct {
			RedirectURIs []string `json:"redirect_uris"`
			Scope        string   `json:"scope"`
			Auth         string   `json:"token_endpoint_auth_method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&request)
		if len(request.RedirectURIs) != 1 || request.Scope != atlassianScopes || request.Auth != "none" {
			http.Error(w, "bad registration", 400)
			return
		}
		f.redirect = request.RedirectURIs[0]
		result = map[string]any{"client_id": "synthetic-client", "token_endpoint_auth_method": "none", "redirect_uris": request.RedirectURIs}
	case "/oauth/token":
		_ = r.ParseForm()
		f.forms = append(f.forms, r.PostForm)
		if r.PostForm.Get("resource") != AtlassianEndpoint || r.PostForm.Get("client_id") != "synthetic-client" {
			http.Error(w, "bad binding", 400)
			return
		}
		if r.PostForm.Get("grant_type") == "authorization_code" {
			sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
			if r.PostForm.Get("redirect_uri") != f.redirect || r.PostForm.Get("code") != "synthetic-code" || base64.RawURLEncoding.EncodeToString(sum[:]) != f.challenge {
				http.Error(w, "bad PKCE", 400)
				return
			}
		} else if f.mode == "refresh-failure" {
			http.Error(w, "synthetic-sensitive-error", 400)
			return
		}
		token := oauthToken{AccessToken: "synthetic-access", RefreshToken: "synthetic-refresh", TokenType: "Bearer", ExpiresIn: 3600, Scope: atlassianScopes}
		if len(f.forms) > 1 {
			token.AccessToken = "synthetic-access-rotated"
			token.RefreshToken = "synthetic-refresh-rotated"
		}
		if f.mode == "extra-scope" {
			token.Scope += " write:jira:agent-interface"
		}
		result = token
	case "/v2/mcp":
		f.tokens = append(f.tokens, r.Header.Get("Authorization"))
		if f.mode == "unauthorized" {
			http.Error(w, "synthetic-sensitive-error", 401)
			return
		}
		if f.mode == "redirect" {
			http.Redirect(w, r, "https://untrusted.invalid/steal", 307)
			return
		}
		if r.Method == http.MethodDelete {
			w.WriteHeader(204)
			return
		}
		var request rpcRequest
		_ = json.NewDecoder(r.Body).Decode(&request)
		f.rpc = append(f.rpc, request.Method)
		if request.ID == 0 {
			w.WriteHeader(202)
			return
		}
		if request.Method == "initialize" {
			w.Header().Set("Mcp-Session-Id", "synthetic-session")
		}
		result = map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": fixtureReply(request.Method, "")}
	default:
		http.NotFound(w, r)
		return
	}
	_ = json.NewEncoder(w).Encode(result)
}

func authorizeFixture(t *testing.T, f *oauthFixture, client *http.Client) *OAuthSession {
	t.Helper()
	login, err := beginAtlassianOAuth(t.Context(), client)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(login.URL())
	q := u.Query()
	if q.Get("resource") != AtlassianEndpoint || q.Get("scope") != atlassianScopes || q.Get("code_challenge_method") != "S256" || len(q.Get("state")) < 32 {
		t.Fatal("authorization binding missing")
	}
	f.mu.Lock()
	f.challenge = q.Get("code_challenge")
	f.mu.Unlock()
	type outcome struct {
		session *OAuthSession
		err     error
	}
	done := make(chan outcome, 1)
	go func() { session, err := login.Wait(t.Context()); done <- outcome{session, err} }()
	// A wrong state must not consume the callback or exchange a code.
	for _, state := range []string{"wrong-state", q.Get("state")} {
		response, err := http.Get(login.redirectURI + "?" + url.Values{"state": {state}, "code": {"synthetic-code"}}.Encode())
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, response.Body)
		_ = response.Body.Close()
		if state == "wrong-state" && response.StatusCode != 400 {
			t.Fatal("wrong state accepted")
		}
	}
	result := <-done
	if result.err != nil {
		t.Fatal(result.err)
	}
	t.Cleanup(result.session.Forget)
	if _, err := http.Get(login.redirectURI); err == nil {
		t.Fatal("callback listener leaked")
	}
	return result.session
}

func TestAtlassianOAuthEndToEndAndRefresh(t *testing.T) {
	f, client := newOAuthFixture(t, "")
	auth := authorizeFixture(t, f, client)
	config := Config{ID: "atlassian", Name: "Synthetic Atlassian", Transport: "atlassian-oauth", URL: AtlassianEndpoint, AllowedTools: []string{"read_document"}}
	tools, err := auth.Discover(t.Context(), config)
	if err != nil || len(tools) != 1 {
		t.Fatalf("discover: %v", err)
	}
	auth.mu.Lock()
	auth.token.expires = time.Now().Add(-time.Second)
	auth.mu.Unlock()
	result, err := auth.Call(t.Context(), config, tools[0], `{"id":"one"}`)
	if err != nil || result.RawJSON == "" {
		t.Fatalf("call: %v", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.forms) != 2 || f.forms[1].Get("refresh_token") != "synthetic-refresh" {
		t.Fatal("refresh missing")
	}
	calls := 0
	for _, method := range f.rpc {
		if method == "tools/call" {
			calls++
		}
	}
	if calls != 1 {
		t.Fatalf("calls = %d", calls)
	}
	for _, header := range f.tokens {
		if header != "Bearer synthetic-access" && header != "Bearer synthetic-access-rotated" {
			t.Fatal("request missing authorization")
		}
	}
	if auth.token.RefreshToken != "synthetic-refresh-rotated" {
		t.Fatal("rotated refresh token lost")
	}
	config.oauth = auth
	raw, _ := json.Marshal(config)
	if strings.Contains(string(raw), "synthetic-access") || strings.Contains(string(raw), "synthetic-refresh") {
		t.Fatal("credential serialized")
	}
	if string(mustJSON(auth)) != "{}" {
		t.Fatal("OAuth state serialized")
	}
}

func TestAtlassianRejectsMetadataOutsideBoundary(t *testing.T) {
	for _, mode := range []string{"foreign-issuer", "foreign-token", "no-pkce"} {
		t.Run(mode, func(t *testing.T) {
			_, client := newOAuthFixture(t, mode)
			if login, err := beginAtlassianOAuth(t.Context(), client); err == nil {
				login.Close()
				t.Fatal("unsupported metadata accepted")
			}
		})
	}
	for _, raw := range []string{"http://auth.atlassian.com/token", "https://auth.atlassian.com.evil.invalid/token", "https://auth.atlassian.com:443/token", "https://user@auth.atlassian.com/token", "https://auth.atlassian.com/token?x=y", "https://auth.atlassian.com/token#x", "https://auth.atlassian.com/a/../token"} {
		if atlassianAuthURL(raw) {
			t.Fatal("unsafe auth URL accepted")
		}
	}
}

func TestAtlassianCancelClosesCallback(t *testing.T) {
	_, client := newOAuthFixture(t, "")
	login, err := beginAtlassianOAuth(t.Context(), client)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := login.Wait(ctx); err == nil {
		t.Fatal("cancelled login succeeded")
	}
	if _, err := http.Get(login.redirectURI); err == nil {
		t.Fatal("callback listener leaked")
	}
}

func TestAtlassianAuthFailureDoesNotReplay(t *testing.T) {
	for _, mode := range []string{"unauthorized", "redirect", "refresh-failure"} {
		t.Run(mode, func(t *testing.T) {
			f, client := newOAuthFixture(t, mode)
			auth := authorizeFixture(t, f, client)
			if mode == "refresh-failure" {
				auth.token.expires = time.Now().Add(-time.Second)
			}
			c := Config{ID: "remote", Name: "Remote", Transport: "atlassian-oauth", URL: AtlassianEndpoint, AllowedTools: []string{"read_document"}}
			if _, err := auth.Discover(t.Context(), c); err == nil || strings.Contains(err.Error(), "synthetic-sensitive") {
				t.Fatal("auth error accepted or leaked")
			}
			if mode != "redirect" {
				if _, err := auth.Discover(t.Context(), c); !errors.Is(err, ErrLoginRequired) {
					t.Fatal("login not invalidated")
				}
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if len(f.tokens) > 1 || len(f.forms) > 2 {
				t.Fatal("failed operation replayed")
			}
		})
	}
}

func TestAtlassianEndpointIsExplicitAndRequiresLogin(t *testing.T) {
	c := Config{ID: "remote", Name: "Remote", Transport: "atlassian-oauth", URL: AtlassianEndpoint, AllowedTools: []string{"read_document"}}
	if _, err := Discover(t.Context(), c); !errors.Is(err, ErrLoginRequired) {
		t.Fatal("unauthenticated request allowed")
	}
	for _, raw := range []string{AtlassianEndpoint + "?tools=all", AtlassianEndpoint + "/", "http://mcp.atlassian.com/v2/mcp", "https://untrusted.invalid/mcp"} {
		c.URL = raw
		if Validate(c) == nil {
			t.Fatal("nonofficial endpoint accepted")
		}
	}
	c.URL = AtlassianEndpoint
	c.Transport = "streamable-http"
	if Validate(c) == nil {
		t.Fatal("local transport silently gained outbound access")
	}
}

func TestAtlassianCallbackValidationAndDecline(t *testing.T) {
	f, client := newOAuthFixture(t, "")
	login, err := beginAtlassianOAuth(t.Context(), client)
	if err != nil {
		t.Fatal(err)
	}
	completed := make(chan error, 1)
	go func() { _, err := login.Wait(t.Context()); completed <- err }()
	for _, query := range []url.Values{
		{"state": {login.state, login.state}, "code": {"synthetic-code"}},
		{"state": {login.state}, "code": {"one", "two"}},
		{"state": {login.state}, "code": {"synthetic-code"}, "iss": {"https://untrusted.invalid"}},
		{"state": {login.state}, "code": {"synthetic-code"}, "error": {"denied"}},
	} {
		response, err := http.Get(login.redirectURI + "?" + query.Encode())
		if err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
		if response.StatusCode != 400 {
			t.Fatal("malformed callback accepted")
		}
	}
	response, err := http.Get(login.redirectURI + "?" + url.Values{"state": {login.state}, "error": {"access_denied"}}.Encode())
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if err := <-completed; err == nil {
		t.Fatal("declined login succeeded")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.forms) != 0 {
		t.Fatal("invalid/declined callback exchanged token")
	}
}

func TestAtlassianCallbackResponseCompletes(t *testing.T) {
	for _, outcome := range []string{"approved", "declined"} {
		t.Run(outcome, func(t *testing.T) {
			f, client := newOAuthFixture(t, "")
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			login, err := beginAtlassianOAuth(ctx, client)
			if err != nil {
				t.Fatal(err)
			}
			defer login.Close()
			authorization, _ := url.Parse(login.URL())
			f.challenge = authorization.Query().Get("code_challenge")
			completed := make(chan error, 1)
			go func() {
				session, err := login.Wait(ctx)
				if session != nil {
					session.Forget()
				}
				completed <- err
			}()
			// A fresh connection cannot hide a truncated reply behind an HTTP retry.
			transport := &http.Transport{DisableKeepAlives: true}
			defer transport.CloseIdleConnections()
			callbackClient := &http.Client{Transport: transport, Timeout: time.Second}
			query := url.Values{"state": {login.state}}
			if outcome == "approved" {
				query.Set("code", "synthetic-code")
			} else {
				query.Set("error", "access_denied")
			}
			response, err := callbackClient.Get(login.redirectURI + "?" + query.Encode())
			if err != nil {
				t.Fatalf("callback response unavailable: %v", err)
			}
			body, err := io.ReadAll(response.Body)
			_ = response.Body.Close()
			if err != nil || response.StatusCode != http.StatusOK || string(body) != "Return to Detective to check login status. You may close this page." {
				t.Fatalf("callback response incomplete: status=%d, read error=%v", response.StatusCode, err)
			}
			select {
			case err := <-completed:
				if (err != nil) != (outcome == "declined") {
					t.Fatalf("unexpected login outcome: %v", err)
				}
			case <-ctx.Done():
				t.Fatal("callback waiter did not stop")
			}
			if _, err := callbackClient.Get(login.redirectURI); err == nil {
				t.Fatal("callback listener leaked")
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			wantExchanges := 0
			if outcome == "approved" {
				wantExchanges = 1
			}
			if len(f.forms) != wantExchanges {
				t.Fatal("unexpected token exchange count")
			}
		})
	}
}

func TestAtlassianRejectsBroaderTokenScopes(t *testing.T) {
	f, client := newOAuthFixture(t, "extra-scope")
	login, err := beginAtlassianOAuth(t.Context(), client)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(login.URL())
	f.challenge = u.Query().Get("code_challenge")
	completed := make(chan error, 1)
	go func() { _, err := login.Wait(t.Context()); completed <- err }()
	response, err := http.Get(login.redirectURI + "?" + url.Values{"state": {login.state}, "code": {"synthetic-code"}}.Encode())
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if err := <-completed; err == nil {
		t.Fatal("unrequested write scope accepted")
	}
}
