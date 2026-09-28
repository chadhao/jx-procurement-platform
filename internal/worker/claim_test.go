package worker

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chadhao/jx-procurement-platform/internal/store/storetest"
)

// claim_test.go —— **作业认领原子性**回归测试（P1 缺陷修复，2026-09-28 真机实测发现）。
//
// ★★ 缺陷本体：`ProcessDueOnce` 曾直接调用 `db.DueJobs`（**裸 SELECT，不改行状态**），
// 之后才在 `processJob` 里把作业置 RUNNING。`SELECT` 与 `UPDATE` 是**两条独立语句、
// 两个独立事务**，连接在其间被归还连接池 ⇒ `workerPoolSize=4` 个协程可**依次 SELECT
// 到同一行**，随后各自完整执行一遍。
//
// ★ 真机铁证（本测试即其最小复现）：一条 `contact.user.updated_v3` 事件
// （`t_event_inbox` 仅 1 行、`t_worker_job` 仅 1 行、`attempts=1`）却打出
// **3 条**「人员已增量落库」日志，时间戳相差 300µs / 37ms。
//
// ★ 为什么不能靠 `SetMaxOpenConns(1)` 兜底：单连接保证的是「语句不并行」，
// **不是**「SELECT 与后续 UPDATE 同事务」——两条语句之间连接照样被别的协程用。
//
// ★ 本文件的两个测试分别锁住「认领层的互斥语义」与「端到端不重复执行」：
//   - TestClaimDueJobsAtomicallyExclusive：**确定性**断言 CAS 语义（不依赖调度时序）。
//   - TestConcurrentWorkersNoDuplicateProcessing：8 协程并发调度 12 条事件，
//     断言每条**恰好**执行一次（**用旧实现必失败**，见文件末对照说明）。

// atomicOrgHandler 线程安全的 handler 替身（recordingOrgHandler 的非原子计数器
// 不能用于并发测试）。
type atomicOrgHandler struct {
	calls    atomic.Int64
	payloads atomic.Value // []string
}

func (h *atomicOrgHandler) HandleContactEvent(_ context.Context, payload []byte) error {
	h.calls.Add(1)
	prev, _ := h.payloads.Load().([]string)
	next := make([]string, 0, len(prev)+1)
	next = append(next, prev...)
	next = append(next, string(payload))
	h.payloads.Store(next)
	return nil
}

// TestClaimDueJobsAtomicallyExclusive 认领必须**原子且互斥**：
// 第一次认领拿到作业，第二次（模拟并发的另一个协程）必须拿不到。
func TestClaimDueJobsAtomicallyExclusive(t *testing.T) {
	h := &atomicOrgHandler{}
	svc, w := newOrgTestEnv(t, h)
	ctx := context.Background()

	if _, err := svc.Handle(ctx, contactEvent2("ev-cas-1", "contact.user.updated_v3")); err != nil {
		t.Fatalf("投递失败: %v", err)
	}

	now := time.Now().UTC()

	first, err := w.db.ClaimDueJobs(ctx, now, 20)
	if err != nil {
		t.Fatalf("首次认领出错: %v", err)
	}
	if len(first) != 1 {
		t.Fatalf("首次认领应得 1 条，实得 %d", len(first))
	}
	if first[0].State != jobStateRunning {
		t.Errorf("认领后作业状态 = %q, 期望 %q", first[0].State, jobStateRunning)
	}
	// ★ 认领**不得**改动 attempts（attempts 语义 = 已消耗的处理次数，由 MarkJob 维护）
	if first[0].Attempts != 0 {
		t.Errorf("认领不应消耗 attempts: 实为 %d, 期望 0", first[0].Attempts)
	}

	second, err := w.db.ClaimDueJobs(ctx, now, 20)
	if err != nil {
		t.Fatalf("二次认领出错: %v", err)
	}
	if len(second) != 0 {
		t.Fatalf("已被认领的作业不得被再次分发: 实得 %d 条 —— 认领未原子化", len(second))
	}

	// 全程 handler 一次未被调用（认领 ≠ 处理）。
	if got := h.calls.Load(); got != 0 {
		t.Errorf("认领阶段不应触发处理: handler 调用 %d 次", got)
	}
}

