package specload

// checklist —— `spec/checks.json` 的 Go 消费端（N-011 定案：清单是判据**唯一来源**）。
//
// ★★★ 消费纪律（checks.json#consumer_obligations.go_side）：
//   - 本文件实现 8 个原语的 Go 版；**原语语义以 checks.json#primitives[].desc 裁决**；
//   - 判据＝数据（哪个文件、哪个原语、什么参数），不得在 Go 侧硬编码 S1–S12；
//   - ★ **min_hits（默认 1）必须实现**：collect 命中数不足即报错 ——
//     杜绝「声明写错（如 [*] 误展开）＝判据没跑却静默通过」（V1.2 第 4 例假绿教训）。
//
// 点路径语法（与 Python 侧一致）：`a.b` 取键 · `a.*`/`a[*]` 取子节点 · `**` 递归任意深度。

import (
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"
)

type checksDoc struct {
	Version    string                     `json:"version"`
	Primitives map[string]json.RawMessage `json:"primitives"`
	Checks     []checkDef                 `json:"checks"`
}

type checkDef struct {
	ID        string                     `json:"id"`
	Severity  string                     `json:"severity"`
	Desc      string                     `json:"desc"`
	Primitive string                     `json:"primitive"`
	Args      map[string]json.RawMessage `json:"args"`
}

// runChecklist 执行 spec/checks.json 的全部判据，返回问题列表（"[Sx] …"）。
func runChecklist(files map[string][]byte) []string {
	problems := []string{}

	// ---- 清单自身必须可解析（清单不可用 ⇒ 一切判据无从执行，fail-closed）----
	raw, ok := files["spec/checks.json"]
	if !ok {
		return []string{"[META] 缺少 spec/checks.json（判据清单是唯一来源）"}
	}
	var doc checksDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		return []string{fmt.Sprintf("[META] spec/checks.json 解析失败：%v", err)}
	}
	// ---- 清单结构自检（探针 E：未知原语 ⇒ [META]）----
	if len(doc.Checks) == 0 {
		problems = append(problems, "[META] checks 为空")
	}
	for _, c := range doc.Checks {
		if c.ID == "" {
			problems = append(problems, "[META] 存在无 id 的判据")
			continue
		}
		if _, ok := doc.Primitives[c.Primitive]; !ok {
			problems = append(problems, fmt.Sprintf("[META] %s 引用未声明的原语 %q", c.ID, c.Primitive))
		}
	}

	// 预解码所有可解析 JSON（json_parse 判据负责报告语法错；其余判据跳过坏文件）。
	decoded := map[string]any{}
	for k, b := range files {
		var v any
		if err := json.Unmarshal(b, &v); err == nil {
			decoded[k] = v
		}
	}

	for _, c := range doc.Checks {
		problems = append(problems, runOne(c, files, decoded)...)
	}
	return problems
}

