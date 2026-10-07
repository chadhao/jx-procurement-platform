package access

// 批 0 · A7：会话冒烟 —— 签发 / 解析 / 过期 / 篡改拒绝。
// N-077：后端落库 t_session —— 重启存续 / 滑动续期两侧 / 节流 / Destroy 持久。

import (
	"context"
	"testing"
	"time"

	"github.com/chadhao/jx-procurement-platform/internal/store/storetest"
)

func TestSessionEstablishResolve(t *testing.T) {
	s := NewStore(storetest.NewDB(t), "test-session-key-a7", time.Hour)
	tok := s.Establish("ou_user_1")
	if tok == "" {
		t.Fatal("Establish 返回空 token")
	}
	sess, ok := s.Resolve(tok)
	if !ok || sess.OpenID != "ou_user_1" {
		t.Fatalf("Resolve = %+v ok=%v", sess, ok)
	}
}

func TestSessionExpired(t *testing.T) {
	s := NewStore(storetest.NewDB(t), "test-session-key-a7", 10*time.Millisecond)
	tok := s.Establish("ou_user_1")
	time.Sleep(30 * time.Millisecond)
	if _, ok := s.Resolve(tok); ok {
		t.Error("过期 token 不应被接受")
	}
}

func TestSessionTamperedRejected(t *testing.T) {
	db := storetest.NewDB(t)
	s := NewStore(db, "test-session-key-a7", time.Hour)
	tok := s.Establish("ou_user_1")
	// 篡改签名段
	bad := tok[:len(tok)-2] + "xx"
	if _, ok := s.Resolve(bad); ok {
		t.Error("篡改 token 不应被接受")
	}
	// 换一把钥匙（伪造签名）
	other := NewStore(db, "other-key", time.Hour)
	if _, ok := other.Resolve(tok); ok {
		t.Error("异钥签名 token 不应被接受")
	}
}

// TestSessionSurvivesRestartN077 判据①（核心）：重启（新 Store 实例、同库）后
// 重启前建立的 cookie 仍有效。★ 改前实测（内存 map 后端）＝
// 「重启后旧会话失效（缺陷重现 —— N-077 根因① 内存 map）」，落库后转绿。
func TestSessionSurvivesRestartN077(t *testing.T) {
	db := storetest.NewDB(t)
	s1 := NewStore(db, "k-n077", DefaultTTL)
	tok := s1.Establish("ou_x")
	// 重启 = 新 Store 实例（进程内存清空、库不动）。
	s2 := NewStore(db, "k-n077", DefaultTTL)
	sess, ok := s2.Resolve(tok)
	if !ok || sess.OpenID != "ou_x" {
		t.Fatalf("重启后旧会话失效: ok=%v sess=%+v（N-077 判据① 不成立）", ok, sess)
	}
}

// TestSessionSlidingRenewalN077 判据②（两侧）：持续访问（7h/9h/11h 间隔）⇒ 不失效
// （每次续期把 expires_at 推到 now+ttl）；停止访问超过 TTL ⇒ 失效（防「永不过期」）。
// 注：节流窗 5 分钟远小于访问间隔 ⇒ 每次访问都真续期（写库）。
func TestSessionSlidingRenewalN077(t *testing.T) {
	s := NewStore(storetest.NewDB(t), "k-n077", DefaultTTL)
	base := time.Date(2026, 10, 7, 8, 0, 0, 0, time.UTC)
	now := base
	s.now = func() time.Time { return now }

	tok := s.Establish("ou_work") // 签发：expires = base+12h

	// 第 7 小时访问 ⇒ 续期（expires → 7h+12h）。
	now = base.Add(7 * time.Hour)
	sess, ok := s.Resolve(tok)
	if !ok || !sess.Renewed {
		t.Fatalf("第 7 小时: ok=%v renewed=%v（应通过并续期）", ok, sess.Renewed)
	}
	// 第 9、11 小时继续访问 ⇒ 各自续期、不失效。
	for _, h := range []int{9, 11} {
		now = base.Add(time.Duration(h) * time.Hour)
		if _, ok := s.Resolve(tok); !ok {
			t.Fatalf("第 %d 小时访问失效（滑动续期未生效）", h)
		}
	}
	// 第 13 小时：若第 7 小时那次续期**没写库**（等效「摘 Touch」），expires 仍= base+12h ⇒ 必失效。
	// 此处通过 ⇒ 续期真落库（与判据② 的鉴别点合一）。
	now = base.Add(13 * time.Hour)
	if _, ok := s.Resolve(tok); !ok {
		t.Fatal("第 13 小时失效 —— 续期未持久（Touch 未生效）")
	}
	// 停止访问超过 TTL：以最后一次访问（13h，expires → 25h）为基准，跳到 25h+1min ⇒ 失效。
	now = base.Add(25*time.Hour + time.Minute)
	if _, ok := s.Resolve(tok); ok {
		t.Fatal("停止访问超过 TTL 仍有效 —— 永不过期（判据② 负向不成立）")
	}
}

