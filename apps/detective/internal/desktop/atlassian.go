package desktop

import (
	"context"
	"errors"

	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/sourcemcp"
)

// SourceAuthView contains only ephemeral login UI state, never credentials.
type SourceAuthView struct {
	Status           string `json:"status"`
	AuthorizationURL string `json:"authorizationURL,omitempty"`
}

// BeginAtlassianLogin starts explicit OAuth and returns the URL for the user to
// open. The owned operation remains cancellable while waiting for the callback.
func (s *Service) BeginAtlassianLogin(ctx context.Context, id string) (State, error) {
	ctx, done, err := s.begin(ctx, "Atlassian OAuth 登入")
	if err != nil {
		return s.Snapshot(), err
	}
	config, err := s.sourceConnection(id)
	if err != nil || config.Transport != "atlassian-oauth" || sourcemcp.Validate(config) != nil {
		return s.finish(done, errors.New("Atlassian connection unavailable"), "請先套用實際模式、Atlassian OAuth 連線與工具 allowlist。")
	}
	s.mu.Lock()
	s.forgetSourceLoginLocked(id)
	s.mu.Unlock()
	login, err := s.beginAtlassian(ctx)
	if err != nil {
		return s.finish(done, err, "無法建立 Atlassian OAuth 登入；請確認網路與組織的 MCP／localhost callback 設定。")
	}
	s.mu.Lock()
	if s.state.SourceAuth == nil {
		s.state.SourceAuth = make(map[string]SourceAuthView)
	}
	s.state.SourceAuth[id] = SourceAuthView{Status: "waiting", AuthorizationURL: login.URL()}
	s.mu.Unlock()
	go func() {
		defer done()
		session, err := login.Wait(ctx)
		s.mu.Lock()
		if err == nil && ctx.Err() != nil {
			session.Forget()
			err = ctx.Err()
		}
		if err != nil {
			s.state.SourceAuth[id] = SourceAuthView{Status: "signed_out"}
		} else {
			if s.oauthSessions == nil {
				s.oauthSessions = make(map[string]*sourcemcp.OAuthSession)
			}
			s.oauthSessions[id] = session
			s.state.SourceAuth[id] = SourceAuthView{Status: "connected"}
		}
		s.mu.Unlock()
		detail := "Atlassian 登入完成；現在可取得工具清單。尚未讀取來源。"
		if err != nil {
			detail = "Atlassian 登入未完成、已取消或逾時；可重新登入。尚未讀取來源。"
		}
		_, _ = s.finish(done, err, detail)
	}()
	return s.Snapshot(), nil
}

// DisconnectAtlassian discards local credentials and invalidates discovered tools.
// It does not claim to revoke the grant at Atlassian.
func (s *Service) DisconnectAtlassian(id string) (State, error) {
	_, done, err := s.begin(context.Background(), "Atlassian 本機登出")
	if err != nil {
		return s.Snapshot(), err
	}
	defer done()
	s.mu.Lock()
	s.forgetSourceLoginLocked(id)
	s.mu.Unlock()
	return s.finish(done, nil, "已清除本次登入與工具清單；Atlassian 帳戶端的授權可另行撤銷。")
}

func (s *Service) forgetSourceLoginLocked(id string) {
	if session := s.oauthSessions[id]; session != nil {
		session.Forget()
		delete(s.oauthSessions, id)
	}
	delete(s.state.SourceAuth, id)
	retained := []Tool{}
	for _, tool := range s.state.Tools {
		if tool.ConnectionID != id {
			retained = append(retained, tool)
		}
	}
	s.state.Tools = retained
	s.state.ToolAdvice = nil
}

func (s *Service) forgetAllSourceLoginsLocked() {
	for _, session := range s.oauthSessions {
		session.Forget()
	}
	s.oauthSessions = nil
	s.state.SourceAuth = nil
}

func (s *Service) sourceLogin(id string) *sourcemcp.OAuthSession {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.oauthSessions[id]
}

func (s *Service) sourceAuthFailure(id string, err error) {
	if !errors.Is(err, sourcemcp.ErrLoginRequired) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.forgetSourceLoginLocked(id)
}
