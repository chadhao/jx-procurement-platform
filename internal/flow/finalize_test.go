package flow_test

import (
	"context"
	"database/sql"
	"sort"
	"testing"

	"github.com/chadhao/jx-procurement-platform/internal/config"
	"github.com/chadhao/jx-procurement-platform/internal/flow"
	"github.com/chadhao/jx-procurement-platform/internal/store"
	"github.com/chadhao/jx-procurement-platform/internal/store/storetest"
)

// TestFinalizeProducesEveryInstanceLedger ★ 每类实例级台账（L01–L07 + L09）都应有生产者（落行）。
func TestFinalizeProducesEveryInstanceLedger(t *testing.T) {
	db := newFlowDB(t)
	instanceTypes := []string{"L01", "L02", "L03", "L04", "L05", "L06", "L07", "L09"}
	maps := &config.Maps{Ledger: map[string][]string{"PR": instanceTypes}}
	svc := flow.NewWithConfig(db, "app", maps, nil)
	ctx := context.Background()

	amt := int64(12345)
	bizNo, err := svc.Submit(ctx, flow.SubmitInput{
		DocType: "PR", ApprovalCode: "code-pr", ApplicantOpenID: "ou_app",
		AmountCents: &amt, Supplier: "甲公司", Department: "生产部",
		Nodes: []flow.NodeSpec{{NodeID: "n1", Seq: 1, Approvers: []flow.Approver{{OpenID: "ou_x"}}}},
		At:    flowAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	tk := taskFor(t, db, bizNo, "ou_x")
	if err := svc.Approve(ctx, bizNo, tk.TaskID, "ou_x", "同意"); err != nil {
		t.Fatal(err)
	}
	if got := instOf(t, db, bizNo).Status; got != flow.InstanceApproved {
		t.Fatalf("实例状态 = %s, 期望 APPROVED", got)
	}
	for _, lt := range instanceTypes {
		if n := storetest.Count(t, db,
			`SELECT COUNT(*) FROM t_ledger_archive WHERE ledger_type = ? AND biz_no = ?`, lt, bizNo); n != 1 {
			t.Errorf("台账 %s 行数 = %d, 期望 1（finalize 未落该台账）", lt, n)
		}
	}
}

// TestResolveLedgerTypesHardBlocks ★ L08/L10/L11/L12 必须被判为**不可落账**（硬拦）。
func TestResolveLedgerTypesHardBlocks(t *testing.T) {
	write, rejected := flow.ResolveLedgerTypes([]string{"L01", "L08", "L10", "L11", "L12", "L09"})
	sort.Strings(write)
	sort.Strings(rejected)
	if len(write) != 2 || write[0] != "L01" || write[1] != "L09" {
		t.Errorf("可落账 = %v, 期望 [L01 L09]", write)
	}
	expectRej := []string{"L08", "L10", "L11", "L12"}
	if len(rejected) != len(expectRej) {
		t.Fatalf("硬拦 = %v, 期望 %v", rejected, expectRej)
	}
	for i := range expectRej {
		if rejected[i] != expectRej[i] {
			t.Errorf("硬拦 = %v, 期望 %v", rejected, expectRej)
			break
		}
	}
}

// TestFinalizeSkipsNonInstanceLedgers 配置里混入非实例级台账时：合法者落行、非法者**不落行**（跳过）。
func TestFinalizeSkipsNonInstanceLedgers(t *testing.T) {
	db := newFlowDB(t)
	maps := &config.Maps{Ledger: map[string][]string{"PR": {"L02", "L08", "L11"}}}
	svc := flow.NewWithConfig(db, "app", maps, nil)
	ctx := context.Background()

	amt := int64(100)
	bizNo, err := svc.Submit(ctx, flow.SubmitInput{
		DocType: "PR", ApprovalCode: "code-pr", ApplicantOpenID: "ou_app",
		AmountCents: &amt,
		Nodes:       []flow.NodeSpec{{NodeID: "n1", Seq: 1, Approvers: []flow.Approver{{OpenID: "ou_x"}}}},
		At:          flowAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	tk := taskFor(t, db, bizNo, "ou_x")
	if err := svc.Approve(ctx, bizNo, tk.TaskID, "ou_x", "同意"); err != nil {
		t.Fatal(err)
	}
	if n := storetest.Count(t, db,
		`SELECT COUNT(*) FROM t_ledger_archive WHERE ledger_type = 'L02' AND biz_no = ?`, bizNo); n != 1 {
		t.Errorf("L02 未落行（合法台账被漏）")
	}
	for _, lt := range []string{"L08", "L11"} {
		if n := storetest.Count(t, db,
			`SELECT COUNT(*) FROM t_ledger_archive WHERE ledger_type = ? AND biz_no = ?`, lt, bizNo); n != 0 {
			t.Errorf("非实例级台账 %s 被落行（应硬拦），行数 = %d", lt, n)
		}
	}
}

// TestFinalizeBizDateAndAmountGuards ★ #33 biz_date 必须 YYYY-MM-DD；#39 amount_cents ≤ 0 不落有效金额。
func TestFinalizeBizDateAndAmountGuards(t *testing.T) {
	db := newFlowDB(t)
	maps := &config.Maps{Ledger: map[string][]string{"PR": {"L02"}}}
	svc := flow.NewWithConfig(db, "app", maps, nil)
	ctx := context.Background()

	// ① 非法 biz_date（非 YYYY-MM-DD）+ 非正金额 → 退回提交日期、金额 NULL。
	bad := int64(0)
	bizNo1, err := svc.Submit(ctx, flow.SubmitInput{
		DocType: "PR", ApprovalCode: "code-pr", ApplicantOpenID: "ou_app",
		AmountCents: &bad, BizFields: map[string]any{"biz_date": "2026-9-1"},
		Nodes: []flow.NodeSpec{{NodeID: "n1", Seq: 1, Approvers: []flow.Approver{{OpenID: "ou_x"}}}},
		At:    flowAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	tk1 := taskFor(t, db, bizNo1, "ou_x")
	if err := svc.Approve(ctx, bizNo1, tk1.TaskID, "ou_x", "同意"); err != nil {
		t.Fatal(err)
	}
	var bizDate1, amount1 sql.NullString
	if err := db.QueryRowContext(ctx,
		`SELECT biz_date, CAST(amount_cents AS TEXT) FROM t_ledger_archive WHERE ledger_type='L02' AND biz_no=?`,
		bizNo1).Scan(&bizDate1, &amount1); err != nil {
		t.Fatal(err)
	}
	if bizDate1.String != "2026-09-27" {
		t.Errorf("非法 biz_date 应退回提交日期 2026-09-27，实际 %q", bizDate1.String)
	}
	if amount1.Valid {
		t.Errorf("amount_cents ≤ 0 不应落有效金额，实际 %q", amount1.String)
	}

	// ② 合法 biz_date 必须被采用（YYYY-MM-DD）。
	amt := int64(500)
	bizNo2, err := svc.Submit(ctx, flow.SubmitInput{
		DocType: "PR", ApprovalCode: "code-pr", ApplicantOpenID: "ou_app",
		AmountCents: &amt, BizFields: map[string]any{"biz_date": "2026-09-01"},
		Nodes: []flow.NodeSpec{{NodeID: "n1", Seq: 1, Approvers: []flow.Approver{{OpenID: "ou_x"}}}},
		At:    flowAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	tk2 := taskFor(t, db, bizNo2, "ou_x")
	if err := svc.Approve(ctx, bizNo2, tk2.TaskID, "ou_x", "同意"); err != nil {
		t.Fatal(err)
	}
	var bizDate2 string
	if err := db.QueryRowContext(ctx,
		`SELECT biz_date FROM t_ledger_archive WHERE ledger_type='L02' AND biz_no=?`, bizNo2).Scan(&bizDate2); err != nil {
		t.Fatal(err)
	}
	if bizDate2 != "2026-09-01" {
		t.Errorf("合法 biz_date 未被采用，实际 %q，期望 2026-09-01", bizDate2)
	}
}

// TestStatusHistoryDedupNoSeqBump 同 (status, operator, opinion) 三者全同 → 去重且**不消耗 event_seq**（#37）。
func TestStatusHistoryDedupNoSeqBump(t *testing.T) {
	db := newFlowDB(t)
	ctx := context.Background()
	h := &store.StatusHistory{InstanceCode: "c1", Status: "PENDING", OperatorOpenID: "ou_a", Opinion: "同意", OccurredAt: flowAt}
	var (
		seq1, seq2 int64
		ap1, ap2   bool
	)
	if err := db.WithTx(ctx, func(tx *sql.Tx) error {
		var err error
		seq1, ap1, err = db.AppendStatusHistory(ctx, tx, h)
		if err != nil {
			return err
		}
		hh := *h // 全同
		seq2, ap2, err = db.AppendStatusHistory(ctx, tx, &hh)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if !ap1 {
		t.Errorf("首条应被追加")
	}
	if ap2 {
		t.Errorf("同状态同人同意见的第二条应被去重（不追加）")
	}
	if seq1 != seq2 {
		t.Errorf("去重不应消耗 event_seq：seq1=%d seq2=%d", seq1, seq2)
	}
	if n := storetest.Count(t, db,
		`SELECT COUNT(*) FROM t_instance_status_history WHERE instance_code='c1'`); n != 1 {
		t.Errorf("状态史行数 = %d, 期望 1（去重失败）", n)
	}
}

// TestSubmitRegistersAttachments 附件元数据在 **Submit** 登记（零网络 IO）；供凭证包清单读取（docs/11 R04）。
func TestSubmitRegistersAttachments(t *testing.T) {
	db := newFlowDB(t)
	svc := flow.New(db, "app")
	ctx := context.Background()

	sz := int64(2048)
	bizNo, err := svc.Submit(ctx, flow.SubmitInput{
		DocType: "PR", ApprovalCode: "code-pr", ApplicantOpenID: "ou_app",
		Attachments: []flow.AttachmentRef{{FileID: "file_1", FieldID: "w_att", FileName: "合同.pdf", Size: &sz}},
		Nodes:       []flow.NodeSpec{{NodeID: "n1", Seq: 1, Approvers: []flow.Approver{{OpenID: "ou_x"}}}},
		At:          flowAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	// ★ 凭证包附件清单的数据源＝按 biz_no 取 t_attachment；此处必须非空（docs/11 R04 修复断言）。
	atts, err := db.ListAttachmentsByBizNos(ctx, []string{bizNo})
	if err != nil {
		t.Fatal(err)
	}
	if len(atts) == 0 {
		t.Fatalf("提交后凭证包附件清单为空（附件未登记）")
	}
	if atts[0].FileID != "file_1" || atts[0].BizNo != bizNo {
		t.Errorf("附件登记内容错误: %+v", atts[0])
	}
}

// TestContractKeyRetrievalKeyScopedAndNoLike ② B22 / 决策 #28：
// 变更链检索**只认契约键** `contract_no`，且**绝不 `LIKE`**。
//
//   - C2 把合同号填在**无关键** `original_biz_no` 上（巧合等值）→ 旧「任意键扫描」会误捞 → 必须**不**捞出；
//   - C3 的 `contract_no = CT-1X` 是 `CT-1` 的**前缀**→ 旧 `LIKE '%CT-1%'` 会命中（P0-A「前缀越权」原型）→ 必须**不**捞出。
func TestContractKeyRetrievalKeyScopedAndNoLike(t *testing.T) {
	db := newFlowDB(t)
	ctx := context.Background()
	seed := func(bizNo, extJSON string) {
		t.Helper()
		if err := db.WithTx(ctx, func(tx *sql.Tx) error {
			return db.UpsertArchiveTx(ctx, tx, &store.LedgerArchive{
				LedgerType: "L09", BizNo: bizNo, ExtJSON: extJSON, CreatedAt: flowAt, UpdatedAt: flowAt,
			})
		}); err != nil {
			t.Fatalf("seed %s 失败: %v", bizNo, err)
		}
	}
	seed("C1", `{"contract_no":"CT-1"}`)     // 命中：合同键等值
	seed("C2", `{"original_biz_no":"CT-1"}`) // ★ 无关键巧合等值 → 不得被捞出
	seed("C3", `{"contract_no":"CT-1X"}`)    // ★ 前缀 → 不得被捞出

	got, err := db.ListChangesByContract(ctx, "CT-1", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	gotBiz := map[string]bool{}
	for _, a := range got {
		gotBiz[a.BizNo] = true
	}
	if !gotBiz["C1"] {
		t.Errorf("应命中 C1（contract_no=CT-1），实际 %v", gotBiz)
	}
	if gotBiz["C2"] {
		t.Errorf("★ 无关键 original_biz_no 的巧合等值被捞出（#28 被破坏，任意键扫描）：%v", gotBiz)
	}
	if gotBiz["C3"] {
		t.Errorf("★ 前缀 CT-1X 被捞出（LIKE 越权，P0-A 原型）：%v", gotBiz)
	}
}