func runOne(c checkDef, files map[string][]byte, decoded map[string]any) []string {
	switch c.Primitive {
	case "json_parse":
		pat, _ := argString(c.Args, "file")
		for _, name := range globKeys(files, pat) {
			if _, err := parseJSON(files[name]); err != nil {
				return []string{fmt.Sprintf("[%s] %s JSON 语法错误：%v", c.ID, name, err)}
			}
		}
		return nil

	case "required_keys":
		pat, _ := argString(c.Args, "file")
		scope, _ := argString(c.Args, "scope")
		keys := argStrings(c.Args, "keys")
		problems := []string{}
		for _, name := range globKeys(files, pat) {
			root, ok := decoded[name]
			if !ok {
				problems = append(problems, fmt.Sprintf("[%s] %s 不可解析", c.ID, name))
				continue
			}
			node := scopeNode(root, scope)
			for _, k := range keys {
				if _, ok := asMap(node)[k]; !ok {
					problems = append(problems, fmt.Sprintf("[%s] %s 缺键 %q（scope=%q）", c.ID, name, k, scope))
				}
			}
		}
		return problems

	case "coverage":
		pat, _ := argString(c.Args, "file")
		dictPath, _ := argString(c.Args, "dict_path")
		required := argStrings(c.Args, "required")
		allowExtra := argBool(c.Args, "allow_extra", false)
		problems := []string{}
		for _, name := range globKeys(files, pat) {
			root, ok := decoded[name]
			if !ok {
				problems = append(problems, fmt.Sprintf("[%s] %s 不可解析", c.ID, name))
				continue
			}
			node := scopeNode(root, dictPath)
			dict := asMap(node)
			if dict == nil || len(dict) == 0 {
				if _, isMap := node.(map[string]any); !isMap {
					problems = append(problems, fmt.Sprintf("[%s] %s 的 dict_path %q 不是 dict 或不存在", c.ID, name, dictPath))
					continue
				}
			}
			for _, k := range required {
				if _, ok := dict[k]; !ok {
					problems = append(problems, fmt.Sprintf("[%s] %s 缺 %q", c.ID, name, k))
				}
			}
			if !allowExtra {
				for k := range dict {
					if !containsStr(required, k) {
						problems = append(problems, fmt.Sprintf("[%s] %s 出现未登记键 %q（allow_extra=false）", c.ID, name, k))
					}
				}
			}
		}
		return problems

	case "enum_subset":
		pat, _ := argString(c.Args, "file")
		collect, _ := argString(c.Args, "collect")
		allowed := argStrings(c.Args, "allowed")
		minHits := argInt(c.Args, "min_hits", 1)
		hits := collectAcross(decoded, pat, collect)
		problems := minHitsProblems(c.ID, collect, hits, minHits)
		for _, s := range stringHits(hits) {
			if !containsStr(allowed, s) {
				problems = append(problems, fmt.Sprintf("[%s] 取值 %q 不在允许集合内", c.ID, s))
			}
		}
		return problems

	case "ref_exists":
		pat, _ := argString(c.Args, "file")
		collect, _ := argString(c.Args, "collect")
		targetFile, _ := argString(c.Args, "target_file")
		targetDict, _ := argString(c.Args, "target_dict")
		extra := argStrings(c.Args, "extra_allowed")
		minHits := argInt(c.Args, "min_hits", 1)
		// N-021：可选 `split` 分隔符 —— 管道串（如 route_by_condition）先拆段再逐段校验。
		// 未设 ⇒ 行为与原先**完全一致**（向后兼容，不影响既有判据）。
		splitSep, hasSplit := argStringOpt(c.Args, "split")
		// target 键集合
		targetKeys := map[string]bool{}
		for _, tn := range globKeys(files, targetFile) {
			if root, ok := decoded[tn]; ok {
				for k := range asMap(scopeNode(root, targetDict)) {
					targetKeys[k] = true
				}
			}
		}
		for _, e := range extra {
			targetKeys[e] = true
		}
		hits := collectAcross(decoded, pat, collect)
		problems := minHitsProblems(c.ID, collect, hits, minHits)
		for _, s := range stringHits(hits) {
			refs := []string{s}
			if hasSplit {
				refs = nil
				for _, seg := range strings.Split(s, splitSep) {
					seg = strings.TrimSpace(seg)
					if seg != "" { // 空段跳过（容忍多余空格 / 尾随分隔符）
						refs = append(refs, seg)
					}
				}
			}
			for _, ref := range refs {
				if !targetKeys[ref] {
					problems = append(problems, fmt.Sprintf("[%s] 引用 %q 不存在于目标键集合", c.ID, ref))
				}
			}
		}
		return problems

	case "path_exists":
		// N-048 · 第 11 原语：锚点指针必须可解析 —— collect 收集字符串指针
		// "<spec 相对路径>#<点路径>"（省略 # ⇒ 只校验文件存在且可解析），
		// 断言：文件存在 ∧ 可 JSON 解析 ∧ 点路径命中 ≥1 节点。
		// ★ 语义与 scripts/check_spec.py#prim_path_exists 逐字对齐（两侧契约）；
		// ★ 切分规则＝括号感知（splitPtrSegs，[...] 内不按 . 切）。
		pat, _ := argString(c.Args, "file")
		collect, _ := argString(c.Args, "collect")
		minHits := argInt(c.Args, "min_hits", 1)
		hits := collectAcross(decoded, pat, collect)
		problems := minHitsProblems(c.ID, collect, hits, minHits)
		for _, h := range hits {
			ref, ok := h.(string)
			if !ok {
				problems = append(problems, fmt.Sprintf("[%s] %s 收集出现非字符串项（%T）—— 锚点必须是字符串指针", c.ID, collect, h))
				continue
			}
			ref = strings.TrimSpace(ref)
			targetPath, ptrPath, hasHash := strings.Cut(ref, "#")
			targetPath = strings.TrimSpace(targetPath)
			raw, exists := files[targetPath]
			if !exists {
				problems = append(problems, fmt.Sprintf("[%s] 锚点 %q 指向的文件不存在：%s", c.ID, ref, targetPath))
				continue
			}
			root, parsed := decoded[targetPath]
			if !parsed {
				var v any
				if err := json.Unmarshal(raw, &v); err != nil {
					problems = append(problems, fmt.Sprintf("[%s] 锚点 %q 指向的文件不可解析（%s）：%v", c.ID, ref, targetPath, err))
					continue
				}
				root = v // 预解码跳过但本文件实际可解析（理论不可达，兜底）
			}
			if !hasHash || strings.TrimSpace(ptrPath) == "" {
				continue // 省略 # ⇒ 只校验文件存在且可解析
			}
			if len(pathHits(root, normalizePtrSegs(splitPtrSegs(ptrPath)))) == 0 {
				problems = append(problems, fmt.Sprintf(
					"[%s] 锚点 %q 的点路径在 %s 内**命中 0 个节点**（字段不存在 / 改名 / 语法写错）", c.ID, ref, targetPath))
			}
		}
		return problems

	case "range_contiguous":
		pat, _ := argString(c.Args, "file")
		collect, _ := argString(c.Args, "collect")
		lowerKey, _ := argString(c.Args, "lower_key")
		upperKey, _ := argString(c.Args, "upper_key")
		minHits := argInt(c.Args, "min_hits", 1)
		hits := collectAcross(decoded, pat, collect)
		problems := minHitsProblems(c.ID, collect, hits, minHits)
		if len(problems) > 0 {
			return problems
		}
		// ★ collect 必须指向**数组本身**（V1.2 假绿教训：`bands[*]` 展开元素后本原语会瞎）。
		for _, h := range hits {
			list, ok := h.([]any)
			if !ok {
				problems = append(problems, fmt.Sprintf(
					"[%s] collect %q 的结果不是数组（若用了 [*] 展开元素，请改为指向数组本身）", c.ID, collect))
				continue
			}
			if len(list) == 0 {
				problems = append(problems, fmt.Sprintf("[%s] 区间数组为空", c.ID))
				continue
			}
			var prevUpper *int64
			for i, item := range list {
				im := asMap(item)
				lo, loNull := jsonInt64(im[lowerKey])
				hi, hiNull := jsonInt64(im[upperKey])
				if i == 0 {
					if !loNull {
						problems = append(problems, fmt.Sprintf("[%s] 首项 lower 应为 null", c.ID))
					}
				} else if prevUpper != nil {
					if loNull || lo != *prevUpper+1 {
						problems = append(problems, fmt.Sprintf(
							"[%s] 区间不连续/重叠：本项 lower=%v，上项 upper=%d", c.ID, im[lowerKey], *prevUpper))
					}
				}
				if hiNull {
					prevUpper = nil
				} else {
					prevUpper = &hi
				}
			}
			if last := asMap(list[len(list)-1]); last[upperKey] != nil {
				problems = append(problems, fmt.Sprintf("[%s] 末项 upper 应为 null", c.ID))
			}
		}
		return problems

	case "pattern_absent":
		pat, _ := argString(c.Args, "file")
		collect, _ := argString(c.Args, "collect")
		patterns := argStrings(c.Args, "forbidden_regex")
		minHits := argInt(c.Args, "min_hits", 1)
		hits := collectAcross(decoded, pat, collect)
		problems := minHitsProblems(c.ID, collect, hits, minHits)
		regs := make([]*regexp.Regexp, 0, len(patterns))
		for _, p := range patterns {
			re, err := regexp.Compile(p)
			if err != nil {
				problems = append(problems, fmt.Sprintf("[%s] 非法正则 %q：%v", c.ID, p, err))
				continue
			}
			regs = append(regs, re)
		}
		for _, s := range allStringsIn(hits) {
			for _, re := range regs {
				if re.MatchString(s) {
					problems = append(problems, fmt.Sprintf("[%s] 值 %q 命中禁止模式 %s", c.ID, s, re.String()))
				}
			}
		}
		return problems

	// set_covers（N-022 第 9 原语）：collect 取到的**标量**并成集合，断言 ⊇ required，
	// 缺哪项报哪项（CT 八组必备条款完整性 / 可写台账字段 ⊆ 白名单 —— 制度三十五/三十七条硬拦截）。
	// min_hits 与既有加固同口径：collect 命中不足 ⇒ 报「疑似清单声明有误」，不许静默通过。
	case "set_covers":
		pat, _ := argString(c.Args, "file")
		collect, _ := argString(c.Args, "collect")
		required := argScalarStrings(c.Args, "required")
		minHits := argInt(c.Args, "min_hits", 1)
		hits := collectAcross(decoded, pat, collect)
		problems := minHitsProblems(c.ID, collect, hits, minHits)
		got := map[string]bool{}
		for _, h := range hits {
			if s, ok := scalarString(h); ok {
				got[s] = true
			}
		}
		for _, need := range required {
			if !got[need] {
				problems = append(problems, fmt.Sprintf("[%s] 集合缺少必需项 %q（collect 覆盖不足）", c.ID, need))
			}
		}
		return problems

	case "array_each_required":
		// ★ N-036 ④（array_each_required · 第 10 原语）：**数组逐项条件必填** ——
		//   对 collect 收集到的**每个数组元素**，当 when_key 缺省（无条件）或
		//   元素[when_key] 的值 ∈ when_in 时，required_keys 中每个键必须**存在**。
		//   ★ 解决「非提交时点 hard 判据必须逐条声明 carried_by_kind」这类
		//   「条件必填」—— 现有原语（required_keys 作用于 dict 键、set_covers.min_hits
		//   逐文件计数）都表达不了；语义按存在性（与 required_keys 同口径）。
		pat, _ := argString(c.Args, "file")
		collect, _ := argString(c.Args, "collect")
		required := argStrings(c.Args, "required_keys")
		whenKey, _ := argStringOpt(c.Args, "when_key")
		whenIn := argStrings(c.Args, "when_in")
		// 可选第二条件（AND）：典型＝ when ∧ severity（"非提交时点的 hard 判据"需要双键，
		// 单键表达不了 —— 本原语设计时的实测需求）。
		whenKey2, _ := argStringOpt(c.Args, "when_key2")
		whenIn2 := argStrings(c.Args, "when_in2")
		// N-038 项③（V1.10 · 条件面为空守卫）：豁免开关。
		allowEmpty := argBool(c.Args, "allow_empty_match", false)
		problems := []string{}
		seen := 0
		matched := 0
		for _, name := range sortedStrKeys(decoded) {
			if !globMatch(pat, name) {
				continue
			}
			items := collectPath(decoded[name], collect)
			for i, item := range items {
				m := asMap(item)
				if m == nil {
					problems = append(problems, fmt.Sprintf("[%s] %s 的 %s 第 %d 项不是对象", c.ID, name, collect, i))
					continue
				}
				seen++
				if whenKey != "" {
					v, exists := m[whenKey]
					if !exists {
						continue // 条件键缺失 ⇒ 不在适用面（由该键自身的 required 判据管）
					}
					sv, _ := v.(string)
					if sv == "" {
						sv = fmt.Sprintf("%v", v)
					}
					if !containsStr(whenIn, sv) {
						continue
					}
				}
				if whenKey2 != "" {
					v2, exists := m[whenKey2]
					if !exists {
						continue
					}
					sv2, _ := v2.(string)
					if sv2 == "" {
						sv2 = fmt.Sprintf("%v", v2)
					}
					if !containsStr(whenIn2, sv2) {
						continue
					}
				}
				matched++
				for _, k := range required {
					if _, exists := m[k]; !exists {
						problems = append(problems, fmt.Sprintf(
							"[%s] %s %s 第 %d 项缺键 %q（条件必填未声明 —— 「声明了却没人执行」必须能被机器看见）",
							c.ID, name, collect, i, k))
					}
				}
			}
		}
		if seen == 0 {
			problems = append(problems, fmt.Sprintf("[%s] collect=%q 在 file=%q 下收集到 0 项（清单声明写错不许静默通过）",
				c.ID, collect, pat))
		}
		// N-038 项③（V1.10 · 条件面为空守卫）：collect 有项但**条件一项不匹配**
		// ⇒ 当 when_in 字面量写错（如 ["True"] vs JSON 布尔 true）时零命中零报错、
		// 判据完全空转 —— 两种病要分开报：collect 空=清单写错，条件空=**条件写错**。
		// 豁免＝args 显式 allow_empty_match:true（判据 desc 须说明依据）。
		if seen > 0 && matched == 0 && !allowEmpty {
			problems = append(problems, fmt.Sprintf(
				"[%s] 条件面为空：collect=%d 项但 when 条件匹配 0 项（疑似 when_in 字面量写错 —— 写错的条件判据完全空转；确属预期请设 allow_empty_match=true 并在 desc 说明）",
				c.ID, seen))
		}
		return problems

	case "cross_equal_by_key":
		leftGlob, _ := argString(c.Args, "left_glob")
		leftKey, _ := argString(c.Args, "left_key")
		leftValue, _ := argString(c.Args, "left_value")
		rightFile, _ := argString(c.Args, "right_file")
		rightDict, _ := argString(c.Args, "right_dict")
		rightValue, _ := argString(c.Args, "right_value")
		problems := []string{}
		// right 索引（只以 left 驱动比较 —— right 有而 left 无的键（如尚未产出的 forms）不算错）
		rightIndex := map[string][]string{}
		for _, rn := range globKeys(files, rightFile) {
			root, ok := decoded[rn]
			if !ok {
				problems = append(problems, fmt.Sprintf("[%s] %s 不可解析", c.ID, rn))
				continue
			}
			for k, entry := range asMap(scopeNode(root, rightDict)) {
				rightIndex[k] = sortedStrings(stringSlice(dig(entry, rightValue)))
			}
		}
		for _, ln := range globKeys(files, leftGlob) {
			root, ok := decoded[ln]
			if !ok {
				problems = append(problems, fmt.Sprintf("[%s] %s 不可解析", c.ID, ln))
				continue
			}
			key := scalarStr(dig(root, leftKey))
			if key == "" {
				problems = append(problems, fmt.Sprintf("[%s] %s 缺关联键 %q", c.ID, ln, leftKey))
				continue
			}
			leftVals := sortedStrings(stringSlice(dig(root, leftValue)))
			rightVals, exists := rightIndex[key]
			if !exists {
				problems = append(problems, fmt.Sprintf("[%s] 右侧缺键 %q（%s）", c.ID, key, rightDict))
				continue
			}
			if !equalStrings(leftVals, rightVals) {
				problems = append(problems, fmt.Sprintf(
					"[%s] %s 的 %s=%v 与 %s.%s[%s]=%v 不一致", c.ID, ln, leftValue, leftVals, rightDict, rightValue, key, rightVals))
			}
		}
		return problems

	default:
		return []string{fmt.Sprintf("[META] %s 引用未实现的原语 %q", c.ID, c.Primitive)}
	}
}