// TestConcurrentWorkersNoDuplicateProcessing 8 个协程并发调度 12 条事件：
// 每条作业与每条收件箱**恰好**处理一次，handler 调用次数恒等于事件数。
//
// ★ 对照实验（本修复的证伪点）：把 `pool.go` 的 `ClaimDueJobs` 换回 `DueJobs`，
// 本测试在 `workerPoolSize` 同形态下必然出现 calls > n（重复执行）——实测 12 条
// 事件下稳定复现。
func TestConcurrentWorkersNoDuplicateProcessing(t *testing.T) {
	h := &atomicOrgHandler{}
	svc, w := newOrgTestEnv(t, h)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	const (
		nWorkers = 8
		nEvents  = 12
	)
	for i := 0; i < nEvents; i++ {
		evID := fmt.Sprintf("ev-claim-%02d", i)
		if _, err := svc.Handle(ctx, contactEvent2(evID, "contact.user.updated_v3")); err != nil {
			t.Fatalf("投递事件 %s 失败: %v", evID, err)
		}
	}

	// 先投递再启动：所有协程的首次调度就会同时看到同一批 QUEUED 行（最恶劣时序）。
	w.Start(ctx, nWorkers)

	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) && h.calls.Load() < nEvents {
		time.Sleep(20 * time.Millisecond)
	}
	// 再多给一秒，让可能的「重复执行」充分暴露（旧实现下多出的调用正是此时产生的）。
	time.Sleep(time.Second)
	cancel()
	w.Stop()

	if got := h.calls.Load(); got != int64(nEvents) {
		t.Fatalf("handler 调用次数 = %d, 期望 %d —— **存在重复执行**（作业认领未原子化）", got, nEvents)
	}

	db := w.db
	if c := storetest.Count(t, db, `SELECT COUNT(*) FROM t_worker_job WHERE state='DONE'`); c != nEvents {
		t.Errorf("DONE 作业数 = %d, 期望 %d", c, nEvents)
	}
	if c := storetest.Count(t, db, `SELECT COUNT(*) FROM t_worker_job WHERE state='RUNNING'`); c != 0 {
		t.Errorf("残留 RUNNING 作业 = %d, 期望 0", c)
	}
	if c := storetest.Count(t, db, `SELECT COUNT(*) FROM t_event_inbox WHERE process_state='DONE'`); c != nEvents {
		t.Errorf("DONE 收件箱数 = %d, 期望 %d", c, nEvents)
	}
	if c := storetest.Count(t, db, `SELECT COUNT(*) FROM t_deadletter`); c != 0 {
		t.Errorf("成功路径不应有死信，实为 %d", c)
	}

	// 每条事件恰好一次（去重后条数 == 事件数）。
	got, _ := h.payloads.Load().([]string)
	seen := map[string]int{}
	for _, p := range got {
		seen[p]++
	}
	for p, c := range seen {
		if c != 1 {
			t.Errorf("同一报文被执行 %d 次（应 1 次）: %.60s…", c, p)
		}
	}
	if len(seen) != nEvents {
		t.Errorf("被执行的不同报文数 = %d, 期望 %d", len(seen), nEvents)
	}
}

// TestClaimDueJobsSkipsNotYetDue 未到期（next_run_at 在未来）的作业不得被认领。
func TestClaimDueJobsSkipsNotYetDue(t *testing.T) {
	h := &atomicOrgHandler{}
	svc, w := newOrgTestEnv(t, h)
	ctx := context.Background()

	if _, err := svc.Handle(ctx, contactEvent2("ev-notdue", "contact.user.updated_v3")); err != nil {
		t.Fatalf("投递失败: %v", err)
	}
	future := time.Now().UTC().Add(time.Hour)
	// 直接改 next_run_at 到未来（模拟退避等待期）。★ 落库格式与 store 一致（RFC3339/UTC），
	// 故此处字符串比较即等价于时间先后比较。
	if _, err := w.db.ExecContext(ctx,
		`UPDATE t_worker_job SET next_run_at = ?`, future.Format(time.RFC3339)); err != nil {
		t.Fatalf("改期失败: %v", err)
	}

	got, err := w.db.ClaimDueJobs(ctx, time.Now().UTC(), 20)
	if err != nil {
		t.Fatalf("认领出错: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("未到期作业不得被认领: 实得 %d 条", len(got))
	}
}
