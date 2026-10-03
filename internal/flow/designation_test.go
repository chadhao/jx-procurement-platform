package flow_test

// N-015 批 2 承载验收：supervisor_approval 同意 PR 时的指定经办填报。
//   ① 结构化通道（fields 非 nil）必填：缺 ⇒ ErrInvalidDesignation 且任务不推进（同事务回滚）；
//   ② 带齐 ⇒ ext 落四列（designated_purchaser / designation_basis / designated_by / designated_at）
//      ＋ 两个不阻断标记（is_self_designated / designation_out_of_scope）；
//   ③ 非指定节点、非 PR ⇒ no-op；④ nil 通道（飞书回调 / repair）⇒ 豁免不写、不卡死。

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/chadhao/jx-procurement-platform/internal/config"
	"github.com/chadhao/jx-procurement-platform/internal/flow"
	"github.com/chadhao/jx-procurement-platform/internal/store"
)

// submitSupervisorPR 提交一张 PR，链上唯一节点 = supervisor_approval（N-015 唯一时点）。
func submitSupervisorPR(t *testing.T, svc *flow.Service, db *store.DB, docType string) string {
	t.Helper()
	bizNo, err := svc.Submit(context.Background(), flow.SubmitInput{
		DocType: docType, ApprovalCode: "code-pr",
		ApplicantOpenID: "ou_app", ApplicantName: "张三", Department: "生产部",
		Nodes: []flow.NodeSpec{
			{NodeID: "supervisor_approval", NodeName: "主管领导", Seq: 1, Approvers: []flow.Approver{
				{OpenID: "ou_sup", Name: "主管"}},
			},
		},
		At: flowAt,
	})
	if err != nil {
		t.Fatalf("提交失败: %v", err)
	}
	return bizNo
}

// extOf 读实例 ext_json。
func extOf(t *testing.T, db *store.DB, bizNo string) map[string]any {
	t.Helper()
	ext := map[string]any{}
	raw := instOf(t, db, bizNo).ExtJSON
	if raw == "" {
		return ext
	}
	if err := json.Unmarshal([]byte(raw), &ext); err != nil {
		t.Fatalf("ext 解析失败: %v", err)
	}
	return ext
}

func TestDesignationRequiredBlocksApprove(t *testing.T) {
	db := newFlowDB(t)
	svc := flow.New(db, "app")
	ctx := context.Background()
	bizNo := submitSupervisorPR(t, svc, db, "PR")
	tk := taskFor(t, db, bizNo, "ou_sup")

	// ① 空 fields（页面通道恒非 nil）⇒ 必填拦截
	err := svc.Approve(ctx, bizNo, tk.TaskID, "ou_sup", "同意", map[string]any{})
	if !errors.Is(err, flow.ErrInvalidDesignation) {
		t.Fatalf("期望 ErrInvalidDesignation, 实际: %v", err)
	}
	// 同事务回滚：任务仍 PENDING、ext 无 designated_purchaser
	if got := taskFor(t, db, bizNo, "ou_sup").Status; got != "PENDING" {
		t.Errorf("拦截后任务状态 = %s, 期望 PENDING（填报失败不得推进）", got)
	}
	if _, has := extOf(t, db, bizNo)["designated_purchaser"]; has {
		t.Error("拦截后 ext 不应有 designated_purchaser")
	}

	// 缺 designation_basis 同拦
	err = svc.Approve(ctx, bizNo, tk.TaskID, "ou_sup", "同意",
		map[string]any{"designated_purchaser": "ou_h"})
	if !errors.Is(err, flow.ErrInvalidDesignation) {
		t.Fatalf("缺指定依据应拦, 实际: %v", err)
	}
}

