package sourcemcp

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"
)

// AtlassianEndpoint is the official Rovo MCP Streamable HTTP resource.
const AtlassianEndpoint = "https://mcp.atlassian.com/v2/mcp"

const atlassianResourceMetadata = "https://mcp.atlassian.com/.well-known/oauth-protected-resource/v2/mcp"
const atlassianScopes = "read:me read:account offline_access read:jira:agent-interface search:jira:agent-interface read:confluence:agent-interface search:confluence:agent-interface"

// ErrLoginRequired requires an explicit new login, never a replay of a tool call.
var ErrLoginRequired = errors.New("Atlassian login required")

// AtlassianAuthorization owns a short-lived loopback callback. The caller must
// call Wait or Close. No browser is opened and no token is written to disk.
type AtlassianAuthorization struct {
	authorizationURL string
	redirectURI      string
	state            string
	verifier         string
	clientID         string
	metadata         oauthMetadata
	client           *http.Client
	listener         net.Listener
}

// URL returns the consent URL for the operator to open manually.
func (a *AtlassianAuthorization) URL() string { return a.authorizationURL }

// Close releases the callback listener, including when login is cancelled.
func (a *AtlassianAuthorization) Close() { _ = a.listener.Close() }

// OAuthSession holds credentials only in memory, outside settings and receipts.
// It is bound to one login; refreshing it never starts a source tool call.
type OAuthSession struct {
	mu            sync.Mutex
	client        *http.Client
	clientID      string
	tokenEndpoint string
	token         oauthToken
}

func (*OAuthSession) String() string { return "[OAuth session]" }

type oauthMetadata struct {
	Issuer                string   `json:"issuer"`
	AuthorizationEndpoint string   `json:"authorization_endpoint"`
	TokenEndpoint         string   `json:"token_endpoint"`
	RegistrationEndpoint  string   `json:"registration_endpoint"`
	CodeChallengeMethods  []string `json:"code_challenge_methods_supported"`
	TokenAuthMethods      []string `json:"token_endpoint_auth_methods_supported"`
}

type oauthToken struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int64  `json:"expires_in"`
	Scope        string `json:"scope"`
	expires      time.Time
}

// BeginAtlassianOAuth discovers the official authorization server and registers
// a public PKCE client. Only an explicit login action should call it.
func BeginAtlassianOAuth(ctx context.Context) (*AtlassianAuthorization, error) {
	transport := &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
		DisableKeepAlives: true, ResponseHeaderTimeout: 10 * time.Second, MaxResponseHeaderBytes: 16 << 10}
	client := &http.Client{Transport: transport, Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
		return errors.New("OAuth redirects are not permitted")
	}}
	return beginAtlassianOAuth(ctx, client)
}