// ---------------------------------------------------------------------------
// 点路径 collect / glob / 参数助手
// ---------------------------------------------------------------------------

// collectAcross 对 glob 匹配的每个**已解码**文件执行点路径收集，聚合全部命中。
// （未解码文件由 json_parse/S1 判据报告，此处不重复。）
func collectAcross(decoded map[string]any, globPat, selector string) []any {
	out := []any{}
	for _, name := range sortedStrKeys(decoded) {
		if !globMatch(globPat, name) {
			continue
		}
		out = append(out, collectPath(decoded[name], selector)...)
	}
	return out
}

// collectPath 点路径收集：
//
//	段 := name | name[*] | * | **（"**.a" 拆为 "**" 与 "a"）
//
// name 取键；name[*] 取该键数组的元素；* 取全部子节点；** 递归任意深度。
// 终点命中**节点本身**（如 `thresholds.purchase.bands` 收集数组本身 —— V1.2 教训）。
func collectPath(root any, selector string) []any {
	segs := parseSelector(selector)
	if len(segs) == 0 {
		return []any{root}
	}
	out := []any{}
	var walk func(node any, segs []string)
	walk = func(node any, segs []string) {
		if len(segs) == 0 {
			out = append(out, node)
			return
		}
		seg, rest := segs[0], segs[1:]
		switch {
		case seg == "**":
			walk(node, rest) // 零宽
			switch t := node.(type) {
			case map[string]any:
				for _, v := range t {
					walk(v, segs)
				}
			case []any:
				for _, v := range t {
					walk(v, segs)
				}
			}
		case seg == "*":
			switch t := node.(type) {
			case map[string]any:
				for _, v := range t {
					walk(v, rest)
				}
			case []any:
				for _, v := range t {
					walk(v, rest)
				}
			}
		case strings.HasSuffix(seg, "[*]"):
			key := strings.TrimSuffix(seg, "[*]")
			m, ok := node.(map[string]any)
			if !ok {
				return
			}
			list, ok := m[key].([]any)
			if !ok {
				return
			}
			for _, v := range list {
				walk(v, rest)
			}
		default:
			m, ok := node.(map[string]any)
			if !ok {
				return
			}
			v, ok := m[seg]
			if !ok {
				return
			}
			walk(v, rest)
		}
	}
	walk(root, segs)
	return out
}

