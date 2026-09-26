package worker

import (
	"context"
	"testing"
	"time"

	"github.com/chadhao/jx-procurement-platform/internal/config"
	"github.com/chadhao/jx-procurement-platform/internal/platform/feishu"
	"github.com/chadhao/jx-procurement-platform/internal/store/storetest"
)

// TestConvergeStatus 状态收敛：终态不被中间态覆盖；驳回可回到 PENDING。
func TestConvergeStatus(t *testing.T) {
	cases := []struct {
		current, incoming, want string
	}{
		{"", "PENDING", "PENDING"},
		{"PENDING", "APPROVED", "APPROVED"},
		{"APPROVED", "PENDING", "APPROVED"},  // 终态不被中间态覆盖
		{"APPROVED", "REJECTED", "APPROVED"}, // 同上
		{"REJECTED", "PENDING", "PENDING"},   // 驳回后重提：必须能回到 PENDING（TC-16）
		{"PENDING", "REJECTED", "REJECTED"},
		{"CANCELED", "PENDING", "CANCELED"}, // 终态
		{"PENDING", "", "PENDING"},
	}
	for _, tc := range cases {
		if got := ConvergeStatus(tc.current, tc.incoming); got != tc.want {
			t.Errorf("ConvergeStatus(%q,%q) = %q, 期望 %q", tc.current, tc.incoming, got, tc.want)
		}
	}
}

// TestParseBizNo 业务单号「前缀-YYMM-####」拆分（FR-M2-04）。
func TestParseBizNo(t *testing.T) {
	p := ParseBizNo("PR-2609-0001")
	if p.Prefix != "PR" || p.YYMM != "2609" || p.Seq != "0001" {
		t.Fatalf("解析结果错误: %+v", p)
	}
	if e := ParseBizNo(""); e != (BizNoParts{}) {
		t.Errorf("空单号应返回零值: %+v", e)
	}
}

// TestBackoffSchedule 退避序列（架构 §4.4）。
func TestBackoffSchedule(t *testing.T) {
	if Backoff(1) != 5*time.Second || Backoff(2) != 30*time.Second {
		t.Fatalf("退避序列不符: %v %v", Backoff(1), Backoff(2))
	}
	if Backoff(99) != time.Hour {
		t.Fatalf("超出序列应取末位 1h, got %v", Backoff(99))
	}
}

// TestIngestAppendOnlyHistory 状态历史追加式写入：驳回重提不被覆盖（TC-16 / FR-M3-05）。
func TestIngestAppendOnlyHistory(t *testing.T) {
	db := storetest.NewDB(t)
	base := time.Date(2026, 9, 26, 8, 0, 0, 0, time.UTC)
	clock := func() time.Time { return base }
	ing := NewIngestor(db, &config.Maps{}, nil).WithClock(clock)
	ctx := context.Background()

	chain := []string{"PENDING", "REJECTED", "PENDING", "APPROVED"}
	for i, st := range chain {
		det := &feishu.InstanceDetail{
			InstanceCode: "INST-HIST",
			ApprovalCode: "ac-todo-q1",
			StatusRaw:    st,
			OccurredAt:   base.Add(time.Duration(i) * time.Minute),
		}
		if err := ing.Ingest(ctx, det, SourceEvent); err != nil {
			t.Fatalf("第 %d 次入库失败: %v", i+1, err)
		}
	}

	// 业务表只有 1 条，且状态收敛为终态 APPROVED。
	if n := storetest.Count(t, db, `SELECT COUNT(*) FROM t_instance WHERE instance_code='INST-HIST'`); n != 1 {
		t.Fatalf("实例行数 = %d, 期望 1", n)
	}
	inst, err := db.GetInstance(ctx, "INST-HIST")
	if err != nil {
		t.Fatalf("读取实例失败: %v", err)
	}
	if inst.Status != "APPROVED" {
		t.Errorf("收敛后状态 = %q, 期望 APPROVED", inst.Status)
	}

	// 状态史完整保留 4 条链条，按 event_seq 递增。
	hist, err := db.ListHistory(ctx, "INST-HIST")
	if err != nil {
		t.Fatalf("读取状态史失败: %v", err)
	}
	if len(hist) != len(chain) {
		t.Fatalf("状态史条数 = %d, 期望 %d（驳回重提链条不得被覆盖）", len(hist), len(chain))
	}
	for i, h := range hist {
		if h.Status != chain[i] {
			t.Errorf("状态史[%d] = %q, 期望 %q", i, h.Status, chain[i])
		}
		if h.EventSeq != int64(i+1) {
			t.Errorf("状态史[%d].event_seq = %d, 期望 %d", i, h.EventSeq, i+1)
		}
	}
}
