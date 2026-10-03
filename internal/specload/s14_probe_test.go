package specload

// S14（checks.json V1.7 · N-036）在 **Go 侧清单引擎**的执行验证：
// `carried_by_kind` 必须来自受控词表 —— 变异真 spec 注入越域值 ⇒ 加载必报 S14；
// 还原 ⇒ 过。★ 这证明「两处的 enum_subset 实现按 checks.json 走」在 Go 侧真实生效
// （WB 方案改进后 S14 值域半条已落，本探针＝两侧一致性在 Go 侧的落点）。

import (
	"strings"
	"testing"
)

func TestS14CarriedByKindDomain(t *testing.T) {
	base := loadReal(t).ProblemsRaw

	// ① 变异：把 PR#no_self_purchaser_at_designation 的 carried_by_kind 改成越域词 ⇒ 必报 S14
	files := cloneFiles(base)
	mutateJSON(t, files, "spec/forms/PR.json", func(m map[string]any) {
		checks, _ := m["checks"].([]any)
		for _, c := range checks {
			cm, _ := c.(map[string]any)
			if cm["id"] == "no_self_purchaser_at_designation" {
				cm["carried_by_kind"] = "totally_bogus_kind"
			}
		}
	})
	_, err := loadFiles(files)
	if err == nil {
		t.Fatal("carried_by_kind 越域值未被 S14 拦下 —— Go 侧清单引擎没执行该判据（或词表漏了）")
	}
	if !strings.Contains(err.Error(), "S14") {
		t.Fatalf("错误未包含 S14 标识: %v", err)
	}

	// ② 还原（真 spec）⇒ 过（正向对照）
	if _, err := loadFiles(cloneFiles(base)); err != nil {
		t.Fatalf("真 spec 应通过 S14: %v", err)
	}
}

// TestArrayEachRequiredProbe 第 10 原语 `array_each_required`（N-036 ④）的
// 鉴别力探针 —— ★ 与 S13 先例同款：[META] 要求探针**先声明注入**原语，不许绕过。
//
//	正向：真 spec（PR#no_self_purchaser_at_designation 带 carried_by_kind）⇒ 过；
//	反向：把该判据的 carried_by_kind 删掉 ⇒ 必报（条件必填被机器看见）；
//	附：collect 收集 0 项 ⇒ 必报（清单写错不许静默通过）。
func TestArrayEachRequiredProbe(t *testing.T) {
	base := loadReal(t).ProblemsRaw

	// 注入原语声明 + 判据（指向 PR 的审批时点判据 —— when∈approval(supervisor_approval)）
	inject := func(files map[string][]byte, mutatePR bool) {
		mutateJSON(t, files, "spec/checks.json", func(m map[string]any) {
			prims, _ := m["primitives"].(map[string]any)
			prims["array_each_required"] = map[string]any{
				"desc": "数组逐项条件必填（探针注入声明 —— META 不许绕过）",
			}
			checks, _ := m["checks"].([]any)
			m["checks"] = append(checks, map[string]any{
				"id": "S15-probe", "severity": "must-green",
				"desc":      "probe: array_each_required 能力自证",
				"primitive": "array_each_required",
				"args": map[string]any{
					"file":          "spec/forms/PR.json",
					"collect":       "checks[*]",
					"required_keys": []any{"carried_by_kind"},
					"when_key":      "when",
					"when_in":       []any{"approval(supervisor_approval)"},
					"when_key2":     "severity",
					"when_in2":      []any{"hard"},
				},
				"min_hits": 1,
			})
		})
		if mutatePR {
			mutateJSON(t, files, "spec/forms/PR.json", func(m map[string]any) {
				checks, _ := m["checks"].([]any)
				for _, c := range checks {
					cm, _ := c.(map[string]any)
					if cm["id"] == "no_self_purchaser_at_designation" {
						delete(cm, "carried_by_kind")
					}
				}
			})
		}
	}

	// ① 正向：真 spec ＋ 注入判据 ⇒ 过（该判据确实带 carried_by_kind）
	files := cloneFiles(base)
	inject(files, false)
	if _, err := loadFiles(files); err != nil {
		t.Fatalf("正向失败（真 spec 该判据应带 carried_by_kind）: %v", err)
	}

	// ② 反向：删掉 carried_by_kind ⇒ 必报 S15-probe（鉴别力）
	files = cloneFiles(base)
	inject(files, true)
	_, err := loadFiles(files)
	if err == nil {
		t.Fatal("删掉 carried_by_kind 后未报错 —— array_each_required 无鉴别力（条件必填没被机器看见）")
	}
	if !strings.Contains(err.Error(), "S15-probe") {
		t.Fatalf("错误未含 S15-probe 标识: %v", err)
	}

	// ③ collect 指空路径 ⇒ 「0 项」必报（清单写错不许静默通过）
	files = cloneFiles(base)
	mutateJSON(t, files, "spec/checks.json", func(m map[string]any) {
		prims, _ := m["primitives"].(map[string]any)
		prims["array_each_required"] = map[string]any{"desc": "probe"}
		checks, _ := m["checks"].([]any)
		m["checks"] = append(checks, map[string]any{
			"id": "S15-empty", "severity": "must-green", "primitive": "array_each_required",
			"args": map[string]any{
				"file": "spec/forms/PR.json", "collect": "no_such_array[*]",
				"required_keys": []any{"x"},
			},
		})
	})
	_, err = loadFiles(files)
	if err == nil || !strings.Contains(err.Error(), "S15-empty") {
		t.Fatalf("collect 0 项未报错（应报清单写错）: %v", err)
	}
}