func beginAtlassianOAuth(ctx context.Context, client *http.Client) (*AtlassianAuthorization, error) {
	var resource struct {
		Resource             string   `json:"resource"`
		AuthorizationServers []string `json:"authorization_servers"`
		Scopes               []string `json:"scopes_supported"`
	}
	if err := oauthJSON(ctx, client, http.MethodGet, atlassianResourceMetadata, "", nil, &resource); err != nil {
		return nil, err
	}
	if resource.Resource != AtlassianEndpoint || len(resource.AuthorizationServers) != 1 || !atlassianAuthURL(resource.AuthorizationServers[0]) {
		return nil, errors.New("Atlassian resource metadata is unsupported")
	}
	for _, scope := range strings.Fields(atlassianScopes) {
		if !slices.Contains(resource.Scopes, scope) {
			return nil, errors.New("Atlassian read scopes are unavailable")
		}
	}
	issuer, _ := url.Parse(resource.AuthorizationServers[0])
	metadataURL := "https://auth.atlassian.com/.well-known/oauth-authorization-server" + issuer.Path
	var metadata oauthMetadata
	if err := oauthJSON(ctx, client, http.MethodGet, metadataURL, "", nil, &metadata); err != nil {
		return nil, err
	}
	if metadata.Issuer != resource.AuthorizationServers[0] || !atlassianAuthURL(metadata.AuthorizationEndpoint) ||
		!atlassianAuthURL(metadata.TokenEndpoint) || !atlassianAuthURL(metadata.RegistrationEndpoint) ||
		!slices.Contains(metadata.CodeChallengeMethods, "S256") || !slices.Contains(metadata.TokenAuthMethods, "none") {
		return nil, errors.New("Atlassian authorization metadata is unsupported")
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return nil, errors.New("cannot listen for Atlassian login callback")
	}
	a := &AtlassianAuthorization{client: client, listener: listener, metadata: metadata,
		redirectURI: "http://" + listener.Addr().String() + "/oauth/callback", state: oauthNonce(), verifier: oauthNonce()}
	registration, _ := json.Marshal(map[string]any{
		"client_name": "AHE Detective", "redirect_uris": []string{a.redirectURI}, "token_endpoint_auth_method": "none",
		"grant_types": []string{"authorization_code", "refresh_token"}, "response_types": []string{"code"}, "scope": atlassianScopes,
	})
	var registered struct {
		ClientID     string   `json:"client_id"`
		AuthMethod   string   `json:"token_endpoint_auth_method"`
		RedirectURIs []string `json:"redirect_uris"`
	}
	if err := oauthJSON(ctx, client, http.MethodPost, metadata.RegistrationEndpoint, "application/json", registration, &registered); err != nil {
		a.Close()
		return nil, err
	}
	if !validText(registered.ClientID, 2048) || registered.ClientID == "" || registered.AuthMethod != "none" || !slices.Equal(registered.RedirectURIs, []string{a.redirectURI}) {
		a.Close()
		return nil, errors.New("Atlassian public client registration is unsupported")
	}
	a.clientID = registered.ClientID
	challenge := sha256.Sum256([]byte(a.verifier))
	q := url.Values{"response_type": {"code"}, "client_id": {a.clientID}, "redirect_uri": {a.redirectURI},
		"scope": {atlassianScopes}, "state": {a.state}, "code_challenge": {base64.RawURLEncoding.EncodeToString(challenge[:])},
		"code_challenge_method": {"S256"}, "resource": {AtlassianEndpoint}}
	a.authorizationURL = metadata.AuthorizationEndpoint + "?" + q.Encode()
	return a, nil
}

func atlassianAuthURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.Host == "auth.atlassian.com" && u.User == nil && u.RawQuery == "" && !u.ForceQuery && u.Fragment == "" && u.RawPath == "" && u.Opaque == "" && strings.HasPrefix(u.Path, "/") && !strings.Contains(u.Path, "..")
}

func oauthNonce() string {
	var b [32]byte
	_, _ = rand.Read(b[:]) // crypto/rand.Read never returns an error in Go 1.27.
	return base64.RawURLEncoding.EncodeToString(b[:])
}

// Wait receives one state-bound callback, exchanges the PKCE code, and closes
// the listener on completion, timeout or cancellation. Callback data is never logged.
func (a *AtlassianAuthorization) Wait(ctx context.Context) (*OAuthSession, error) {
	defer a.Close()
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	type callback struct {
		code   string
		denied bool
	}
	received := make(chan callback, 1)
	var acceptOnce sync.Once
	server := &http.Server{ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 5 * time.Second, MaxHeaderBytes: 16 << 10}
	server.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'none'")
		q, err := url.ParseQuery(r.URL.RawQuery)
		if r.Method != http.MethodGet || r.Host != a.listener.Addr().String() || r.URL.Path != "/oauth/callback" || len(r.URL.RawQuery) > 16<<10 || err != nil || len(q["state"]) != 1 || subtle.ConstantTimeCompare([]byte(q.Get("state")), []byte(a.state)) != 1 {
			http.Error(w, "Invalid login callback.", http.StatusBadRequest)
			return
		}
		if len(q["iss"]) > 0 && (len(q["iss"]) != 1 || q.Get("iss") != a.metadata.Issuer) {
			http.Error(w, "Invalid login issuer.", http.StatusBadRequest)
			return
		}
		denied := len(q["error"]) == 1 && q.Get("error") != "" && len(q["code"]) == 0
		validCode := len(q["code"]) == 1 && q.Get("code") != "" && len(q["error"]) == 0
		if !denied && !validCode {
			http.Error(w, "Invalid login callback.", http.StatusBadRequest)
			return
		}
		accepted := false
		acceptOnce.Do(func() {
			received <- callback{code: q.Get("code"), denied: denied}
			accepted = true
		})
		if !accepted {
			http.Error(w, "Login callback already received.", http.StatusConflict)
			return
		}
		_, _ = io.WriteString(w, "Return to Detective to check login status. You may close this page.")
	})
	served := make(chan struct{})
	go func() {
		_ = server.Serve(a.listener) // Completion is handled below without logging callback data.
		close(served)
	}()
	defer func() { _ = server.Close(); <-served }()
	var result callback
	select {
	case <-ctx.Done():
		return nil, errors.New("Atlassian login cancelled or timed out")
	case result = <-received:
	case <-served:
		return nil, errors.New("Atlassian callback listener stopped")
	}
	if result.denied {
		return nil, errors.New("Atlassian login was declined")
	}
	form := url.Values{"grant_type": {"authorization_code"}, "code": {result.code}, "redirect_uri": {a.redirectURI}, "client_id": {a.clientID}, "code_verifier": {a.verifier}, "resource": {AtlassianEndpoint}}
	token, err := exchangeOAuthToken(ctx, a.client, a.metadata.TokenEndpoint, form)
	if err != nil {
		return nil, err
	}
	return &OAuthSession{client: a.client, clientID: a.clientID, tokenEndpoint: a.metadata.TokenEndpoint, token: token}, nil
}

