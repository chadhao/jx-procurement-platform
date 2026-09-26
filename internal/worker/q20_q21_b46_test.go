package worker

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/chadhao/jx-procurement-platform/internal/config"
	"github.com/chadhao/jx-procurement-platform/internal/platform/feishu"
	"github.com/chadhao/jx-procurement-platform/internal/store"
	"github.com/chadhao/jx-procurement-platform/internal/store/storetest"
)

// 本文件覆盖本轮三项「静默」防护：
//
//	① B46 状态史**同状态去重**（`inbox` 对 approval_instance / approval_task 一视同仁，
//	       节点事件会反复追加同一状态 → 时间线变噪声、该表不可用）
//	② Q21 单笔金额必须 **> 0**（0/负金额不作为有效金额落库）
//	③ Q20 供应商归一 —— 同名异写必须计入**同一**「当月累计」（防拆分不能被绕过）

func newIngestorWithMaps(t *testing.T) (*Ingestor, *store.DB) {
	t.Helper()
	db := storetest.NewDB(t)
	if _, err := config.ImportMappings(context.Background(), db, &config.ImportPayload{
		ApprovalCode: []config.ImportApprovalCode{{Code: "CODE-CT", DocType: "CT"}},
		FieldID: []config.ImportFieldID{
			{DocType: "CT", FieldID: "w_amount", BizField: config.BizFieldAmount},
			{DocType: "CT", FieldID: "w_supplier", BizField: config.BizFieldSupplier},
		},
		LedgerType: []config.ImportKV{{Key: "CT", Value: "L04"}},
	}); err != nil {
		t.Fatalf("导入配置失败: %v", err)
	}
	m, err := config.LoadMaps(context.Background(), db)
	if err != nil {
		t.Fatalf("装载映射失败: %v", err)
	}
	return NewIngestor(db, m, nil), db
}

// TestStatusHistoryDeduplicatesSameStatus B46：同 (status, operator, opinion) 不重复追加。
func TestStatusHistoryDeduplicatesSameStatus(t *testing.T) {
	db := storetest.NewDB(t)
	ctx := context.Background()
	now := time.Now().UTC()

	// 直接走 store 层（事务内调用）。
	mustAppend := func(h *store.StatusHistory) bool {
		t.Helper()
		var appended bool
		if err := db.WithTx(ctx, func(tx *sql.Tx) error {
			_, a, err := db.AppendStatusHistory(ctx, tx, h)
			appended = a
			return err
		}); err != nil {
			t.Fatalf("追加状态史失败: %v", err)
		}
		return appended
	}

	base := store.StatusHistory{InstanceCode: "I-DEDUP", Status: "PENDING", OperatorOpenID: "ou_a",
		OccurredAt: now}
	if !mustAppend(&base) {
		t.Fatal("首次追加应生效")
	}
	// ① 完全相同 → 跳过
	same := base
	if mustAppend(&same) {
		t.Error("完全相同的状态被重复追加（节点事件会灌满状态史）")
	}
	// ② 仅意见不同 → 追加（有意义的变化不得被误杀）
	withOpinion := base
	withOpinion.Opinion = "同意"
	if !mustAppend(&withOpinion) {
		t.Error("带新意见的同状态应追加（去重过宽会丢信息）")
	}
	// ③ 仅操作人不同 → 追加
	otherOp := withOpinion
	otherOp.OperatorOpenID = "ou_b"
	if !mustAppend(&otherOp) {
		t.Error("换人的同状态应追加")
	}
	// ④ 状态变化 → 追加
	approved := otherOp
	approved.Status = "APPROVED"
	if !mustAppend(&approved) {
		t.Error("状态变化应追加")
	}

	n := storetest.Count(t, db, `SELECT COUNT(*) FROM t_instance_status_history WHERE instance_code='I-DEDUP'`)
	if n != 4 {
		t.Fatalf("状态史行数 = %d, 期望 4（首次 + 新意见 + 换人 + 状态变化）", n)
	}
	// 序号必须**连续无空洞**：跳过时不消耗 event_seq。
	seqs := storetest.Count(t, db, `SELECT COUNT(DISTINCT event_seq) FROM t_instance_status_history WHERE instance_code='I-DEDUP'`)
	if seqs != 4 {
		t.Fatalf("event_seq 去重后 = %d, 期望 4（跳过不应消耗序号）", seqs)
	}
	if max := storetest.Count(t, db, `SELECT MAX(event_seq) FROM t_instance_status_history WHERE instance_code='I-DEDUP'`); max != 4 {
		t.Errorf("最大 event_seq = %d, 期望 4（无空洞）", max)
	}
}

