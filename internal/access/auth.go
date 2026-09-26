package access

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"github.com/chadhao/jx-procurement-platform/internal/observ"
	"github.com/chadhao/jx-procurement-platform/internal/platform/feishu"
	"github.com/chadhao/jx-procurement-platform/internal/store"
)

// ErrRoleNotMapped 表示 open_id 未在 t_user_role 映射（默认拒绝，TC-32）。
var ErrRoleNotMapped = errors.New("access: open_id 未映射角色（默认拒绝）")

// Authenticator 飞书免登 + 会话 + 角色解析。
type Authenticator struct {
	db       *store.DB
	sessions *Store
	oauth    feishu.OAuthExchange
	devMode  bool
	log      *slog.Logger
}

// NewAuthenticator 构造鉴权器。oauth 可为 nil（未配置凭据时仅开发模式可用）。
func NewAuthenticator(db *store.DB, sessions *Store, oauth feishu.OAuthExchange, devMode bool, log *slog.Logger) *Authenticator {
	if log == nil {
		log = observ.NewLogger("info", nil)
	}
	return &Authenticator{db: db, sessions: sessions, oauth: oauth, devMode: devMode,
		log: observ.WithComponent(log, "access")}
}

// Exchange 用免登 code 换取用户身份；开发模式下允许直接用 devOpenID 跳过飞书调用。
func (a *Authenticator) Exchange(ctx context.Context, code, devOpenID string) (feishu.FeishuIdentity, error) {
	if a.devMode && strings.TrimSpace(devOpenID) != "" {
		return feishu.FeishuIdentity{OpenID: strings.TrimSpace(devOpenID)}, nil
	}
	if a.oauth == nil {
		return feishu.FeishuIdentity{}, errors.New("access: 未配置飞书免登客户端（缺少凭据）")
	}
	return a.oauth.ExchangeCode(ctx, code)
}

// ResolveRole 实时解析角色（不缓存决策，保证权限即时生效，TC-11）。
func (a *Authenticator) ResolveRole(ctx context.Context, openID string) (*store.UserRole, error) {
	ur, err := a.db.GetUserRole(ctx, openID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, ErrRoleNotMapped
		}
		return nil, err
	}
	return ur, nil
}

// Establish 建立会话，返回签过名的 Cookie 值。
func (a *Authenticator) Establish(openID string) string { return a.sessions.Establish(openID) }

// ResolveSession 校验并解析会话 Cookie。
func (a *Authenticator) ResolveSession(cookieValue string) (Session, bool) {
	return a.sessions.Resolve(cookieValue)
}

// Logout 销毁会话。
func (a *Authenticator) Logout(sessionID string) { a.sessions.Destroy(sessionID) }

// DevMode 是否开发模式。
func (a *Authenticator) DevMode() bool { return a.devMode }