func TestDesignationAppliedWithFlags(t *testing.T) {
	db := newFlowDB(t)
	svc := flow.New(db, "app")
	ctx := context.Background()
	// 被指定人：部门=销售部（≠需求部门生产部、≠综合运营部）⇒ 范围外标记
	db.UpsertUserRole(ctx, store.UserRole{
		OpenID: "ou_h_out", Role: "采购经办人", Department: "销售部", Active: true,
	})
	// 被指定人：同需求部门 ⇒ 范围内
	db.UpsertUserRole(ctx, store.UserRole{
		OpenID: "ou_h_in", Role: "采购经办人", Department: "生产部", Active: true,
	})

	bizNo := submitSupervisorPR(t, svc, db, "PR")
	tk := taskFor(t, db, bizNo, "ou_sup")

	// 范围外 + 指定的不是申请人（两个标记都落、都不阻断）
	if err := svc.Approve(ctx, bizNo, tk.TaskID, "ou_sup", "同意", map[string]any{
		"designated_purchaser": "ou_h_out",
		"designation_basis":    "制度第六十条：三家比价最优",
	}); err != nil {
		t.Fatalf("带齐应放行（标记不阻断）: %v", err)
	}
	ext := extOf(t, db, bizNo)
	if ext["designated_purchaser"] != "ou_h_out" || ext["designation_basis"] == "" {
		t.Errorf("四列未落: %v", ext)
	}
	if ext["designated_by"] != "ou_sup" {
		t.Errorf("designated_by = %v, 期望本任务审批人 ou_sup（系统带入）", ext["designated_by"])
	}
	if v, ok := ext["designated_at"].(string); !ok || v == "" {
		t.Errorf("designated_at 未落: %v", ext)
	}
	if v, _ := ext["is_self_designated"].(bool); v {
		t.Errorf("指定的不是申请人，is_self_designated 应 false: %v", ext)
	}
	if v, _ := ext["designation_out_of_scope"].(bool); !v {
		t.Errorf("被指定人部门(销售部)∉{生产部,综合运营部}，应标记范围外: %v", ext)
	}
	// 任务已推进
	if got := taskFor(t, db, bizNo, "ou_sup").Status; got != "APPROVED" {
		t.Errorf("带齐放行后任务状态 = %s, 期望 APPROVED", got)
	}

	// 范围内 + 指定申请人本人 ⇒ is_self_designated=true、out_of_scope=false（均不阻断）
	bizNo2 := submitSupervisorPR(t, svc, db, "PR")
	tk2 := taskFor(t, db, bizNo2, "ou_sup")
	db.UpsertUserRole(ctx, store.UserRole{
		OpenID: "ou_app", Role: "申请人", Department: "生产部", Active: true,
	})
	if err := svc.Approve(ctx, bizNo2, tk2.TaskID, "ou_sup", "同意", map[string]any{
		"designated_purchaser": "ou_app",
		"designation_basis":    "就近经办",
	}); err != nil {
		t.Fatalf("自任经办不阻断（制度第十四条已承认此代价）: %v", err)
	}
	ext2 := extOf(t, db, bizNo2)
	if v, _ := ext2["is_self_designated"].(bool); !v {
		t.Errorf("指定申请人本人 ⇒ is_self_designated 应 true: %v", ext2)
	}
	if v, _ := ext2["designation_out_of_scope"].(bool); v {
		t.Errorf("部门=需求部门 ⇒ 不应标范围外: %v", ext2)
	}
}

