package observ

// N-060 F2/F8：飞书 API 按月归集（FR-M0-08）＋ 配额水位 70/90（FR-M0-19）。

import (
	"sync"
	"testing"
	"time"
)

func TestQuotaLevelBands(t *testing.T) {
	// 边界：6999→0 · 7000→70 · 8999→70 · 9000→90 · 0→0 · quota<=0→0（不误报）
	cases := []struct {
		calls, quota int64
		want         int
	}{
		{0, 10000, 0},
		{6999, 10000, 0},
		{7000, 10000, 70},
		{8999, 10000, 70},
		{9000, 10000, 90},
		{999999, 10000, 90},
		{500, 0, 0},    // 未配置 ⇒ 不误报
		{-1, 10000, 0}, // 负值防御
	}
	for _, c := range cases {
		if got := QuotaLevel(c.calls, c.quota); got != c.want {
			t.Errorf("QuotaLevel(%d, %d) = %d, 期望 %d", c.calls, c.quota, got, c.want)
		}
	}
}

func TestFeishuAPICallsMonthlyAndLevel(t *testing.T) {
	m := NewMetrics()
	for i := 0; i < 3; i++ {
		m.IncFeishuAPICall()
	}
	month := time.Now().Format("2006-01")
	if got := m.FeishuCallsMonth(month); got != 3 {
		t.Errorf("当月调用量 = %d, 期望 3（按月归集 —— FR-M0-08）", got)
	}
	if got := m.FeishuCallsMonth("1999-01"); got != 0 {
		t.Errorf("未知月 = %d, 期望 0", got)
	}
	if m.FeishuCallsThisMonth() != 3 {
		t.Errorf("FeishuCallsThisMonth = %d, 期望 3", m.FeishuCallsThisMonth())
	}
	// 水位存取（atomic）
	m.SetQuotaLevel(70)
	if m.QuotaLevelNow() != 70 {
		t.Errorf("QuotaLevelNow = %d, 期望 70", m.QuotaLevelNow())
	}
	// Snapshot 暴露按月与水位
	snap := m.Snapshot()
	if snap.FeishuAPICallsTotal != 3 {
		t.Errorf("snapshot total = %d, 期望 3", snap.FeishuAPICallsTotal)
	}
	if snap.FeishuAPICallsMonthly[month] != 3 {
		t.Errorf("snapshot monthly[%s] = %d, 期望 3（F8 按月归集可查）", month, snap.FeishuAPICallsMonthly[month])
	}
	if snap.FeishuQuotaLevel != 70 {
		t.Errorf("snapshot quota_level = %d, 期望 70", snap.FeishuQuotaLevel)
	}
	// 并发安全冒烟（race 下跑）
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			m.IncFeishuAPICall()
		}()
	}
	wg.Wait()
	if m.FeishuCallsThisMonth() != 11 {
		t.Errorf("并发后当月 = %d, 期望 11", m.FeishuCallsThisMonth())
	}
}

// N-060 F12（FR-M3-06）：异步事件作业耗时指标（count/total/max/avg 可查询）。
func TestEventJobDurationMetrics(t *testing.T) {
	m := NewMetrics()
	if snap := m.Snapshot(); snap.EventJobDurationCount != 0 || snap.EventJobDurationAvg != 0 {
		t.Fatalf("初始应全 0: %+v", snap)
	}
	m.AddEventJobDuration(10)
	m.AddEventJobDuration(30)
	m.AddEventJobDuration(5)
	snap := m.Snapshot()
	if snap.EventJobDurationCount != 3 || snap.EventJobDurationTotal != 45 || snap.EventJobDurationMax != 30 || snap.EventJobDurationAvg != 15 {
		t.Errorf("count/total/max/avg = %d/%d/%d/%d, 期望 3/45/30/15",
			snap.EventJobDurationCount, snap.EventJobDurationTotal, snap.EventJobDurationMax, snap.EventJobDurationAvg)
	}
	m.AddEventJobDuration(-1)
	if m.Snapshot().EventJobDurationMax != 30 {
		t.Errorf("负样本不应影响 max, 实为 %d", m.Snapshot().EventJobDurationMax)
	}
}
