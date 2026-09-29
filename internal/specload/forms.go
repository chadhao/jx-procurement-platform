package specload

// forms 自洽校验（M3；在 S1–S12 之外的表单侧判据）。
//
// 判据（F 系列 —— check_spec.py 暂无对应项，Go 侧先行，双侧漂移面见 N-011）：
//   F1  forms/<X>.json 的 doc_type 必须与文件名一致（防 BA.json 写成 PR）
//   F2  checks[].id 必须唯一（提交期机判按 id 索引）
//   F3  sections[].id 必须唯一且非空；fields[].name 必须非空
//   F4  enum_ref 形如 "spec/enums.json#<key>…" ⇒ <key> 必须是 enums.json 顶层键
//       （防枚举引用悬空 —— 与 S6 的 route 引用同族）

import (
	"fmt"
	"strings"
)

func validateForms(files map[string][]byte, decoded map[string]any, enumsKeys map[string]bool) []string {
	problems := []string{}
	for _, key := range sortedKeys(files) {
		if !strings.HasPrefix(key, "spec/forms/") || !strings.HasSuffix(key, ".json") {
			continue
		}
		data, ok := decoded[key].(map[string]any)
		if !ok {
			continue // S1 已报
		}

		// F1：doc_type 与文件名一致
		wantDoc := strings.TrimSuffix(strings.TrimPrefix(key, "spec/forms/"), ".json")
		if doc, _ := data["doc_type"].(string); doc != wantDoc {
			problems = append(problems, fmt.Sprintf("[F1] %s 的 doc_type=%q，应为 %q", key, doc, wantDoc))
		}

		// F2：checks[].id 唯一
		seenChecks := map[string]bool{}
		if checks, ok := data["checks"].([]any); ok {
			for _, c := range checks {
				id, _ := asMap(c)["id"].(string)
				if id == "" {
					problems = append(problems, fmt.Sprintf("[F2] %s 存在空 checks.id", key))
					continue
				}
				if seenChecks[id] {
					problems = append(problems, fmt.Sprintf("[F2] %s checks.id 重复：%s", key, id))
				}
				seenChecks[id] = true
			}
		}

		// F3：sections/fields 结构完整
		seenSections := map[string]bool{}
		if secs, ok := data["sections"].([]any); ok {
			for i, s := range secs {
				sm := asMap(s)
				sid, _ := sm["id"].(string)
				if sid == "" {
					problems = append(problems, fmt.Sprintf("[F3] %s sections[%d] 缺 id", key, i))
				} else if seenSections[sid] {
					problems = append(problems, fmt.Sprintf("[F3] %s section id 重复：%s", key, sid))
				} else {
					seenSections[sid] = true
				}
				fields, _ := sm["fields"].([]any)
				for j, f := range fields {
					fm := asMap(f)
					name, _ := fm["name"].(string)
					if name == "" {
						problems = append(problems, fmt.Sprintf("[F3] %s sections[%d].fields[%d] 缺 name", key, i, j))
					}
				}
			}
		}

		// F4：enum_ref 引用的 enums.json 顶层键必须存在
		for _, s := range collectMaps(data["sections"]) {
			fields, _ := s["fields"].([]any)
			for _, f := range fields {
				ref, _ := asMap(f)["enum_ref"].(string)
				if ref == "" {
					continue
				}
				ek, ok := enumRefKey(ref)
				if !ok {
					problems = append(problems, fmt.Sprintf("[F4] %s enum_ref 无法解析：%q", key, ref))
					continue
				}
				if !enumsKeys[ek] {
					problems = append(problems, fmt.Sprintf("[F4] %s enum_ref 指向 enums.json 不存在的键：%s", key, ek))
				}
			}
		}
	}
	return problems
}

// enumRefKey 从 "spec/enums.json#usage_categories（联动说明）" 提取 "usage_categories"。
func enumRefKey(ref string) (string, bool) {
	i := strings.Index(ref, "#")
	if i < 0 || i == len(ref)-1 {
		return "", false
	}
	rest := ref[i+1:]
	// 截断到第一个全角括号/空格
	for _, stop := range []string{"（", "(", " "} {
		if j := strings.Index(rest, stop); j >= 0 {
			rest = rest[:j]
		}
	}
	if rest == "" {
		return "", false
	}
	return rest, true
}

func collectMaps(v any) []map[string]any {
	out := []map[string]any{}
	if list, ok := v.([]any); ok {
		for _, x := range list {
			if m, ok := x.(map[string]any); ok {
				out = append(out, m)
			}
		}
	}
	return out
}