// parseSelector 把点路径拆为段（支持 "**.a.b" / "a[*].b[*]" / "*"）。
func parseSelector(sel string) []string {
	sel = strings.TrimSpace(sel)
	if sel == "" {
		return nil
	}
	parts := strings.Split(sel, ".")
	segs := make([]string, 0, len(parts))
	for _, p := range parts {
		if p == "" {
			continue
		}
		segs = append(segs, p)
	}
	return segs
}

// splitPtrSegs ★ path_exists 专用点路径切分（N-048 §1.3）：按 "." 切段，
// 但方括号 [...] 内部不切分 —— 两条实测必要性：
// ① 字面键可含点（spec/params.json 的 reporting.monthly_cutoff_day 等 5 键）；
// ② 选择器值可含点（checks.json#change_log[version=1.5]）。
// 与 Python 侧 check_spec.py#split_ptr_segs 逐字同规则。
func splitPtrSegs(ptr string) []string {
	segs := []string{}
	var buf strings.Builder
	depth := 0
	for _, r := range ptr {
		switch {
		case r == '[':
			depth++
			buf.WriteRune(r)
		case r == ']':
			if depth > 0 {
				depth--
			}
			buf.WriteRune(r)
		case r == '.' && depth == 0:
			if buf.Len() > 0 {
				segs = append(segs, buf.String())
				buf.Reset()
			}
		default:
			buf.WriteRune(r)
		}
	}
	if buf.Len() > 0 {
		segs = append(segs, buf.String())
	}
	return segs
}

