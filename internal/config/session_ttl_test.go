package config

// N-077 判据④：JX_SESSION_TTL 可配生效 —— 设置 ⇒ 解析；未配置/非法 ⇒ 缺省 12h。

import (
	"testing"
	"time"
)

func TestSessionTTLDefaultAndOverrideN077(t *testing.T) {
	// ① 缺省＝推荐值 12h（覆盖一个工作日）。
	t.Setenv("JX_SESSION_TTL", "")
	env, err := LoadEnv()
	if err != nil {
		t.Fatalf("LoadEnv 失败: %v", err)
	}
	if env.SessionTTL != 12*time.Hour {
		t.Errorf("缺省 SessionTTL = %v, 期望 12h", env.SessionTTL)
	}

	// ② 覆盖生效。
	t.Setenv("JX_SESSION_TTL", "2h30m")
	env2, err := LoadEnv()
	if err != nil {
		t.Fatalf("LoadEnv 失败: %v", err)
	}
	if env2.SessionTTL != 2*time.Hour+30*time.Minute {
		t.Errorf("JX_SESSION_TTL=2h30m ⇒ SessionTTL = %v, 期望 2h30m", env2.SessionTTL)
	}

	// ③ 非法值 ⇒ 回缺省（缺省可用，不拒启）。
	t.Setenv("JX_SESSION_TTL", "banana")
	env3, err := LoadEnv()
	if err != nil {
		t.Fatalf("LoadEnv 失败: %v", err)
	}
	if env3.SessionTTL != 12*time.Hour {
		t.Errorf("非法 JX_SESSION_TTL ⇒ SessionTTL = %v, 期望回落 12h", env3.SessionTTL)
	}
}
