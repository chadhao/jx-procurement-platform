// Package access 实现飞书免登会话与角色解析（接入层）。
//
// 纪律：未映射角色的 open_id 默认拒绝进入业务页（deny by default，UC-01/A2、TC-32）；
// 角色每请求实时解析，不缓存决策（TC-11 即时生效）。
package access

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"time"

	"github.com/chadhao/jx-procurement-platform/internal/store"
)

// CookieName 会话 Cookie 名（HttpOnly / Secure / SameSite=Lax，架构 §1.2）。
const CookieName = "jx_session"

// DefaultTTL 会话缺省 TTL（N-077：JX_SESSION_TTL 缺省 12h —— 覆盖一个工作日；
// 未配置/非法时 NewStore 与 cookie Max-Age 均回落此值，缺省可用）。
const DefaultTTL = 12 * time.Hour

// renewAfter 滑动续期节流窗（N-077：距上次续期 < 5 分钟不写库、不重发 cookie，
// 避免每请求一次写）。
const renewAfter = 5 * time.Minute

// Session 服务端会话（角色不放入会话，仅存 open_id）。
type Session struct {
	ID       string
	OpenID   string
	IssuedAt time.Time
	// ExpiresAt 当前过期时刻（滑动续期后刷新；N-077）。
	ExpiresAt time.Time
	// Renewed 本次 Resolve 是否执行了续期（HTTP 层据此**同步重发 cookie Max-Age**，
	// N-077 判据③ —— 只在写库时为 true，节流窗内为 false）。
	Renewed bool
}

// Store 会话存储（★ N-077 起**落库 t_session**：与原内存 map 对外语义同构 ——
// Establish/Resolve/Destroy 不变，仅换后端 ⇒ 重启后旧 cookie 仍有效、Destroy
// 落库即持久生效）。单实例、表极小（5–10 人协作，数十行）。
type Store struct {
	db         *store.DB
	signingKey []byte
	ttl        time.Duration
	now        func() time.Time
}

// NewStore 构造会话存储。signingKey 为空时使用占位密钥（仅开发模式应如此）；
// ttl <= 0 ⇒ DefaultTTL（缺省可用）。
func NewStore(db *store.DB, signingKey string, ttl time.Duration) *Store {
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	return &Store{
		db:         db,
		signingKey: []byte(signingKey),
		ttl:        ttl,
		now:        func() time.Time { return time.Now().UTC() },
	}
}

// Establish 为 open_id 建立会话，返回签过名的 Cookie 值。
func (s *Store) Establish(openID string) string {
	id := newSessionID()
	now := s.now()
	ctx := context.Background()
	// 顺带清理已过期行（表极小、幂等 —— 未再 Resolve 的废弃会话不残留）。
	_, _ = s.db.PurgeExpiredSessions(ctx, now)
	if err := s.db.InsertSession(ctx, store.SessionRow{
		ID: id, OpenID: openID,
		IssuedAt: now, ExpiresAt: now.Add(s.ttl), UpdatedAt: now,
	}); err != nil {
		// 落库失败不得放行（会话建立必须可见失败，不静默退回内存）。
		return ""
	}
	return s.sign(id)
}

// Resolve 校验 Cookie 值并返回会话；签名不符、查无、过期返回 false。
//
// ★ 滑动续期（N-077 判据②）：通过且距上次续期（updated_at）≥ renewAfter ⇒
//
//	刷新 expires_at = now+ttl 并置 sess.Renewed=true（HTTP 层据此重发 cookie，
//	判据③ 两侧同批）；节流窗内不写库。
func (s *Store) Resolve(cookieValue string) (Session, bool) {
	id, ok := s.verify(cookieValue)
	if !ok {
		return Session{}, false
	}
	ctx := context.Background()
	row, err := s.db.GetSession(ctx, id)
	if err != nil {
		return Session{}, false // 查无 / 读错均拒（deny by default）
	}
	now := s.now()
	if !now.Before(row.ExpiresAt) {
		_ = s.db.DeleteSession(ctx, id) // 过期即删（顺带清理）
		return Session{}, false
	}
	sess := Session{
		ID: row.ID, OpenID: row.OpenID,
		IssuedAt: row.IssuedAt, ExpiresAt: row.ExpiresAt,
	}
	if now.Sub(row.UpdatedAt) >= renewAfter {
		sess.ExpiresAt = now.Add(s.ttl)
		if err := s.db.TouchSession(ctx, id, sess.ExpiresAt, now); err != nil {
			// 续期写失败：会话本身仍有效（读路径已通过），下次请求再试；不据此拒绝。
			return sess, true
		}
		sess.Renewed = true
	}
	return sess, true
}

// Destroy 销毁会话（落库删除 ⇒ 重启后仍生效，N-077 判据⑤）。
func (s *Store) Destroy(id string) {
	_ = s.db.DeleteSession(context.Background(), id)
}

func (s *Store) sign(id string) string {
	mac := hmac.New(sha256.New, s.signingKey)
	mac.Write([]byte(id))
	return id + "." + hex.EncodeToString(mac.Sum(nil))
}

func (s *Store) verify(value string) (string, bool) {
	for i := 0; i < len(value); i++ {
		if value[i] == '.' {
			id, sig := value[:i], value[i+1:]
			mac := hmac.New(sha256.New, s.signingKey)
			mac.Write([]byte(id))
			expect := hex.EncodeToString(mac.Sum(nil))
			if hmac.Equal([]byte(sig), []byte(expect)) {
				return id, true
			}
			return "", false
		}
	}
	return "", false
}

func newSessionID() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		// 退化：使用时间戳（仅极端情况）。
		return hex.EncodeToString([]byte(time.Now().UTC().Format(time.RFC3339Nano)))
	}
	return hex.EncodeToString(buf)
}
