package access

// 批 0 · A7：会话冒烟 —— 签发 / 解析 / 过期 / 篡改拒绝。

import (
	"testing"
	"time"
)

func TestSessionEstablishResolve(t *testing.T) {
	s := NewStore("test-session-key-a7", time.Hour)
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
	s := NewStore("test-session-key-a7", 10*time.Millisecond)
	tok := s.Establish("ou_user_1")
	time.Sleep(30 * time.Millisecond)
	if _, ok := s.Resolve(tok); ok {
		t.Error("过期 token 不应被接受")
	}
}

func TestSessionTamperedRejected(t *testing.T) {
	s := NewStore("test-session-key-a7", time.Hour)
	tok := s.Establish("ou_user_1")
	// 篡改签名段
	bad := tok[:len(tok)-2] + "xx"
	if _, ok := s.Resolve(bad); ok {
		t.Error("篡改 token 不应被接受")
	}
	// 换一把钥匙（伪造签名）
	other := NewStore("other-key", time.Hour)
	if _, ok := other.Resolve(tok); ok {
		t.Error("异钥签名 token 不应被接受")
	}
}
