package httpapi

// N-047 批 12 · BA/PR/SA 提交时点求值器 8 条（合成 FormDoc 直调 evaluateHardChecks ——
// 真 spec 的 severity 未声明、真实提交路径暂不调用，由我方随后 severity 提交启用）。
// 每条拦/放成对；条件型四例；边界与 fail-closed；跨单据分流钉子（M3 鉴别力）。

import (
	"context"
	"strings"
	"testing"

	"github.com/chadhao/jx-procurement-platform/internal/specload"
)

// n047Form 合成表单：真 spec 起底（拿 DocType 与该判据的 when/assert），只留目标 id
// 并置 severity=hard —— 照 §1.2#1：测试不得依赖真实 spec 的 severity。
func n047Form(t *testing.T, docType string, ids ...string) specload.FormDoc {
	t.Helper()
	base := metaTestBundle(t).Forms[docType]
	want := map[string]bool{}
	for _, id := range ids {
		want[id] = true
	}
	checks := make([]specload.CheckDoc, 0, len(ids))
	for _, c := range base.Checks {
		if want[c.ID] {
			c.Severity = "hard"
			checks = append(checks, c)
			delete(want, c.ID)
		}
	}
	for id := range want {
		t.Fatalf("真 spec 缺判据 %q@%s —— spec 结构变了？", id, docType)
	}
	base.Checks = checks
	return base
}

func n47Body(docType string, amt *int64, fields map[string]any) *approvalSubmitBody {
	return &approvalSubmitBody{DocType: docType, AmountCents: amt, Fields: fields}
}

func runN47(t *testing.T, form specload.FormDoc, body *approvalSubmitBody) error {
	t.Helper()
	return Deps{}.evaluateHardChecks(context.Background(), form, body, "ou_app")
}

// ---- #1/#7 amount_positive（含边界、两缺 fail-closed、跨单据分流钉子）----

func TestN047AmountPositive(t *testing.T) {
	ba := n047Form(t, "BA", "amount_positive")
	sa := n047Form(t, "SA", "amount_positive")
	one := int64(1)
	zero := int64(0)
	neg := int64(-5)

	mustBlock(t, "BA 0 ⇒ 拒", runN47(t, ba, n47Body("BA", &zero, nil)))
	mustBlock(t, "BA 负数 ⇒ 拒", runN47(t, ba, n47Body("BA", &neg, nil)))
	mustPass(t, "BA 正数 ⇒ 放", runN47(t, ba, n47Body("BA", &one, nil)))
	// 顶层 nil ⇒ 回落 fields.amount_cents
	mustPass(t, "BA 顶层缺回落 fields ⇒ 放", runN47(t, ba, n47Body("BA", nil, map[string]any{"amount_cents": float64(100)})))
	// ★ 两处都缺 ⇒ 可见失败（fail-closed），且文案与「值不合法」不同
	errBoth := runN47(t, ba, n47Body("BA", nil, map[string]any{}))
	mustBlock(t, "BA 两处皆缺 ⇒ 拒", errBoth)
	if errBoth != nil && !strings.Contains(errBoth.Error(), "读取失败") {
		t.Errorf("读不到值的文案应与值不合法不同（点名读取失败）：%v", errBoth)
	}
	mustPass(t, "SA 正数 ⇒ 放", runN47(t, sa, n47Body("SA", &one, nil)))
	mustBlock(t, "SA 两处皆缺 ⇒ 拒", runN47(t, sa, n47Body("SA", nil, map[string]any{})))

	// ★ 跨单据分流钉子（防同键互相覆盖 / M3 鉴别力）：
	// PR 版必须读 estimated_total_cents（服务端汇总回写），而不是 amount_cents。
	pr := n047Form(t, "PR", "amount_positive")
	mustPass(t, "PR 有 estimated_total_cents ⇒ 放（读 estimated 非 amount）",
		runN47(t, pr, n47Body("PR", nil, map[string]any{"estimated_total_cents": float64(50000)})))
	mustBlock(t, "PR 缺 estimated_total_cents ⇒ 拒",
		runN47(t, pr, n47Body("PR", nil, map[string]any{})))
	// ★ 钉死「读的是 estimated」：fields 有 amount_cents 但无 estimated ⇒ PR 版仍拒
	mustBlock(t, "PR 只有 amount_cents 无 estimated ⇒ 仍拒（证明 PR 不读 amount_cents）",
		runN47(t, pr, n47Body("PR", nil, map[string]any{"amount_cents": float64(100)})))
}

// ---- #2 amount_tier1_only（边界 99999/100000）----