// TestArrayEachRequiredEmptyConditionGuard N-038 项③（V1.10 · S17）：
// 条件面为空守卫 —— collect 有项但 when 一项不匹配（如 when_in 字面量写错）
// ⇒ 必报；args 声明 allow_empty_match=true ⇒ 豁免（判据 desc 须说明依据）。
func TestArrayEachRequiredEmptyConditionGuard(t *testing.T) {
	base := loadReal(t).ProblemsRaw
	inject := func(files map[string][]byte, whenIn []any, allowEmpty bool) {
		mutateJSON(t, files, "spec/checks.json", func(m map[string]any) {
			prims, _ := m["primitives"].(map[string]any)
			prims["array_each_required"] = map[string]any{"desc": "probe (N-038 S17)"}
			checks, _ := m["checks"].([]any)
			args := map[string]any{
				"file": "spec/forms/PR.json", "collect": "checks[*]",
				"required_keys": []any{"carried_by_kind"},
				"when_key":      "when", "when_in": whenIn,
			}
			if allowEmpty {
				args["allow_empty_match"] = true
			}
			m["checks"] = append(checks, map[string]any{
				"id": "S17-probe", "severity": "must-green",
				"primitive": "array_each_required", "args": args,
			})
		})
	}

	// ① when_in 写错（无任何 when 命中）⇒ 条件面为空守卫必报
	files := cloneFiles(base)
	inject(files, []any{"no_such_when_value"}, false)
	_, err := loadFiles(files)
	if err == nil {
		t.Fatal("条件面为空（when_in 写错）未报 —— 判据将完全空转（S17 治的病）")
	}
	if !strings.Contains(err.Error(), "S17-probe") || !strings.Contains(err.Error(), "条件面为空") {
		t.Fatalf("错误未含 S17-probe/条件面为空: %v", err)
	}

	// ② allow_empty_match=true ⇒ 豁免（正向对照）
	files = cloneFiles(base)
	inject(files, []any{"no_such_when_value"}, true)
	if _, err := loadFiles(files); err != nil {
		t.Fatalf("allow_empty_match=true 应豁免: %v", err)
	}
}
