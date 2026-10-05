package flow_test

// N-064：WriteTierApprovalRecord 的 ext 回写「可见失败」判据（先红后绿）。
//   ① 损坏的实例 ext_json ⇒ 回写返回错误且**该行 ext 未被改写**（拒绝静默抹掉其它键）；
//   ② L09 台账行缺失 ⇒ 可见 ErrNotFound（不得静默跳过）。

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/chadhao/jx-procurement-platform/internal/config"
	"github.com/chadhao/jx-procurement-platform/internal/flow"
	"github.com/chadhao/jx-procurement-platform/internal/store"
	"github.com/chadhao/jx-procurement-platform/internal/store/storetest"
)

// seedTerminalPCWithOps 造一个 PC 终态实例（L09 已落行）——复用 finalize_l09 夹具。
func seedTerminalPCWithOps(t *testing.T, svc *flow.Service, db *store.DB) string {
	t.Helper()
	ctx := context.Background()
	bizNo := submitPCWithFields(t, svc, map[string]any{
		"contract_no": "CT-2609-0001", "exception_type": "采购变更",
	})
	if err := svc.Approve(ctx, bizNo, taskFor(t, db, bizNo, "ou_x").TaskID, "ou_x", "同意", nil); err != nil {
		t.Fatal(err)
	}
	return bizNo
}

func newTierSvc(t *testing.T) (*flow.Service, *store.DB) {
	t.Helper()
	db := newFlowDB(t)
	maps := &config.Maps{Ledger: map[string][]string{"PC": {"L09"}}}
	return flow.NewWithConfig(db, "app", maps, nil), db
}

// TestWriteTierApprovalRecordCorruptExtVisible ① 损坏实例 ext ⇒ 报错 ＋ ext 未被改写。
// ★ 先红依据（改前实测）：`_ = json.Unmarshal` 静默吞错 ⇒ ext 空表写回、只留 tier 键、
//
//	函数返回 nil —— 两个断言（须报错 / 原串未变）同时红。
func TestWriteTierApprovalRecordCorruptExtVisible(t *testing.T) {
	svc, db := newTierSvc(t)
	ctx := context.Background()
	bizNo := seedTerminalPCWithOps(t, svc, db)

	const corrupt = `{"金额":123,"状态":"APPROVED",损坏`
	if _, err := db.ExecContext(ctx,
		`UPDATE t_instance SET ext_json=? WHERE biz_no=?`, corrupt, bizNo); err != nil {
		t.Fatal(err)
	}

	inst := instOf(t, db, bizNo)
	err := svc.WriteTierApprovalRecord(ctx, &inst)
	if err == nil {
		t.Fatal("损坏 ext 回写必须可见失败（当前实现静默吞掉 Unmarshal 错误 ⇒ 红）")
	}
	if !strings.Contains(err.Error(), "ext_json 损坏") {
		t.Errorf("错误应点名 ext_json 损坏，实为: %v", err)
	}
	// 断言该行 ext **未被改写**（仍是原损坏串、不含新键）
	var after string
	if err := db.QueryRowContext(ctx,
		`SELECT ext_json FROM t_instance WHERE biz_no=?`, bizNo).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != corrupt {
		t.Errorf("损坏 ext 被静默改写: got %q, want 原串 %q", after, corrupt)
	}
	if strings.Contains(after, "tier_approval_record") {
		t.Errorf("回写不得写入新键: %s", after)
	}
}

// TestWriteTierApprovalRecordMissingL09Row ② L09 行缺失 ⇒ 可见 ErrNotFound。
// ★ 红的形态（如实）：改前现路径 GetArchiveByKey 已返回 ErrNotFound 且 %w 保留链
//
//	⇒ 本断言改前即绿；鉴别力由**对抗变异**承载（吞掉助手的 ErrNotFound ⇒ 本用例恰红）。
func TestWriteTierApprovalRecordMissingL09Row(t *testing.T) {
	svc, db := newTierSvc(t)
	ctx := context.Background()
	bizNo := seedTerminalPCWithOps(t, svc, db)

	if n := storetest.Count(t, db,
		`SELECT COUNT(*) FROM t_ledger_archive WHERE ledger_type='L09' AND biz_no=?`, bizNo); n != 1 {
		t.Fatalf("前置：L09 行应已落，实为 %d", n)
	}
	if _, err := db.ExecContext(ctx,
		`DELETE FROM t_ledger_archive WHERE ledger_type='L09' AND biz_no=?`, bizNo); err != nil {
		t.Fatal(err)
	}

	inst := instOf(t, db, bizNo)
	err := svc.WriteTierApprovalRecord(ctx, &inst)
	if err == nil {
		t.Fatal("L09 行缺失必须可见失败（静默跳过 ⇒ 红）")
	}
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("错误链须含 store.ErrNotFound，实为: %v", err)
	}
}
