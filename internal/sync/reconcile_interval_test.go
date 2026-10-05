package fsync

// N-060 F2：对账周期自适应（纯函数表驱动 —— 01a §5.5 约束2/3）。

import (
	"testing"
	"time"
)

func TestComputeReconcileInterval(t *testing.T) {
	base := 5 * time.Minute
	cases := []struct {
		name    string
		base    time.Duration
		pending int
		level   int
		want    time.Duration
	}{
		{"静默基线：在途少、水位低 ⇒ base", base, 5, 0, 5 * time.Minute},
		{"在途 21 ⇒ ×2", base, 21, 0, 10 * time.Minute},
		{"在途 101 ⇒ ×4", base, 101, 0, 20 * time.Minute},
		{"水位 70 ⇒ 至少 ×2", base, 5, 70, 10 * time.Minute},
		{"水位 90 ⇒ 仍 ×2（保对账不砍停 —— 约束3）", base, 5, 90, 10 * time.Minute},
		{"在途 101 ∧ 水位 90 ⇒ 取大 ×4", base, 101, 90, 20 * time.Minute},
		{"下限保护：base<=0 回落默认 5m", 0, 0, 0, defaultReconcileInterval},
		{"无关闭档：任何输入都 >0", base, 10000, 90, 20 * time.Minute},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := computeReconcileInterval(c.base, c.pending, c.level)
			if got != c.want {
				t.Errorf("interval = %v, 期望 %v", got, c.want)
			}
			if got <= 0 {
				t.Errorf("interval 必须 >0（01a §5.5 约束2：无「关闭对账」档）—— 实为 %v", got)
			}
			// 钳制上界：≤10×base（base 有效时）
			if c.base > 0 && got > 10*c.base {
				t.Errorf("interval = %v 超过 10×base=%v（下限保护被破）", got, 10*c.base)
			}
		})
	}
}
