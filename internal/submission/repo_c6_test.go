package submission

import (
	"context"
	"strings"
	"testing"

	"github.com/chadhao/jx-procurement-platform/internal/store/storetest"
)

// TestC6FindIdemFailsClosedOnCorruptDetail 幂等键记录损坏时必须**报错**，不得静默降级。
//
// 反例（修复前 `_ = json.Unmarshal`）：detail_json 非法 → PayloadHash 留空 →
// 与「历史遗留空指纹」**无法区分** → 运维按错误线索排查；
// 更糟的是若将来有人把空指纹判成"可复用"，就会**用错载荷的首次结果回给调用方**。
// 现改为返回错误（fail-closed）：调用方拿不到记录，绝不会误判为"同载荷可复用"。
func TestC6FindIdemFailsClosedOnCorruptDetail(t *testing.T) {
	ctx := context.Background()
	db := storetest.NewDB(t)
	repo := NewRepo(db)

	const key, actor = "qa-c6-key", "ou_actor"
	if _, err := db.ExecContext(ctx, `
INSERT INTO t_audit_log (actor_open_id, action, resource, target_id, result, detail_json, created_at)
VALUES (?, 'submission_idem', 'submission', ?, 'allow', ?, '2026-09-27T00:00:00Z')`,
		actor, key, `{"submission_id":`); err != nil {
		t.Fatalf("写入损坏的幂等键记录失败: %v", err)
	}

	rec, found, err := repo.FindIdem(ctx, key, actor)
	if err == nil {
		t.Fatalf("记录损坏必须报错，实际返回记录=%+v found=%v", rec, found)
	}
	if rec != nil || found {
		t.Errorf("报错时不得同时返回记录（found=%v rec=%+v）", found, rec)
	}
	if !strings.Contains(err.Error(), "detail_json") {
		t.Errorf("错误信息应点明是 detail_json 损坏，实际: %v", err)
	}
}

// TestC6FindIdemStillWorksOnHealthyRecord 反向断言：正常记录必须照常返回
// （不得因为收紧而把幂等重放这条主路径弄坏）。
func TestC6FindIdemStillWorksOnHealthyRecord(t *testing.T) {
	ctx := context.Background()
	db := storetest.NewDB(t)
	repo := NewRepo(db)

	const key, actor = "qa-c6-ok", "ou_actor"
	if _, err := db.ExecContext(ctx, `
INSERT INTO t_audit_log (actor_open_id, action, resource, target_id, result, detail_json, created_at)
VALUES (?, 'submission_idem', 'submission', ?, 'allow', ?, '2026-09-27T00:00:00Z')`,
		actor, key, `{"submission_id":42,"payload_hash":"abc"}`); err != nil {
		t.Fatalf("写入幂等键记录失败: %v", err)
	}

	rec, found, err := repo.FindIdem(ctx, key, actor)
	if err != nil {
		t.Fatalf("正常记录不该报错: %v", err)
	}
	if !found {
		t.Fatal("正常记录应被找到")
	}
	if rec.SubmissionID != 42 || rec.PayloadHash != "abc" {
		t.Errorf("记录内容不对: %+v", rec)
	}
}

// TestC6FindIdemIsolatedByActor 幂等键按调用方隔离（防止跨方读到他人的报送记录）。
func TestC6FindIdemIsolatedByActor(t *testing.T) {
	ctx := context.Background()
	db := storetest.NewDB(t)
	repo := NewRepo(db)

	if _, err := db.ExecContext(ctx, `
INSERT INTO t_audit_log (actor_open_id, action, resource, target_id, result, detail_json, created_at)
VALUES ('ou_a', 'submission_idem', 'submission', 'same-key', 'allow', '{"submission_id":1,"payload_hash":"h"}', '2026-09-27T00:00:00Z')`); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	if _, found, err := repo.FindIdem(ctx, "same-key", "ou_b"); err != nil || found {
		t.Errorf("乙不应读到甲登记的键：found=%v err=%v", found, err)
	}
	if _, found, err := repo.FindIdem(ctx, "same-key", "ou_a"); err != nil || !found {
		t.Errorf("甲应读到自己登记的键：found=%v err=%v", found, err)
	}
}