// normalizePtrSegs 段归一（同 Python prim_path_exists 的预处理）：
//   - 尾缀形 `name[k]`（如 checks[id=x]）拆为裸键 `name` ＋ 选择/字面段 `[k]`；
//   - 独立段 `[*]` 归一为 `*`。
//
// ★ 与 Python 的顺序差异（如实登记）：Python 先 `replace("[*]","*")` 再拆尾缀，
// 会把 `name[*]` 折成裸键 `name*`（miss）；本实现先拆尾缀 ⇒ `name[*]` → `name`+`*`
// （数组元素，语义正确）。★ 实数据（institution-anchors 158 条）零 `[*]` 形态，
// 两侧在真锚点上行为一致；未来若出现 `name[*]` 指针，Python 侧需同步调整顺序。
func normalizePtrSegs(raw []string) []string {
	out := make([]string, 0, len(raw)*2)
	for _, s := range raw {
		if s == "[*]" {
			out = append(out, "*")
			continue
		}
		if i := strings.LastIndex(s, "["); i > 0 && strings.HasSuffix(s, "]") {
			name, br := s[:i], s[i:]
			if name != "" {
				out = append(out, name)
			}
			if br == "[*]" {
				out = append(out, "*")
			} else {
				out = append(out, br)
			}
			continue
		}
		out = append(out, s)
	}
	return out
}

