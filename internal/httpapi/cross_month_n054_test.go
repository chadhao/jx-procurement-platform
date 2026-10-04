package httpapi

// N-054 ① · 批 14：date_range（iso_interval）跨月判定 —— X1–X4 handler 级（真 spec、
// severity 内存置 hard 预演翻转终态）＋ 求值器直调四例 ＋ dateRange.js 纯函数面。
// ★ 契约＝spec/chain.json#conventions.field_payload_forms；判据出处＝
// spec/forms/SA.json#checks[id=cross_month_allocation]。

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/chadhao/jx-procurement-platform/internal/specload"
)

// hardCrossMonthMutator 把 SA#cross_month_allocation 的 severity 在**内存**里置 hard
// （spec 文件零改动 —— soft 不经 evaluateHardChecks，X2/X4 的 400 在 soft 下结构上
// 不可达；本 mutator 预演我方验收后翻 hard 的终态）。
func hardCrossMonthMutator() func(*specload.Bundle) {
	return func(b *specload.Bundle) {
		sa := b.Forms["SA"]
		for i := range sa.Checks {
			if sa.Checks[i].ID == "cross_month_allocation" {
				sa.Checks[i].Severity = "hard"
			}
		}
		b.Forms["SA"] = sa
	}
}

// ---- 求值器直调四例（§1.2：跨月缺拒 / 跨月有放 / 不跨月放 / 不可解析拒）----

func TestN054CrossMonthFourCases(t *testing.T) {
	d := Deps{}
	form := metaTestBundle(t).Forms["SA"]
	ctx := context.Background()

	call := func(period, note string) error {
		fields := map[string]any{}
		if period != "" {
			fields["occurrence_period"] = period
		}
		if note != "" {
			fields["allocation_note"] = note
		}
		return checkSACrossMonthAllocation(ctx, d, form, n47Body("SA", nil, fields), "ou_app")
	}

	mustBlock(t, "跨月 ∧ 空 ⇒ 拒", call("2026-09-28/2026-10-03", ""))
	mustPass(t, "跨月 ∧ 有值 ⇒ 放", call("2026-09-28/2026-10-03", "按周分摊"))
	mustPass(t, "不跨月 ∧ 空 ⇒ 放（不反向）", call("2026-10-05/2026-10-20", ""))
	mustPass(t, "同日起止（边界不跨月）⇒ 放", call("2026-10-05/2026-10-05", ""))
	// 不可解析 ⇒ 拒，且文案与「没填」分句、点名格式要求
	err4 := call("2026-10-01", "")
	mustBlock(t, "单日期缺斜杠 ⇒ 拒（不可解析）", err4)
	if err4 != nil && !strings.Contains(err4.Error(), "格式非法") {
		t.Errorf("不可解析文案应点名格式：%v", err4)
	}
	errEmpty := call("", "")
	mustBlock(t, "未填 ⇒ 拒（与格式非法分句）", errEmpty)
	if errEmpty != nil && !strings.Contains(errEmpty.Error(), "未填写") {
		t.Errorf("未填文案应为「未填写」而非「格式非法」：%v", errEmpty)
	}
	// 倒置（start > end）⇒ 视为不可解析
	mustBlock(t, "起止倒置 ⇒ 拒", call("2026-10-20/2026-10-05", ""))
}

// ---- §1.3 X1–X4（handler 级 · 真 spec · severity 内存置 hard）----

func TestN054X1SameMonthEmptyNote200(t *testing.T) {
	e, _, auth := newSASubmitAppWith(t, hardCrossMonthMutator())
	cookie := auth.Establish("ou_app")
	code, env := postSubmit(t, e, cookie, saBody(func(m map[string]any) {
		m["occurrence_period"] = "2026-10-05/2026-10-20"
		m["allocation_note"] = ""
	}), "")
	if code != http.StatusOK {
		t.Fatalf("X1 同月∧空 ⇒ 应 200（不跨月放的钉子），实为 %d（%s）", code, env.Message)
	}
}

func TestN054X2CrossMonthEmptyNote400(t *testing.T) {
	e, _, auth := newSASubmitAppWith(t, hardCrossMonthMutator())
	cookie := auth.Establish("ou_app")
	code, env := postSubmit(t, e, cookie, saBody(func(m map[string]any) {
		m["occurrence_period"] = "2026-09-28/2026-10-03"
		m["allocation_note"] = ""
	}), "")
	if code != http.StatusBadRequest {
		t.Fatalf("X2 跨月∧空 ⇒ 应 400（真 spec 判据被执行的证明），实为 %d（%s）", code, env.Message)
	}
	if !strings.Contains(env.Message, "allocation_note") {
		t.Errorf("X2 文案应点名 allocation_note：%s", env.Message)
	}
}

func TestN054X3CrossMonthWithNote200(t *testing.T) {
	e, _, auth := newSASubmitAppWith(t, hardCrossMonthMutator())
	cookie := auth.Establish("ou_app")
	code, env := postSubmit(t, e, cookie, saBody(func(m map[string]any) {
		m["occurrence_period"] = "2026-09-28/2026-10-03"
		m["allocation_note"] = "九月按周分摊至各部门"
	}), "")
	if code != http.StatusOK {
		t.Fatalf("X3 跨月∧有值 ⇒ 应 200，实为 %d（%s）", code, env.Message)
	}
}

func TestN054X4Unparseable400(t *testing.T) {
	e, _, auth := newSASubmitAppWith(t, hardCrossMonthMutator())
	cookie := auth.Establish("ou_app")
	code, env := postSubmit(t, e, cookie, saBody(func(m map[string]any) {
		m["occurrence_period"] = "2026-10-01" // 单日期、缺斜杠
		m["allocation_note"] = "随便写点"
	}), "")
	if code != http.StatusBadRequest {
		t.Fatalf("X4 不可解析 ⇒ 应 400（可见失败钉子），实为 %d（%s）", code, env.Message)
	}
	if !strings.Contains(env.Message, "occurrence_period") || !strings.Contains(env.Message, "YYYY-MM-DD") {
		t.Errorf("X4 文案应点名 occurrence_period 与格式要求：%s", env.Message)
	}
}
