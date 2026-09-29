package specload

// validate —— Go 侧 S1–S12 等价断言。
//
// ★ 判据逐条对应 scripts/check_spec.py 文档串（降级方案，见 COLLAB.md N-011：
//   checks.yaml 交付前，以 Python 门禁文档串为准实现；交付后切换为清单驱动）。
//   问题文案格式与 Python 侧对齐（"[Sx] …"），便于两侧输出比对。

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

var (
	ledgerCodeRe   = regexp.MustCompile(`^L\d{2}$`)
	badFieldNameRe = regexp.MustCompile(`<br\s*/?>|\s`)
)

var (
	requiredTopKeys  = []string{"version", "roles", "thresholds", "contract_approval", "routes", "doc_chains"}
	requiredDocChain = []string{"BA", "PR", "SA", "RFQ", "BJ", "SS", "CT", "PC", "GR", "QC", "SUB"}
	requiredRoutes   = []string{
		"purchase_tier1", "purchase_tier2", "purchase_tier3",
		"expense_sales", "expense_mgmt_advance", "expense_mgmt_direct",
		"sole_source", "emergency", "change",
	}
	allLedgers        = []string{"L01", "L02", "L03", "L04", "L05", "L06", "L07", "L08", "L09", "L10", "L11", "L12"}
	forbiddenAsTarget = []string{"L08", "L10", "L11", "L12"}
)

