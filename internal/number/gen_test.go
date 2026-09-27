package number_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/chadhao/jx-procurement-platform/internal/number"
	"github.com/chadhao/jx-procurement-platform/internal/store"
	"github.com/chadhao/jx-procurement-platform/internal/store/storetest"
)

var t0927 = time.Date(2026, 9, 27, 10, 0, 0, 0, time.UTC)

// TestAllocSequential 顺序分配得递增单号。
func TestAllocSequential(t *testing.T) {
	db := storetest.NewDB(t)
	g := number.New(db)
	ctx := context.Background()

	for i, want := range []string{"PR-2609-0001", "PR-2609-0002", "PR-2609-0003"} {
		got, err := g.Alloc(ctx, "PR", t0927)
		if err != nil {
			t.Fatalf("第 %d 次分配失败: %v", i+1, err)
		}
		if got != want {
			t.Errorf("第 %d 个单号 = %s, 期望 %s", i+1, got, want)
		}
	}
}

// TestAllocMonthlyReset 跨月重置序号（YYMM 变化）。
func TestAllocMonthlyReset(t *testing.T) {
	db := storetest.NewDB(t)
	g := number.New(db)
	ctx := context.Background()

	// ★ YYMM 取业务本地时区（Asia/Shanghai, UTC+8）；此处用月中时刻避开跨月边界歧义。
	sep, err := g.Alloc(ctx, "PR", time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	oct, err := g.Alloc(ctx, "PR", time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if sep != "PR-2609-0001" || oct != "PR-2610-0001" {
		t.Errorf("跨月单号 = %s / %s, 期望 PR-2609-0001 / PR-2610-0001", sep, oct)
	}
}

// TestPOCollapsesToCT PO 与 CT 同码同号段（04a §6.1「PO 沿用 CT」），不得撞号。
func TestPOCollapsesToCT(t *testing.T) {
	db := storetest.NewDB(t)
	g := number.New(db)
	ctx := context.Background()

	ct, err := g.Alloc(ctx, "CT", t0927)
	if err != nil {
		t.Fatal(err)
	}
	po, err := g.Alloc(ctx, "PO", t0927)
	if err != nil {
		t.Fatal(err)
	}
	if ct != "CT-2609-0001" || po != "CT-2609-0002" {
		t.Errorf("CT/PO 单号 = %s / %s, 期望 CT-2609-0001 / CT-2609-0002（共用 CT 号段）", ct, po)
	}
}

// TestAllocNoDuplicateUnderConcurrency 并发分配不得重号（负向断言：任何重号即失败）。
func TestAllocNoDuplicateUnderConcurrency(t *testing.T) {
	db := storetest.NewDB(t)
	g := number.New(db)
	const n = 64

	var (
		mu   sync.Mutex
		seen = map[string]int{}
		wg   sync.WaitGroup
	)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := g.Alloc(context.Background(), "PR", t0927)
			if err != nil {
				mu.Lock()
				seen["__err__"+err.Error()]++
				mu.Unlock()
				return
			}
			mu.Lock()
			seen[got]++
			mu.Unlock()
		}()
	}
	wg.Wait()

	for k, c := range seen {
		if k != "" && len(k) > 7 && k[:7] == "__err__" {
			t.Fatalf("并发分配失败: %s", k[7:])
		}
		if c != 1 {
			t.Errorf("单号 %s 被分配 %d 次 → 重号（并发唯一性失效）", k, c)
		}
	}
	if len(seen) != n {
		t.Errorf("不同单号数 = %d, 期望 %d", len(seen), n)
	}
}

// TestDocSeqMonotonic 游标只增不减（04a §6.4 前提 P1 的可执行检查）。
func TestDocSeqMonotonic(t *testing.T) {
	db := storetest.NewDB(t)
	g := number.New(db)
	ctx := context.Background()

	if _, err := g.Alloc(ctx, "PR", t0927); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Alloc(ctx, "PR", t0927); err != nil {
		t.Fatal(err)
	}
	seq1, found, err := db.PeekDocSeq(ctx, "PR", "2609")
	if err != nil || !found {
		t.Fatalf("读取游标失败: found=%v err=%v", found, err)
	}
	if seq1 != 2 {
		t.Fatalf("游标 = %d, 期望 2", seq1)
	}
	if _, err := g.Alloc(ctx, "PR", t0927); err != nil {
		t.Fatal(err)
	}
	seq2, _, err := db.PeekDocSeq(ctx, "PR", "2609")
	if err != nil {
		t.Fatal(err)
	}
	if seq2 <= seq1 {
		t.Errorf("游标回退：%d → %d（破坏锁号前提 P1）", seq1, seq2)
	}
}

// TestTerminalBizNoReuseRejected ★ 负向断言：终态单号被复用必须被拒（UNIQUE(biz_no) 兜底）。
func TestTerminalBizNoReuseRejected(t *testing.T) {
	db := storetest.NewDB(t)
	ctx := context.Background()
	now := time.Now().UTC()

	// 先占一个「已终态」的单号（模拟：该单已 APPROVED/CANCELED，永久锁号）。
	if err := db.UpsertInstance(ctx, &store.Instance{
		InstanceCode: "app:PR-2609-0001", ApprovalCode: "code-pr", DocType: "PR",
		BizNo: "PR-2609-0001", Status: "APPROVED", Source: "flow",
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("首笔实例写入失败: %v", err)
	}

	// 试图「复用」同一终态单号（不同 instance_code）→ 必须被 UNIQUE(biz_no) 拦下。
	err := db.UpsertInstance(ctx, &store.Instance{
		InstanceCode: "app:OTHER", ApprovalCode: "code-pr", DocType: "PR",
		BizNo: "PR-2609-0001", Status: "PENDING", Source: "flow",
		CreatedAt: now, UpdatedAt: now,
	})
	if err == nil {
		t.Fatalf("复用终态单号 PR-2609-0001 被接受 → 锁号失效（UNIQUE(biz_no) 兜底未生效）")
	}

	// 且该单号仍归属原实例，未被第二笔覆盖。
	inst, err := db.GetInstance(ctx, "app:PR-2609-0001")
	if err != nil {
		t.Fatalf("读取原实例失败: %v", err)
	}
	if inst.BizNo != "PR-2609-0001" || inst.Status != "APPROVED" {
		t.Errorf("原实例被污染: biz_no=%s status=%s", inst.BizNo, inst.Status)
	}
}