func oauthJSON(ctx context.Context, client *http.Client, method, endpoint, contentType string, body []byte, target any) error {
	req, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(body))
	if err != nil {
		return errors.New("cannot create Atlassian OAuth request")
	}
	req.Header.Set("Accept", "application/json")
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := client.Do(req)
	if err != nil {
		return errors.New("Atlassian OAuth connection failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return errors.New("Atlassian OAuth request rejected; login or administrator configuration may be required")
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10+1))
	if err != nil || len(data) > 64<<10 || decodeJSON(data, target) != nil {
		return errors.New("Atlassian OAuth response is invalid")
	}
	return nil
}

func exchangeOAuthToken(ctx context.Context, client *http.Client, endpoint string, form url.Values) (oauthToken, error) {
	var token oauthToken
	if err := oauthJSON(ctx, client, http.MethodPost, endpoint, "application/x-www-form-urlencoded", []byte(form.Encode()), &token); err != nil {
		return oauthToken{}, err
	}
	if !strings.EqualFold(token.TokenType, "Bearer") || !validBearer(token.AccessToken) || token.ExpiresIn <= 0 || token.ExpiresIn > 365*24*3600 || len(token.RefreshToken) > 16<<10 {
		return oauthToken{}, errors.New("Atlassian OAuth token response is unsupported")
	}
	for _, scope := range strings.Fields(token.Scope) {
		if !slices.Contains(strings.Fields(atlassianScopes), scope) {
			return oauthToken{}, errors.New("Atlassian OAuth returned unrequested scopes")
		}
	}
	token.expires = time.Now().Add(time.Duration(token.ExpiresIn) * time.Second)
	return token, nil
}

func validBearer(token string) bool {
	if len(token) == 0 || len(token) > 16<<10 {
		return false
	}
	for _, c := range token {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("-._~+/=", c)) {
			return false
		}
	}
	return true
}

func (a *OAuthSession) bearer(ctx context.Context) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.token.AccessToken == "" {
		return "", ErrLoginRequired
	}
	if time.Until(a.token.expires) > 30*time.Second {
		return a.token.AccessToken, nil
	}
	if a.token.RefreshToken == "" {
		a.token = oauthToken{}
		return "", ErrLoginRequired
	}
	token, err := exchangeOAuthToken(ctx, a.client, a.tokenEndpoint, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {a.token.RefreshToken}, "client_id": {a.clientID}, "resource": {AtlassianEndpoint}})
	// Refresh failure is ambiguous with rotating tokens. Do not replay it.
	if err != nil {
		a.token = oauthToken{}
		return "", ErrLoginRequired
	}
	if token.RefreshToken == "" {
		token.RefreshToken = a.token.RefreshToken
	}
	a.token = token
	return token.AccessToken, nil
}

// Forget discards this process's credentials without claiming server revocation.
func (a *OAuthSession) Forget() { a.mu.Lock(); defer a.mu.Unlock(); a.token = oauthToken{} }

// Discover lists tools using this in-memory Atlassian login.
func (a *OAuthSession) Discover(ctx context.Context, c Config) ([]Tool, error) {
	if c.Transport != "atlassian-oauth" {
		return nil, ErrLoginRequired
	}
	c.oauth = a
	return Discover(ctx, c)
}

// Call executes a previously confirmed tool using this Atlassian login.
func (a *OAuthSession) Call(ctx context.Context, c Config, tool Tool, args string) (Result, error) {
	if c.Transport != "atlassian-oauth" {
		return Result{}, ErrLoginRequired
	}
	c.oauth = a
	return Call(ctx, c, tool, args)
}