// TestSessionRenewThrottleN077 续期节流：距上次续期 < 5 分钟 ⇒ 不续期（Renewed=false、
// 不写库）—— 避免每请求一次写。
func TestSessionRenewThrottleN077(t *testing.T) {
	s := NewStore(storetest.NewDB(t), "k-n077", DefaultTTL)
	base := time.Date(2026, 10, 7, 8, 0, 0, 0, time.UTC)
	now := base
	s.now = func() time.Time { return now }
	tok := s.Establish("ou_throttle") // updated_at = base

	now = base.Add(1 * time.Minute) // 距上次写 1 分钟 < 5 分钟
	sess, ok := s.Resolve(tok)
	if !ok {
		t.Fatal("节流窗内 Resolve 应仍通过")
	}
	if sess.Renewed {
		t.Error("节流窗内不应续期（Renewed 应为 false）")
	}
	now = base.Add(6 * time.Minute) // ≥ 5 分钟 ⇒ 续期
	sess, ok = s.Resolve(tok)
	if !ok || !sess.Renewed {
		t.Fatalf("过节流窗应续期: ok=%v renewed=%v", ok, sess.Renewed)
	}
}

// TestSessionDestroyPersistentN077 判据⑤（服务端半边）：Destroy 落库删除 ——
// 重启（新实例）后旧 cookie 不复活。
func TestSessionDestroyPersistentN077(t *testing.T) {
	db := storetest.NewDB(t)
	s1 := NewStore(db, "k-n077", DefaultTTL)
	tok := s1.Establish("ou_gone")
	s2 := NewStore(db, "k-n077", DefaultTTL) // 重启
	sess, ok := s2.Resolve(tok)
	if !ok {
		t.Fatal("前置失败：重启后本应有效")
	}
	s1.Destroy(sess.ID)
	s3 := NewStore(db, "k-n077", DefaultTTL) // 再重启
	if _, ok := s3.Resolve(tok); ok {
		t.Fatal("登出后重启旧 cookie 复活 —— Destroy 未持久生效")
	}
}

// TestSessionTTLConfigurableN077 判据④（store 半边）：NewStore 的 ttl 直接决定
// 落库的 ExpiresAt —— ttl=2h ⇒ 签发即 now+2h；ttl<=0 ⇒ 缺省 DefaultTTL(12h)。
func TestSessionTTLConfigurableN077(t *testing.T) {
	db := storetest.NewDB(t)
	base := time.Date(2026, 10, 7, 8, 0, 0, 0, time.UTC)

	s2 := NewStore(db, "k-ttl", 2*time.Hour)
	now := base
	s2.now = func() time.Time { return now }
	tok := s2.Establish("ou_ttl")
	id := tok[:indexDot(tok)]
	row, err := s2.db.GetSession(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if want := base.Add(2 * time.Hour); !row.ExpiresAt.Equal(want) {
		t.Errorf("ttl=2h ⇒ ExpiresAt = %v, 期望 %v（JX_SESSION_TTL 消费面同构）", row.ExpiresAt, want)
	}

	// 缺省：ttl<=0 ⇒ DefaultTTL。
	sd := NewStore(db, "k-ttl2", 0)
	sd.now = func() time.Time { return now }
	tokd := sd.Establish("ou_ttl_default")
	rowd, err := sd.db.GetSession(context.Background(), tokd[:indexDot(tokd)])
	if err != nil {
		t.Fatal(err)
	}
	if want := base.Add(DefaultTTL); !rowd.ExpiresAt.Equal(want) {
		t.Errorf("ttl=0 ⇒ ExpiresAt = %v, 期望缺省 %v（12h）", rowd.ExpiresAt, want)
	}
}

// indexDot cookie 值 = id + "." + sig。
func indexDot(v string) int {
	for i := 0; i < len(v); i++ {
		if v[i] == '.' {
			return i
		}
	}
	return -1
}
