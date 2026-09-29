package specload

// validate —— spec 装载校验入口。
//
// ★★ S1–S12 判据的**唯一来源**是 `spec/checks.json`（N-011 定案）：
//   本包不再硬编码 S 判据，只实现 checks.json#primitives 的 Go 引擎（checklist.go）。
//   Python 侧 check_spec.py 同为清单执行器 ⇒ 两侧同一清单，判据不漂。
// ★ F1–F4 为 Go 装载器自有的表单自洽检查（非 spec 契约判据，故不在清单内）；
//   语义见 forms.go 头注。

import (
	"encoding/json"
	"sort"
	"strings"
)

func validate(files map[string][]byte) []string {
	// ---- S 系列：清单驱动（spec/checks.json 是唯一判据源）----
	problems := runChecklist(files)

	// ---- F 系列：表单自洽（装载器自有）----
	decoded := map[string]any{}
	for k, b := range files {
		var v any
		if err := json.Unmarshal(b, &v); err == nil {
			decoded[k] = v
		}
	}
	enumsKeys := map[string]bool{}
	if em, ok := decoded["spec/enums.json"].(map[string]any); ok {
		for k := range em {
			enumsKeys[k] = true
		}
	}
	problems = append(problems, validateForms(files, decoded, enumsKeys)...)

	return problems
}

// ---------------------------------------------------------------------------
// F 判据共用工具
// ---------------------------------------------------------------------------

func asMap(v any) map[string]any {
	if m, ok := v.(map[string]any); ok {
		return m
	}
	return map[string]any{}
}

func sortedKeys(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func hasPrefix(s, p string) bool {
	return strings.HasPrefix(s, p)
}
