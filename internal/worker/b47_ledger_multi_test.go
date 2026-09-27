package worker

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/chadhao/jx-procurement-platform/internal/config"
	"github.com/chadhao/jx-procurement-platform/internal/platform/feishu"
	"github.com/chadhao/jx-procurement-platform/internal/store"
	"github.com/chadhao/jx-procurement-platform/internal/store/storetest"
)

// 本文件为 B47 回归（新增，不改既有文件）：
//
//	`doc_type → ledger_type` 一对多 —— `PR` 必须**同时**产生
//	  · `L02` 采购需求与审批台账
//	  · `L03` 采购经办登记台账
//	两张存档行，且 `L03` 行里要能取到「被指定经办人」（看板的「经办人指定集中度」
//	与「需求提出人任经办人的笔数」都读它）。
//
// 反例（若 B47 回退到一对一）：`PR` 只落 `L02` → `L03` **永远没有行** →
// 看板「需求提出人任经办人的笔数」恒为 0，而 **0 恰好是该指标的期望值** ——
// 「没有违规」与「没有数据」在界面上完全一样（静默假绿）。
// 故本用例必须**读库直断 L03 行存在**，而不是只看接口返回。

// b47Maps 构造一份含「PR → L02 + L03」的合法配置并装载。
func b47Maps(t *testing.T) (*store.DB, *config.Maps) {
	t.Helper()
	db := storetest.NewDB(t)
	payload := &config.ImportPayload{
		ApprovalCode: []config.ImportApprovalCode{{Code: "ac-b47-pr", DocType: "PR"}},
		FieldID: []config.ImportFieldID{
			{DocType: "PR", FieldID: "w_pr_amt", FieldName: "预估总金额", BizField: "amount"},
			{DocType: "PR", FieldID: "w_pr_sup", FieldName: "建议供应商", BizField: "supplier"},
			{DocType: "PR", FieldID: "w_pr_dept", FieldName: "申请部门", BizField: "department"},
			{DocType: "PR", FieldID: "w_pr_handler", FieldName: "指定采购经办人", BizField: "assigned_open_id"},
		},
		// ★ 一对多：同一 doc_type 两条台账
		LedgerType: []config.ImportKV{
			{Key: "PR", Value: "L02"},
			{Key: "PR", Value: "L03"},
		},
		Threshold: []config.ImportKV{{Key: "split_supplier_month", Value: "1000"}},
	}
	if _, err := config.ImportMappings(context.Background(), db, payload); err != nil {
		t.Fatalf("导入配置映射失败: %v", err)
	}
	maps, err := config.LoadMaps(context.Background(), db)
	if err != nil {
		t.Fatalf("装载映射失败: %v", err)
	}
	return db, maps
}