func validate(files map[string][]byte) []string {
	problems := []string{}

	// ---- S1：所有 *.json 可解析 ----
	decoded := map[string]any{}
	jsonKeys := make([]string, 0, len(files))
	for k := range files {
		jsonKeys = append(jsonKeys, k)
	}
	sort.Strings(jsonKeys)
	if len(jsonKeys) == 0 {
		problems = append(problems, "[S1] spec/ 下没有任何 .json 文件（机读规格为空？）")
	}
	for _, k := range jsonKeys {
		var v any
		if err := json.Unmarshal(files[k], &v); err != nil {
			problems = append(problems, fmt.Sprintf("[S1] %s JSON 语法错误：%v", k, err))
			continue
		}
		decoded[k] = v
	}
	if _, ok := decoded["spec/chain.json"]; !ok {
		problems = append(problems, "[S2] 缺少 spec/chain.json（审批链规格是地基）")
		return problems
	}

	chain := asMap(decoded["spec/chain.json"])

	// ---- S2：chain.json 顶层必含键 ----
	for _, k := range requiredTopKeys {
		if _, ok := chain[k]; !ok {
			problems = append(problems, fmt.Sprintf("[S2] chain.json 缺顶层键「%s」", k))
		}
	}

	routes := asMap(chain["routes"])
	docChains := asMap(chain["doc_chains"])
	// doc_chains 中 "_" 前缀键为注释性条目（与 Python 侧一致）
	validRouteIDs := map[string]bool{"contract_two_level": true}
	for id := range routes {
		validRouteIDs[id] = true
	}

	// ---- S3：routes 恰好 9 条 ----
	for _, r := range requiredRoutes {
		if _, ok := routes[r]; !ok {
			problems = append(problems, fmt.Sprintf("[S3] routes 缺流程线：%s", r))
		}
	}
	for id := range routes {
		if !contains(requiredRoutes, id) {
			problems = append(problems, fmt.Sprintf("[S3] routes 出现未登记的流程线（请同步更新 requiredRoutes）：%s", id))
		}
	}

	// ---- S4：doc_chains 11 类 ----
	for _, d := range requiredDocChain {
		if _, ok := docChains[d]; !ok {
			problems = append(problems, fmt.Sprintf("[S4] doc_chains 缺单据类型：%s", d))
		}
	}

	// ---- S5：chain 内 ledger 取值合法 + 禁作落账目标（仅 list，与 Python isinstance(v, list) 一致）----
	var walkLedgers func(v any, path string)
	walkLedgers = func(v any, path string) {
		switch t := v.(type) {
		case map[string]any:
			for k, vv := range t {
				if k == "ledger" {
					if list, ok := vv.([]any); ok {
						for _, code := range list {
							s, isStr := code.(string)
							switch {
							case !isStr || !ledgerCodeRe.MatchString(s):
								problems = append(problems, fmt.Sprintf("[S5] %s ledger 取值非法：%v", path, code))
							case contains(forbiddenAsTarget, s):
								problems = append(problems, fmt.Sprintf(
									"[S5] %s ledger 指向 %s —— L08/L10/L11/L12 不可作落账目标（README #20）", path, s))
							case !contains(allLedgers, s):
								problems = append(problems, fmt.Sprintf("[S5] %s ledger 编号不存在：%s", path, s))
							}
						}
					}
				}
				walkLedgers(vv, path+"."+k)
			}
		case []any:
			for i, vv := range t {
				walkLedgers(vv, fmt.Sprintf("%s[%d]", path, i))
			}
		}
	}
	walkLedgers(chain, "chain")

	// ---- S6：route / ref / route_by_tier / route_by_condition 引用不悬空 ----
	var walkRefs func(v any, path string)
	walkRefs = func(v any, path string) {
		switch t := v.(type) {
		case map[string]any:
			for k, vv := range t {
				switch {
				case (k == "route" || k == "ref") && isString(vv):
					s := vv.(string)
					if !hasPrefix(s, "routes.") && !hasPrefix(s, "<") && !validRouteIDs[s] {
						problems = append(problems, fmt.Sprintf("[S6] %s.%s 指向不存在的流程线：%s", path, k, s))
					}
				case k == "route_by_tier":
					if m, ok := vv.(map[string]any); ok {
						for kk, vv2 := range m {
							if isString(vv2) && !validRouteIDs[vv2.(string)] {
								problems = append(problems, fmt.Sprintf("[S6] %s.route_by_tier[%s] 指向不存在的流程线：%s", path, kk, vv2))
							}
						}
					}
				case k == "route_by_condition" && isString(vv):
					for _, part := range splitTrim(vv.(string), "|") {
						if part != "" && !validRouteIDs[part] {
							problems = append(problems, fmt.Sprintf("[S6] %s.route_by_condition 指向不存在的流程线：%s", path, part))
						}
					}
				}
				walkRefs(vv, path+"."+k)
			}
		case []any:
			for i, vv := range t {
				walkRefs(vv, fmt.Sprintf("%s[%d]", path, i))
			}
		}
	}
	walkRefs(chain, "chain")

	// ---- S7：采档阈值无缝无重叠 ----
	bands := []any{}
	if bl, ok := asMap(asMap(chain["thresholds"])["purchase"])["bands"].([]any); ok {
		bands = bl
	}
	if len(bands) != 3 {
		problems = append(problems, fmt.Sprintf("[S7] thresholds.purchase.bands 应为 3 档，实为 %d", len(bands)))
	} else {
		var prevUpper *int64
		for i, bw := range bands {
			bm := asMap(bw)
			lo, _ := jsonInt(bm["lower_inclusive"])
			hi, _ := jsonInt(bm["upper_inclusive"])
			id, _ := bm["id"].(string)
			loIsNull := bm["lower_inclusive"] == nil
			hiIsNull := bm["upper_inclusive"] == nil
			if i == 0 {
				if !loIsNull {
					problems = append(problems, fmt.Sprintf("[S7] 首档 %s 的 lower_inclusive 应为 null（负金额不存在），实为 %v", id, bm["lower_inclusive"]))
				}
			} else if prevUpper != nil {
				if loIsNull || lo != *prevUpper+1 {
					problems = append(problems, fmt.Sprintf(
						"[S7] 档位不连续/有重叠：%s lower=%v，上一档 upper=%d（应为 %d）",
						id, bm["lower_inclusive"], *prevUpper, *prevUpper+1))
				}
			}
			if hiIsNull {
				prevUpper = nil // 无上限（Python 侧 inf，后续无档位）
			} else {
				prevUpper = &hi
			}
		}
		if last := asMap(bands[len(bands)-1]); last["upper_inclusive"] != nil {
			problems = append(problems, fmt.Sprintf("[S7] 末档 upper_inclusive 应为 null（无上限），实为 %v", last["upper_inclusive"]))
		}
	}

	// ---- S8：forms/*.json 字段名规范（禁 <br> / 空白）----
	var walkNames func(v any, path string)
	walkNames = func(v any, path string) {
		switch t := v.(type) {
		case map[string]any:
			if nm, ok := t["name"].(string); ok && badFieldNameRe.MatchString(nm) {
				problems = append(problems, fmt.Sprintf("[S8] %s 字段名不规范（含 <br> 或空白）：%q（R-18）", path, nm))
			}
			for k, vv := range t {
				walkNames(vv, path+"."+k)
			}
		case []any:
			for i, vv := range t {
				walkNames(vv, fmt.Sprintf("%s[%d]", path, i))
			}
		}
	}
	for _, k := range jsonKeys {
		if hasPrefix(k, "spec/forms/") {
			if d, ok := decoded[k]; ok {
				walkNames(d, k)
			}
		}
	}

	// ---- S9~S12：ledger-mapping.json 交叉一致性 ----
	if lmRaw, ok := decoded["spec/ledger-mapping.json"]; ok {
		lm := asMap(lmRaw)
		// S9：递归收集台账编号
		codes := map[string]bool{}
		var collect func(v any)
		collect = func(v any) {
			switch t := v.(type) {
			case map[string]any:
				if c, ok := t["code"].(string); ok && ledgerCodeRe.MatchString(c) {
					codes[c] = true
				}
				for k, vv := range t {
					if ledgerCodeRe.MatchString(k) {
						codes[k] = true
					}
					collect(vv)
				}
			case []any:
				for _, vv := range t {
					collect(vv)
				}
			}
		}
		collect(lm)
		for _, c := range allLedgers {
			if !codes[c] {
				problems = append(problems, fmt.Sprintf("[S9] ledger-mapping.json 未覆盖台账：%s", c))
			}
		}

		d2l := asMap(lm["doc_to_ledger"])
		ledgers := asMap(lm["ledgers"])
		// S10：doc_to_ledger 取值必须在 ledgers 中存在
		for doc, specV := range d2l {
			sm := asMap(specV)
			for _, code := range asStringSlice(sm["ledger"]) {
				if _, ok := ledgers[code]; !ok {
					problems = append(problems, fmt.Sprintf(
						"[S10] ledger-mapping.doc_to_ledger[%s] 指向未定义的台账 %s（ledgers 中不存在）", doc, code))
				}
			}
		}
		// S11：禁作落账目标
		for doc, specV := range d2l {
			sm := asMap(specV)
			for _, code := range asStringSlice(sm["ledger"]) {
				if contains(forbiddenAsTarget, code) {
					problems = append(problems, fmt.Sprintf(
						"[S11] ledger-mapping.doc_to_ledger[%s] 把 %s 当落账目标 —— L08/L10/L11/L12 禁止作落账目标（README #20）", doc, code))
				}
			}
		}
		// S12：forms.ledger 必须与 doc_to_ledger 完全一致（R-02 执行守卫）
		for _, k := range jsonKeys {
			if !hasPrefix(k, "spec/forms/") {
				continue
			}
			fm, ok := decoded[k].(map[string]any)
			if !ok {
				continue
			}
			doc, _ := fm["doc_type"].(string)
			if doc == "" {
				continue
			}
			ds, ok := d2l[doc]
			if !ok {
				continue
			}
			dm := asMap(ds)
			want := sortedCopy(asStringSlice(dm["ledger"]))
			got := sortedCopy(asStringSlice(fm["ledger"]))
			if !equalStrSlices(want, got) {
				problems = append(problems, fmt.Sprintf(
					"[S12] %s 的 ledger=%v 与 ledger-mapping.doc_to_ledger[%s]=%v 不一致", k, got, doc, want))
			}
		}
	}

	return problems
}

// ---------------------------------------------------------------------------
// 小工具
// ---------------------------------------------------------------------------

func asMap(v any) map[string]any {
	if m, ok := v.(map[string]any); ok {
		return m
	}
	return map[string]any{}
}

func asStringSlice(v any) []string {
	list, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(list))
	for _, x := range list {
		if s, ok := x.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func jsonInt(v any) (int64, bool) {
	if f, ok := v.(float64); ok {
		return int64(f), true
	}
	return 0, false
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func isString(v any) bool {
	_, ok := v.(string)
	return ok
}

func hasPrefix(s, p string) bool {
	return strings.HasPrefix(s, p)
}

func splitTrim(s, sep string) []string {
	parts := strings.Split(s, sep)
	for i, p := range parts {
		parts[i] = strings.TrimSpace(p)
	}
	return parts
}

func sortedCopy(s []string) []string {
	out := append([]string{}, s...)
	sort.Strings(out)
	return out
}

func equalStrSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