func TestDesignationIgnoredOnOtherNodeAndNilChannel(t *testing.T) {
	db := newFlowDB(t)
	svc := flow.New(db, "app")
	ctx := context.Background()

	// ③ 非指定节点：任意节点 id + fields ⇒ 不写不拦（designated_* 仅 supervisor_approval）
	bizNo, err := svc.Submit(context.Background(), flow.SubmitInput{
		DocType: "PR", ApprovalCode: "code-pr",
		ApplicantOpenID: "ou_app", ApplicantName: "张三", Department: "生产部",
		Nodes: []flow.NodeSpec{
			{NodeID: "n1", NodeName: "其他节点", Seq: 1, Approvers: []flow.Approver{
				{OpenID: "ou_x", Name: "某人"}},
			},
		},
		At: flowAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	tk := taskFor(t, db, bizNo, "ou_x")
	if err := svc.Approve(ctx, bizNo, tk.TaskID, "ou_x", "同意", map[string]any{
		"designated_purchaser": "ou_h", "designation_basis": "不该写",
	}); err != nil {
		t.Fatalf("非指定节点应放行: %v", err)
	}
	if _, has := extOf(t, db, bizNo)["designated_purchaser"]; has {
		t.Error("非 supervisor_approval 节点不得落 designated_*")
	}

	// ④ nil 通道（飞书回调 / repair）：豁免必填、不写入
	bizNo2 := submitSupervisorPR(t, svc, db, "PR")
	tk2 := taskFor(t, db, bizNo2, "ou_sup")
	if err := svc.Approve(ctx, bizNo2, tk2.TaskID, "ou_sup", "同意", nil); err != nil {
		t.Fatalf("nil 通道（回调/repair）必须豁免不卡死: %v", err)
	}
	ext := extOf(t, db, bizNo2)
	if _, has := ext["designated_purchaser"]; has {
		t.Errorf("nil 通道不写入: %v", ext)
	}
	if got := taskFor(t, db, bizNo2, "ou_sup").Status; got != "APPROVED" {
		t.Errorf("豁免通道任务状态 = %s, 期望 APPROVED", got)
	}
}

// TestDesignationBlocksSelfSelf N-035：自批自派自经办（操作人与经办人**同时**为
// 需求提出人）⇒ 拦；且两个「允许」必须保留 ——
//
//	① 上级领导指派提出人本人 ⇒ 放（已在 TestDesignationAppliedWithFlags）；
//	② 提出人自批但指定**别人** ⇒ 放（本用例后半）。
func TestDesignationBlocksSelfSelf(t *testing.T) {
	db := newFlowDB(t)
	svc := flow.New(db, "app")
	ctx := context.Background()

	// 构造：申请人本人就是 supervisor_approval 的审批人（自批场景）。
	bizNo, err := svc.Submit(ctx, flow.SubmitInput{
		DocType: "PR", ApprovalCode: "code-pr",
		ApplicantOpenID: "ou_app", ApplicantName: "张三", Department: "生产部",
		Nodes: []flow.NodeSpec{
			{NodeID: "supervisor_approval", NodeName: "主管领导", Seq: 1, Approvers: []flow.Approver{
				{OpenID: "ou_app", Name: "张三"}},
			},
		},
		At: flowAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	tk := taskFor(t, db, bizNo, "ou_app")

	// ① 自批 + 自派 ⇒ 拦（任务仍 PENDING、ext 无残留）
	err = svc.Approve(ctx, bizNo, tk.TaskID, "ou_app", "同意", map[string]any{
		"designated_purchaser": "ou_app",
		"designation_basis":    "我自己经办",
	})
	if !errors.Is(err, flow.ErrInvalidDesignation) {
		t.Fatalf("自批自派应拦（PR#no_self_purchaser_at_designation）, 实际: %v", err)
	}
	if got := taskFor(t, db, bizNo, "ou_app").Status; got != "PENDING" {
		t.Errorf("拦截后任务状态 = %s, 期望 PENDING（同事务回滚）", got)
	}
	if _, has := extOf(t, db, bizNo)["designated_purchaser"]; has {
		t.Error("拦截后 ext 不应有 designated_purchaser（残留）")
	}

	// ② 提出人自批但指定**别人** ⇒ 放行（仅操作人＝提出人，不构成自批自派）
	if err := svc.Approve(ctx, bizNo, tk.TaskID, "ou_app", "同意", map[string]any{
		"designated_purchaser": "ou_h_other",
		"designation_basis":    "指定他人经办",
	}); err != nil {
		t.Fatalf("自批但指定别人应放行（只拦两者同时成立）: %v", err)
	}
	ext := extOf(t, db, bizNo)
	if ext["designated_purchaser"] != "ou_h_other" {
		t.Errorf("ext designated_purchaser = %v, 期望 ou_h_other", ext["designated_purchaser"])
	}
	if v, _ := ext["is_self_designated"].(bool); v {
		t.Errorf("指定的不是申请人本人，is_self_designated 应 false: %v", ext)
	}
	if got := taskFor(t, db, bizNo, "ou_app").Status; got != "APPROVED" {
		t.Errorf("放行后任务状态 = %s, 期望 APPROVED", got)
	}
}

// TestDesignationWritesBothTables N-042 判据⑦：指定经办 ⇒ t_instance.designated_open_id
// 与 t_ledger_archive.designated_open_id（L03）**同时有值**（防「只写一处 / 有列无数据」）。
func TestDesignationWritesBothTables(t *testing.T) {
	db := newFlowDB(t)
	maps := &config.Maps{Ledger: map[string][]string{"PR": {"L02", "L03"}}}
	svc := flow.NewWithConfig(db, "app", maps, nil)
	ctx := context.Background()

	bizNo := submitSupervisorPR(t, svc, db, "PR")
	tk := taskFor(t, db, bizNo, "ou_sup")
	if err := svc.Approve(ctx, bizNo, tk.TaskID, "ou_sup", "同意", map[string]any{
		"designated_purchaser": "ou_h_dual",
		"designation_basis":    "判据⑦：两表同批",
	}); err != nil {
		t.Fatalf("放行: %v", err)
	}

	// ① instance 规范列（designation 同事务写）
	var desig string
	if err := db.QueryRowContext(ctx,
		`SELECT COALESCE(designated_open_id,'') FROM t_instance WHERE biz_no=?`, bizNo).
		Scan(&desig); err != nil {
		t.Fatal(err)
	}
	if desig != "ou_h_dual" {
		t.Errorf("t_instance.designated_open_id = %q, 期望 ou_h_dual（有列无数据＝判据⑦要防的）", desig)
	}
	// ② archive L03 行（单节点 approve ⇒ 终态 ⇒ finalize 落账，同源 ext）
	var arch string
	if err := db.QueryRowContext(ctx,
		`SELECT COALESCE(designated_open_id,'') FROM t_ledger_archive WHERE ledger_type='L03' AND biz_no=?`, bizNo).
		Scan(&arch); err != nil {
		t.Fatalf("L03 行不存在（finalize 未落账？）: %v", err)
	}
	if arch != "ou_h_dual" {
		t.Errorf("L03.designated_open_id = %q, 期望同值（两表同批、不得只写一处）", arch)
	}
}

// TestGRFlowWritesAcceptorsToArchive N-042 判据⑦（GR 侧 · archive 半）：
// GR 提交时 fields["acceptors"]（handler 收集 member_* 后塞入）随 Submit 落 ext，
// finalize 落 L05 时写规范列 acceptors（PARTICIPATED 数据承载 · archive 侧）。
// ★ t_instance.acceptors 由 httpapi handler 在 Submit 成功后写（SetInstanceAcceptors）——
//
//	GR 的 HTTP 通路（ResolveRoute）未接，端到端随通路批验证；本测覆盖 store 写入函数有效。
func TestGRFlowWritesAcceptorsToArchive(t *testing.T) {
	db := newFlowDB(t)
	maps := &config.Maps{Ledger: map[string][]string{"GR": {"L05"}}}
	svc := flow.NewWithConfig(db, "app", maps, nil)
	ctx := context.Background()

	bizNo, err := svc.Submit(ctx, flow.SubmitInput{
		DocType: "GR", ApprovalCode: "code-pr",
		ApplicantOpenID: "ou_app", ApplicantName: "张三", Department: "生产部",
		Nodes: []flow.NodeSpec{
			{NodeID: "n1", NodeName: "入库", Seq: 1, Approvers: []flow.Approver{
				{OpenID: "ou_g", Name: "验收"}},
			},
		},
		// handler 收集 member_* 后塞入 acceptors（本测直接给该形态）
		BizFields: map[string]any{"acceptors": []any{"ou_qc", "ou_store"}},
		At:        flowAt,
	})
	if err != nil {
		t.Fatalf("提交失败: %v", err)
	}
	if err := svc.Approve(ctx, bizNo, taskFor(t, db, bizNo, "ou_g").TaskID, "ou_g", "同意", nil); err != nil {
		t.Fatalf("放行: %v", err)
	}
	var acc string
	if err := db.QueryRowContext(ctx,
		`SELECT COALESCE(acceptors,'') FROM t_ledger_archive WHERE ledger_type='L05' AND biz_no=?`, bizNo).
		Scan(&acc); err != nil {
		t.Fatalf("L05 行不存在: %v", err)
	}
	if acc == "" {
		t.Error("L05.designated acceptors 为空 —— GR 落账未带验收人集合（有列无数据）")
	}
	// store 写入函数本身（handler 调它写 t_instance.acceptors）
	if err := db.SetInstanceAcceptors(ctx, bizNo, `["ou_qc","ou_store"]`); err != nil {
		t.Fatalf("SetInstanceAcceptors: %v", err)
	}
	var iacc string
	if err := db.QueryRowContext(ctx,
		`SELECT COALESCE(acceptors,'') FROM t_instance WHERE biz_no=?`, bizNo).Scan(&iacc); err != nil {
		t.Fatal(err)
	}
	if iacc != `["ou_qc","ou_store"]` {
		t.Errorf("t_instance.acceptors = %q, 期望写入值", iacc)
	}
}