// pathHits ★ path_exists 的指针求值（同 Python _ptr_walk）：返回命中节点列表。
// 段语义：`**` 递归 · `*`/`[*]` 全部子节点 · `[k=v]` 选择器（子节点中 k 的标量
// 值 == v）· `[字面键]` 字面键（允许含点；dict 自身与数组元素两种宿主）· 裸键。
func pathHits(node any, segs []string) []any {
	if len(segs) == 0 {
		return []any{node}
	}
	head, rest := segs[0], segs[1:]
	out := []any{}
	switch {
	case head == "**":
		out = append(out, pathHits(node, rest)...)
		for _, c := range ptrChildren(node) {
			out = append(out, pathHits(c, segs)...)
		}
	case head == "*":
		for _, c := range ptrChildren(node) {
			out = append(out, pathHits(c, rest)...)
		}
	case len(head) >= 2 && strings.HasPrefix(head, "[") && strings.HasSuffix(head, "]"):
		body := head[1 : len(head)-1]
		if k, v, hasKV := strings.Cut(body, "="); hasKV {
			for _, c := range ptrChildren(node) {
				if m, ok := c.(map[string]any); ok {
					if sv, isScalar := scalarString(m[k]); isScalar && sv == v {
						out = append(out, pathHits(c, rest)...)
					}
				}
			}
			return out
		}
		// 字面键：dict 自身优先
		if m, ok := node.(map[string]any); ok {
			if v, exists := m[body]; exists {
				return pathHits(v, rest)
			}
		}
		// 字面键：数组元素宿主（遍历子节点）
		for _, c := range ptrChildren(node) {
			if m, ok := c.(map[string]any); ok {
				if v, exists := m[body]; exists {
					out = append(out, pathHits(v, rest)...)
				}
			}
		}
	default:
		if m, ok := node.(map[string]any); ok {
			if v, exists := m[head]; exists {
				out = append(out, pathHits(v, rest)...)
			}
		}
	}
	return out
}