// TestB47IngestWritesBothLedgers 真实 Ingest 路径下 PR 必须落 L02 与 L03 两行。
func TestB47IngestWritesBothLedgers(t *testing.T) {
	db, maps := b47Maps(t)
	ctx := context.Background()

	// 前置断言：映射本身确实是「一对多」
	if lts := maps.LedgerTypesFor("PR"); len(lts) != 2 {
		t.Fatalf("PR 的台账映射 = %v，期望 2 条（L02 + L03）", lts)
	}

	g := NewIngestor(db, maps, nil)
	det := &feishu.InstanceDetail{
		InstanceCode:    "I-B47-1",
		ApprovalCode:    "ac-b47-pr",
		StatusRaw:       "APPROVED",
		BizNo:           "PR-2609-7001",
		ApplicantOpenID: "ou_requester",
		Department:      "生产部",
		OccurredAt:      time.Date(2026, 9, 20, 9, 0, 0, 0, time.UTC),
		Fields: []feishu.FieldValue{
			{FieldID: "w_pr_amt", ValueText: "4200"},
			{FieldID: "w_pr_sup", ValueText: "某某轴承"},
			{FieldID: "w_pr_dept", ValueText: "生产部"},
			{FieldID: "w_pr_handler", ValueText: "ou_handler"},
		},
	}
	if err := g.Ingest(ctx, det, SourceEvent); err != nil {
		t.Fatalf("Ingest 失败: %v", err)
	}

	// ① 两行存档都必须存在（这是本用例的核心断言：L03 不能缺席）
	for _, lt := range []string{"L02", "L03"} {
		var n int
		if err := db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM t_ledger_archive WHERE ledger_type=? AND biz_no='PR-2609-7001'`, lt).
			Scan(&n); err != nil {
			t.Fatalf("查 %s 行数失败: %v", lt, err)
		}
		if n != 1 {
			t.Errorf("台账 %s 的 PR-2609-7001 行数 = %d，期望 1（B47 回退时 L03 会是 0）", lt, n)
		}
	}

	// ② L03 行必须带得出「被指定经办人」—— 看板两个监督指标都读它。
	var extJSON string
	if err := db.QueryRowContext(ctx,
		`SELECT COALESCE(ext_json,'') FROM t_ledger_archive WHERE ledger_type='L03' AND biz_no='PR-2609-7001'`).
		Scan(&extJSON); err != nil {
		t.Fatalf("回读 L03 存档失败: %v", err)
	}
	var ext map[string]any
	if err := json.Unmarshal([]byte(extJSON), &ext); err != nil {
		t.Fatalf("L03 ext_json 非法 JSON: %v（%s）", err, extJSON)
	}
	if got, _ := ext["assigned_open_id"].(string); got != "ou_handler" {
		t.Errorf("L03 的 ext_json.assigned_open_id = %q，期望 %q（否则看板「经办人指定集中度」无数据）",
			got, "ou_handler")
	}

	// ③ 两行的业务键必须一致（同一实例、同一业务单号），便于查询侧关联。
	rows, err := db.QueryContext(ctx,
		`SELECT ledger_type, biz_no FROM t_ledger_archive WHERE instance_code='I-B47-1' ORDER BY ledger_type`)
	if err != nil {
		t.Fatalf("查实例台账失败: %v", err)
	}
	defer func() { _ = rows.Close() }()
	var got []string
	for rows.Next() {
		var lt, biz string
		if err := rows.Scan(&lt, &biz); err != nil {
			t.Fatalf("扫描失败: %v", err)
		}
		got = append(got, lt+":"+biz)
	}
	if strings.Join(got, ",") != "L02:PR-2609-7001,L03:PR-2609-7001" {
		t.Errorf("同一实例的台账行 = %v，期望 [L02:PR-2609-7001 L03:PR-2609-7001]", got)
	}
}

// TestB47UnconfiguredDocTypeWritesNoLedger 未配置台账的 doc_type 仍不得写任何台账行。
//
// ★ 反向断言：修复一对多时最容易「顺手」把所有 doc_type 都写一遍，
//
//	这条钉住「未配置 → 不写」（不虚构口径）。
func TestB47UnconfiguredDocTypeWritesNoLedger(t *testing.T) {
	db, maps := b47Maps(t)
	ctx := context.Background()
	g := NewIngestor(db, maps, nil)

	det := &feishu.InstanceDetail{
		InstanceCode: "I-B47-2",
		ApprovalCode: "ac-b47-qc", // 未配置 approval_code / ledger_type
		StatusRaw:    "APPROVED",
		BizNo:        "QC-2609-7002",
		OccurredAt:   time.Date(2026, 9, 20, 9, 30, 0, 0, time.UTC),
	}
	if err := g.Ingest(ctx, det, SourceEvent); err != nil {
		t.Fatalf("Ingest 失败: %v", err)
	}
	var n int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM t_ledger_archive WHERE biz_no='QC-2609-7002'`).Scan(&n); err != nil {
		t.Fatalf("查行数失败: %v", err)
	}
	if n != 0 {
		t.Errorf("未配置映射的 doc_type 写了 %d 行台账，期望 0（不得虚构口径）", n)
	}
}

// TestB47RepeatIngestIsIdempotent 同一实例重复 ingest 不得产生重复台账行。
func TestB47RepeatIngestIsIdempotent(t *testing.T) {
	db, maps := b47Maps(t)
	ctx := context.Background()
	g := NewIngestor(db, maps, nil)

	det := &feishu.InstanceDetail{
		InstanceCode: "I-B47-3",
		ApprovalCode: "ac-b47-pr",
		StatusRaw:    "APPROVED",
		BizNo:        "PR-2609-7003",
		OccurredAt:   time.Date(2026, 9, 21, 9, 0, 0, 0, time.UTC),
		Fields:       []feishu.FieldValue{{FieldID: "w_pr_handler", ValueText: "ou_h2"}},
	}
	for i := 0; i < 3; i++ {
		if err := g.Ingest(ctx, det, SourceEvent); err != nil {
			t.Fatalf("第 %d 次 Ingest 失败: %v", i+1, err)
		}
	}
	var n int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM t_ledger_archive WHERE instance_code='I-B47-3'`).Scan(&n); err != nil {
		t.Fatalf("查行数失败: %v", err)
	}
	if n != 2 {
		t.Errorf("重复 ingest 后台账行数 = %d，期望 2（L02 + L03，幂等不叠加）", n)
	}
}
