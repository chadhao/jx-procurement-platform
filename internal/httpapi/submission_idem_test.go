package httpapi

import (
	"net/http"
	"sync"
	"testing"

	"github.com/chadhao/jx-procurement-platform/internal/store"
	"github.com/chadhao/jx-procurement-platform/internal/store/storetest"
)

// TestSubmissionConcurrentSameKeyOnlyOneRow ★ P0 回归：并发同键 + 同载荷只能产生 1 条记录。
//
// 背景：原实现为「FindIdem → CreateSubmission → RecordIdem」三条**独立语句**。
// 连接池虽限制为 1 条连接（store.Open 的 SetMaxOpenConns(1)），但池只串行化**单条语句**、
// **不串行化语句序列** —— 两条并发请求可交错通过各自的 FindIdem，然后各建一条记录（TOCTOU）。
//
// 修复：CreateWithIdem 把整段序列放进一个事务（独占连接），
// 并以 0002 迁移的部分唯一索引作为与并发模型无关的兜底。
//
// ★ 注意：若本用例失败（行数 = 2），说明「唯一约束」这道防线失效，
// 需先检查 migrations/0002_idem_unique.sql 是否已被执行。
func TestSubmissionConcurrentSameKeyOnlyOneRow(t *testing.T) {
	e, db, auth := newM1M6App(t)
	seedDefaultUsers(t, db, store.UserRole{OpenID: "ou_ops", Role: roleOpsSupervisor, Active: true})
	cookie := auth.Establish("ou_ops")

	// 同一幂等键 + 同一载荷，且**不带业务单号之外的区分字段**。
	// 注意：business 侧 biz_no 有唯一约束，这里刻意用同一 biz_no，
	// 以聚焦「幂等键占位」这道防线（biz_no 唯一是对另一类重复的兜底）。
	headers := map[string]string{"Idempotency-Key": "race-key-001"}
	body := `{"biz_no":"SUB-RACE-1","subject_type":"公户付款","hn_finish_date":"2026-09-21"}`

	const n = 8
	var wg sync.WaitGroup
	codes := make([]int, n)
	envCodes := make([]int, n)
	ids := make([]float64, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			rec, env := m1m6Do(e, http.MethodPost, "/api/submission", cookie, body, headers)
			codes[idx] = rec.Code
			envCodes[idx] = env.Code
			if m, okData := env.Data.(map[string]any); okData {
				if v, okID := m["id"].(float64); okID {
					ids[idx] = v
				}
			}
		}(i)
	}
	wg.Wait()

	// ① 只允许落 1 条。
	if got := storetest.Count(t, db, `SELECT COUNT(*) FROM t_submission`); got != 1 {
		t.Fatalf("并发同键后报送行数 = %d, 期望 1（幂等/唯一约束失效）", got)
	}
	// ② ★ 全部请求都应成功：胜出者新建（200），其余幂等复用（200 同一 id）。
	//    任何 409 都说明「占位顺序」写错了（先建报送后占位 → 重试撞 biz_no 唯一约束）。
	for i := 0; i < n; i++ {
		if codes[i] != http.StatusOK || envCodes[i] != codeOK {
			t.Errorf("第 %d 次请求: http=%d code=%d, 期望 200/0（同键同载荷应当幂等复用而非报冲突）",
				i, codes[i], envCodes[i])
		}
	}
	// ③ 所有成功响应必须指向同一条记录。
	for i := 0; i < n; i++ {
		if ids[i] != ids[0] {
			t.Errorf("第 %d 次返回 id=%v, 与首次 %v 不一致（幂等复用应返回同一记录）", i, ids[i], ids[0])
		}
	}
}

// TestSubmissionConcurrentDifferentBizNoSameKeyIsolated 并发同键但**不同载荷**时，
// 只能有一条胜出（另一条必须 40900），不得两条都落库。
func TestSubmissionConcurrentDifferentBizNoSameKeyIsolated(t *testing.T) {
	e, db, auth := newM1M6App(t)
	seedDefaultUsers(t, db, store.UserRole{OpenID: "ou_ops", Role: roleOpsSupervisor, Active: true})
	cookie := auth.Establish("ou_ops")

	headers := map[string]string{"Idempotency-Key": "race-key-002"}
	bodies := []string{
		`{"biz_no":"SUB-RACE-A","subject_type":"公户付款"}`,
		`{"biz_no":"SUB-RACE-B","subject_type":"公户付款"}`,
	}

	var wg sync.WaitGroup
	results := make([]int, len(bodies))
	for i, b := range bodies {
		wg.Add(1)
		go func(idx int, body string) {
			defer wg.Done()
			rec, _ := m1m6Do(e, http.MethodPost, "/api/submission", cookie, body, headers)
			results[idx] = rec.Code
		}(i, b)
	}
	wg.Wait()

	if got := storetest.Count(t, db, `SELECT COUNT(*) FROM t_submission`); got != 1 {
		t.Fatalf("并发同键异载荷后报送行数 = %d, 期望 1（只有一条可胜出）", got)
	}
	okCount := 0
	for i, c := range results {
		switch c {
		case http.StatusOK:
			okCount++
		case http.StatusConflict:
		default:
			t.Errorf("第 %d 次请求状态码 = %d, 期望 200 或 409", i, c)
		}
	}
	if okCount != 1 {
		t.Errorf("成功次数 = %d, 期望恰好 1", okCount)
	}
}

// TestSubmissionIdemPartialUniqueIndexExists 断言 0002 迁移的唯一索引确实生效。
//
// ★ 这是「并发兜底」的根基：若索引缺失，上面的并发用例只会偶发失败（难以复现），
// 本用例则提供**确定性**的缺失检测。
func TestSubmissionIdemPartialUniqueIndexExists(t *testing.T) {
	_, db, _ := newM1M6App(t)

	for _, name := range []string{"ux_audit_idem_key", "ux_submission_biz_no_nonnull"} {
		n := storetest.Count(t, db,
			`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='`+name+`'`)
		if n != 1 {
			t.Errorf("唯一索引 %s 不存在（migrations/0002_idem_unique.sql 未生效）", name)
		}
	}

	// 行为验证：同一 (target_id, actor_open_id) 的 submission_idem 行不可重复插入。
	insert := `INSERT INTO t_audit_log(actor_open_id, action, resource, target_id, result, created_at)
	           VALUES('ou_x','submission_idem','submission','dup-key','allow','2026-09-26T00:00:00Z')`
	if _, err := db.Exec(insert); err != nil {
		t.Fatalf("首次插入幂等行失败: %v", err)
	}
	if _, err := db.Exec(insert); err == nil {
		t.Error("重复插入同一幂等键成功 → 部分唯一索引未生效，并发下会产生重复报送")
	}
	// 换一个 actor 应可插入（幂等键按调用方隔离）。
	if _, err := db.Exec(`INSERT INTO t_audit_log(actor_open_id, action, resource, target_id, result, created_at)
	                      VALUES('ou_y','submission_idem','submission','dup-key','allow','2026-09-26T00:00:00Z')`); err != nil {
		t.Errorf("不同 actor 用同一幂等键应被允许: %v", err)
	}
}