// ptrChildren 取子节点（dict 值 / 数组元素）。
func ptrChildren(node any) []any {
	switch t := node.(type) {
	case map[string]any:
		out := make([]any, 0, len(t))
		for _, v := range t {
			out = append(out, v)
		}
		return out
	case []any:
		return t
	}
	return nil
}

// globMatch：`*` 匹配单段内任意字符，`**` 跨段匹配（含零段）。
func globMatch(pattern, name string) bool {
	ps := strings.Split(pattern, "/")
	ns := strings.Split(name, "/")
	var m func(i, j int) bool
	m = func(i, j int) bool {
		if i == len(ps) && j == len(ns) {
			return true
		}
		if i == len(ps) {
			return false
		}
		if ps[i] == "**" {
			for k := j; k <= len(ns); k++ {
				if m(i+1, k) {
					return true
				}
			}
			return false
		}
		if j == len(ns) {
			return false
		}
		ok, err := path.Match(ps[i], ns[j])
		if err != nil || !ok {
			return false
		}
		return m(i+1, j+1)
	}
	return m(0, 0)
}

func globKeys(files map[string][]byte, pattern string) []string {
	out := []string{}
	for k := range files {
		if globMatch(pattern, k) {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// ---------------------------------------------------------------------------
// 参数 / 值工具
// ---------------------------------------------------------------------------

func argString(args map[string]json.RawMessage, key string) (string, error) {
	raw, ok := args[key]
	if !ok {
		return "", fmt.Errorf("缺参数 %q", key)
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", fmt.Errorf("参数 %q 非字符串", key)
	}
	return s, nil
}

// argStringOpt 可选字符串参数；未设返回 ("", false)（N-021 split 用）。
func argStringOpt(args map[string]json.RawMessage, key string) (string, bool) {
	raw, ok := args[key]
	if !ok {
		return "", false
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", false
	}
	return s, true
}

// argScalarStrings 读标量数组（元素可为字符串或数字 —— JSON 数字解码为 float64；
// required 里的 clause_group 可能写成 1..8 的数字）。规范化为比较用字符串。
func argScalarStrings(args map[string]json.RawMessage, key string) []string {
	raw, ok := args[key]
	if !ok {
		return nil
	}
	var list []any
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil
	}
	out := make([]string, 0, len(list))
	for _, v := range list {
		if s, ok := scalarString(v); ok {
			out = append(out, s)
		}
	}
	return out
}

// scalarString 标量 → 规范字符串（JSON 数字 1.0 → "1"）；非标量返回 false。
func scalarString(v any) (string, bool) {
	switch t := v.(type) {
	case string:
		return t, true
	case float64:
		// 整数值去小数点（clause_group 8 → "8"）
		if t == float64(int64(t)) {
			return fmt.Sprintf("%d", int64(t)), true
		}
		return fmt.Sprintf("%v", t), true
	case bool:
		return fmt.Sprintf("%v", t), true
	default:
		return "", false
	}
}

func argStrings(args map[string]json.RawMessage, key string) []string {
	raw, ok := args[key]
	if !ok {
		return nil
	}
	var list []string
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil
	}
	return list
}

func argBool(args map[string]json.RawMessage, key string, def bool) bool {
	raw, ok := args[key]
	if !ok {
		return def
	}
	var b bool
	if err := json.Unmarshal(raw, &b); err != nil {
		return def
	}
	return b
}

func argInt(args map[string]json.RawMessage, key string, def int) int {
	raw, ok := args[key]
	if !ok {
		return def
	}
	var n int
	if err := json.Unmarshal(raw, &n); err != nil {
		return def
	}
	return n
}

// minHitsProblems ★ min_hits 语义（V1.2）：collect 命中数不足即报错 ——
// 杜绝「声明写错 ⇒ 判据没跑 ⇒ 静默通过」。
func minHitsProblems(id, collect string, hits []any, minHits int) []string {
	if minHits < 1 {
		minHits = 1
	}
	if len(hits) < minHits {
		return []string{fmt.Sprintf(
			"[%s] collect %q 命中 %d < min_hits %d（声明可能写错 —— 判据未生效不许静默通过）",
			id, collect, len(hits), minHits)}
	}
	return nil
}

// scopeNode 点路径取节点（空串/空 ⇒ 根）。
func scopeNode(root any, selector string) any {
	hits := collectPath(root, selector)
	if len(hits) == 0 {
		return nil
	}
	return hits[0]
}

// dig 按单键/嵌套点路径取值（target_dict 已由 scopeNode 处理；此处支持 "a.b" 形式）。
func dig(node any, keyPath string) any {
	cur := node
	for _, seg := range strings.Split(keyPath, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur, ok = m[seg]
		if !ok {
			return nil
		}
	}
	return cur
}

func stringHits(hits []any) []string {
	out := []string{}
	for _, h := range hits {
		if s, ok := h.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// allStringsIn 递归收集命中节点内的全部字符串（pattern_absent 的「递归收集」）。
func allStringsIn(hits []any) []string {
	out := []string{}
	var walk func(v any)
	walk = func(v any) {
		switch t := v.(type) {
		case string:
			out = append(out, t)
		case map[string]any:
			for _, x := range t {
				walk(x)
			}
		case []any:
			for _, x := range t {
				walk(x)
			}
		}
	}
	for _, h := range hits {
		walk(h)
	}
	return out
}

func stringSlice(v any) []string {
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

func scalarStr(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func jsonInt64(v any) (int64, bool) {
	if v == nil {
		return 0, true // null
	}
	f, ok := v.(float64)
	if !ok {
		return 0, false
	}
	return int64(f), false
}

func containsStr(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func sortedStrings(in []string) []string {
	out := append([]string{}, in...)
	sort.Strings(out)
	return out
}

func equalStrings(a, b []string) bool {
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

func sortedStrKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func parseJSON(b []byte) (any, error) {
	var v any
	err := json.Unmarshal(b, &v)
	return v, err
}
