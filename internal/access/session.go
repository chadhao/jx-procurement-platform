// Package access 实现飞书免登会话与角色解析（接入层）。
//
// 纪律：未映射角色的 open_id 默认拒绝进入业务页（deny by default，UC-01/A2、TC-32）；
// 角色每请求实时解析，不缓存决策（TC-11 即时生效）。
package access

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"sync"
	"time"
)

// CookieName 会话 Cookie 名（HttpOnly / Secure / SameSite=Lax，架构 §1.2）。
const CookieName = "jx_session"

// Session 服务端会话（角色不放入会话，仅存 open_id）。
type Session struct {
	ID       string
	OpenID   string
	IssuedAt time.Time
}

// Store 内存会话存储（单实例、数据量小；重启后需重新登录）。
type Store struct {
	mu         sync.RWMutex
	sessions   map[string]Session
	signingKey []byte
	ttl        time.Duration
	now        func() time.Time
}

// NewStore 构造会话存储。signingKey 为空时使用占位密钥（仅开发模式应如此）。
func NewStore(signingKey string, ttl time.Duration) *Store {
	if ttl <= 0 {
		ttl = 8 * time.Hour
	}
	return &Store{
		sessions:   map[string]Session{},
		signingKey: []byte(signingKey),
		ttl:        ttl,
		now:        func() time.Time { return time.Now().UTC() },
	}
}

// Establish 为 open_id 建立会话，返回签过名的 Cookie 值。
func (s *Store) Establish(openID string) string {
	id := newSessionID()
	s.mu.Lock()
	s.sessions[id] = Session{ID: id, OpenID: openID, IssuedAt: s.now()}
	s.mu.Unlock()
	return s.sign(id)
}

// Resolve 校验 Cookie 值并返回会话；签名不符或过期返回 false。
func (s *Store) Resolve(cookieValue string) (Session, bool) {
	id, ok := s.verify(cookieValue)
	if !ok {
		return Session{}, false
	}
	s.mu.RLock()
	sess, found := s.sessions[id]
	s.mu.RUnlock()
	if !found {
		return Session{}, false
	}
	if s.now().Sub(sess.IssuedAt) > s.ttl {
		s.Destroy(id)
		return Session{}, false
	}
	return sess, true
}

// Destroy 销毁会话。
func (s *Store) Destroy(id string) {
	s.mu.Lock()
	delete(s.sessions, id)
	s.mu.Unlock()
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