func TestN047AmountTier1Only(t *testing.T) {
	ba := n047Form(t, "BA", "amount_tier1_only")
	edge := int64(99999)
	over := int64(100000)
	zero := int64(0)
	neg := int64(-1)

	mustPass(t, "99999 ⇒ 放（边界含）", runN47(t, ba, n47Body("BA", &edge, nil)))
	mustBlock(t, "100000 ⇒ 拒（≥1,000 元走 PR）", runN47(t, ba, n47Body("BA", &over, nil)))
	mustBlock(t, "0 ⇒ 拒", runN47(t, ba, n47Body("BA", &zero, nil)))
	mustBlock(t, "负数 ⇒ 拒", runN47(t, ba, n47Body("BA", &neg, nil)))
	mustBlock(t, "两处皆缺 ⇒ 拒（fail-closed）", runN47(t, ba, n47Body("BA", nil, map[string]any{})))
	if err := runN47(t, ba, n47Body("BA", &over, nil)); err != nil && !strings.Contains(err.Error(), "PR") {
		t.Errorf("超档文案应点名走 PR：%v", err)
	}
}

// ---- #3/#5/#8 completeness_l2（三单据同语义；M2 鉴别力＝L1 有 / L2 空）----

func TestN047CompletenessL2(t *testing.T) {
	for _, dt := range []string{"BA", "PR", "SA"} {
		form := n047Form(t, dt, "completeness_l2")
		full := map[string]any{"usage_category_l1": "P01", "usage_category_l2": "主原料"}
		l1Only := map[string]any{"usage_category_l1": "P01"}
		l2Only := map[string]any{"usage_category_l2": "主原料"}
		mustPass(t, dt+" 一二级齐 ⇒ 放", runN47(t, form, n47Body(dt, nil, full)))
		// ★ M2 鉴别力：L1 有 / L2 空必须拒（改成只判 L1 即红）
		mustBlock(t, dt+" L1 有 L2 空 ⇒ 拒（M2 钉子）", runN47(t, form, n47Body(dt, nil, l1Only)))
		mustBlock(t, dt+" L1 空 L2 有 ⇒ 拒", runN47(t, form, n47Body(dt, nil, l2Only)))
		mustBlock(t, dt+" 全空 ⇒ 拒", runN47(t, form, n47Body(dt, nil, map[string]any{})))
	}
}

// ---- #4 safety_certificate（条件型四例）----

func TestN047SafetyCertificate(t *testing.T) {
	ba := n047Form(t, "BA", "safety_certificate")
	p03 := map[string]any{"usage_category_l1": "P03", "usage_category_l2": "x"}
	p03ok := map[string]any{"usage_category_l1": "P03", "usage_category_l2": "x", "qualified_certificate": "合格证 X"}
	p01no := map[string]any{"usage_category_l1": "P01", "usage_category_l2": "x"}
	p01yes := map[string]any{"usage_category_l1": "P01", "usage_category_l2": "x", "qualified_certificate": "随手传的"}

	mustBlock(t, "P03 缺证明 ⇒ 拒", runN47(t, ba, n47Body("BA", nil, p03)))
	mustPass(t, "P03 有证明 ⇒ 放", runN47(t, ba, n47Body("BA", nil, p03ok)))
	// ★ 不得反向：非 P03 且字段为空 ⇒ 放
	mustPass(t, "非 P03 空证明 ⇒ 放（不反向）", runN47(t, ba, n47Body("BA", nil, p01no)))
	mustPass(t, "非 P03 有证明 ⇒ 放", runN47(t, ba, n47Body("BA", nil, p01yes)))
}

// ---- #6 device_tech_attachment（条件型四例，PR）----

func TestN047DeviceTechAttachment(t *testing.T) {
	pr := n047Form(t, "PR", "device_tech_attachment")
	p04 := map[string]any{"usage_category_l1": "P04", "usage_category_l2": "x"}
	p04ok := map[string]any{"usage_category_l1": "P04", "usage_category_l2": "x", "tech_attachment": "技术附件 A"}
	p01no := map[string]any{"usage_category_l1": "P01", "usage_category_l2": "x"}
	p01yes := map[string]any{"usage_category_l1": "P01", "usage_category_l2": "x", "tech_attachment": "随手传的"}

	mustBlock(t, "P04 缺附件 ⇒ 拒", runN47(t, pr, n47Body("PR", nil, p04)))
	mustPass(t, "P04 有附件 ⇒ 放", runN47(t, pr, n47Body("PR", nil, p04ok)))
	mustPass(t, "非 P04 空附件 ⇒ 放（不反向）", runN47(t, pr, n47Body("PR", nil, p01no)))
	mustPass(t, "非 P04 有附件 ⇒ 放", runN47(t, pr, n47Body("PR", nil, p01yes)))
}