// TestIngestIgnoresNonPositiveAmount Q21：0 / 负金额不作为有效金额落库。
func TestIngestIgnoresNonPositiveAmount(t *testing.T) {
	ing, db := newIngestorWithMaps(t)
	ctx := context.Background()

	cases := []struct {
		name    string
		amount  string
		wantNil bool
	}{
		{"零金额被忽略", "0", true},
		{"负金额被忽略", "-100", true},
		{"正金额正常落库", "100.00", false},
	}
	for i, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			code := "I-AMT-" + string(rune('A'+i))
			det := &feishu.InstanceDetail{
				InstanceCode: code, ApprovalCode: "CODE-CT", StatusRaw: "APPROVED",
				BizNo: "CT-2609-100" + string(rune('0'+i)), ApplicantOpenID: "ou_a",
				Fields: []feishu.FieldValue{{FieldID: "w_amount", ValueText: c.amount}},
			}
			if err := ing.Ingest(ctx, det, SourceEvent); err != nil {
				t.Fatalf("入库失败: %v", err)
			}
			inst, err := db.GetInstance(ctx, code)
			if err != nil {
				t.Fatalf("回读实例失败: %v", err)
			}
			if c.wantNil && inst.AmountCents != nil {
				t.Errorf("金额 = %d, 期望被忽略（Q21：单笔金额必须 > 0）", *inst.AmountCents)
			}
			if !c.wantNil && (inst.AmountCents == nil || *inst.AmountCents != 10000) {
				t.Errorf("金额 = %v, 期望 10000 分", inst.AmountCents)
			}
		})
	}
}

// TestSupplierNormGroupingAcrossVariants Q20：同名异写必须计入同一「当月累计」。
//
// ★ 这是防拆分指标的地基：若按原名分组，「岳阳某某化工有限公司 」（多一个空格）与
// 「岳阳某某化工有限公司」会被算成两家，各自都不到 1,000 元 → 预警永不触发。
func TestSupplierNormGroupingAcrossVariants(t *testing.T) {
	db := storetest.NewDB(t)
	ctx := context.Background()
	now := time.Now().UTC()

	seed := func(bizNo, supplier string, cents int64) {
		t.Helper()
		amt := cents
		if err := db.UpsertArchive(ctx, db, &store.LedgerArchive{
			LedgerType: "L01", BizNo: bizNo, InstanceCode: "I-" + bizNo, SourceDocType: "BA",
			Department: "生产部", ApplicantOpenID: "ou_a", AmountCents: &amt,
			Supplier: supplier, BizDate: "2026-09-10", ExtJSON: "{}", CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			t.Fatalf("写入失败: %v", err)
		}
	}
	// 同一家供应商的三种写法，各 600 元 → 应累计 1,800 元
	seed("BA-2609-0001", "岳阳某某化工有限公司", 60000)
	seed("BA-2609-0002", "岳阳某某化工有限公司 ", 60000)
	seed("BA-2609-0003", "岳阳某某化工有限公司　", 60000) // 全角空格
	// 另一家，写法不同但**确属不同公司** → 不得并入
	seed("BA-2609-0004", "岳阳某某化工", 60000)

	sum, err := db.SumArchiveAmountBySupplierMonth(ctx, "L01", "岳阳某某化工有限公司", "2026-09")
	if err != nil {
		t.Fatalf("累计查询失败: %v", err)
	}
	if sum != 180000 {
		t.Fatalf("同供应商当月累计 = %d 分, 期望 180000（三种写法必须合并；否则防拆分辨认可被绕过）", sum)
	}
	// 反向：不同公司不得被并入
	sum2, err := db.SumArchiveAmountBySupplierMonth(ctx, "L01", "岳阳某某化工", "2026-09")
	if err != nil {
		t.Fatalf("累计查询失败: %v", err)
	}
	if sum2 != 60000 {
		t.Fatalf("不同公司的累计 = %d 分, 期望 60000（归一不等于模糊合并）", sum2)
	}
}
