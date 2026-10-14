package permission

import (
	"encoding/json"
	"reflect"
	"strings"
)

// Project 列级投影：在序列化阶段裁剪字段，无权限字段「连字段名都不出现」（TC-07）。
//
// 规则（架构 §5.3）：
//   - column_deny（黑名单）优先于 column_allow（白名单）；
//   - 敏感列（is_sensitive=1，如金额）默认进入 deny，除非本角色规则显式 allow；
//   - column_allow 为空表示「除 deny 外全部允许」。
func Project(row map[string]any, allow, deny []string, sensitive map[string]bool) map[string]any {
	return ProjectDeep(row, allow, deny, sensitive)
}

// ProjectDeep 递归版列投影（B19）。
//
// ★ 为什么不只是顶层投影：行里可能存在**嵌套块**——台账行的 `formula_flags`、变更链的
// `archive`（原样回出的 ext_json）、看板的 `Supervision`。只投影顶层时，嵌套块里的
// 金额/敏感标量会**原样泄漏**（且不报错）——这正是 B19 记录的形态。
//
// ★ 递归语义刻意**不对称**，这是安全与可用性的取舍：
//   - **deny 与敏感列：在任意层级生效** —— 这两类属于「必须消失」，漏掉即越权；
//   - **allow 白名单：只在顶层生效** —— allow 是"这一行保留哪几列"的清单；
//     递归套用会把嵌套块的结构键（如 `supplier_month_sum` 这类容器名）一并删掉，
//     反而丢掉合法数据。故嵌套层只做 deny 收缩。
//
// ★ **金额同义键**（2026-09-26 补，B31）：deny 集里写的是 `amount_cents` 这类**规范键名**，
// 但同一笔金额在不同接口可能以别的键名出现（变更链的 `change_cents` /
// `original_cents` / `change_display` / `original_display`）。若只按字面 deny 匹配，
// 这些同义键会**照常返回**——「禁金额角色仍能读到金额」，是实打实的越权。
// 故当规则禁掉了规范金额键时，**一并裁掉所有金额类键名**（`*_cents` / `*_display` /
// `amount` / `formula_flags`，任意层级）。
func ProjectDeep(row map[string]any, allow, deny []string, sensitive map[string]bool) map[string]any {
	denySet := toSet(deny)
	allowSet := toSet(allow)
	hasAllow := len(allowSet) > 0
	amountsDenied := AmountsDenied(allowSet, denySet, sensitive)

	out := make(map[string]any, len(row))
	for k, v := range row {
		if !keepKey(k, allowSet, denySet, hasAllow, sensitive) {
			continue
		}
		if amountsDenied && isAmountKey(k) {
			continue
		}
		out[k] = projectNested(v, denySet, sensitive, amountsDenied)
	}
	return out
}

// AmountsDenied 判断该规则是否**禁看金额**（金额类键名不得出现在任何层级）。
//
// 判据：规范金额键 `amount_cents` 被 deny，或它被标为敏感而本角色未显式 allow。
// 供调用方决定是否额外输出「金额已隐藏」之类的提示字段。
func AmountsDenied(allowSet, denySet map[string]bool, sensitive map[string]bool) bool {
	if denySet["amount_cents"] {
		return true
	}
	if sensitive["amount_cents"] && !allowSet["amount_cents"] {
		return true
	}
	return false
}

// IsAmountKey 导出 isAmountKey 供包外复用（权限矩阵「字段值域目录」标注金额类键名）。
//
// ★ 为什么导出而不是在调用方重写一遍：金额类键名的识别规则是**安全判据**
//
//	（禁金额角色依赖它在任意层级裁键）—— 抄一份到 httpapi 就是第二份真相，
//	它一旦与这里漂移，「目录标注的金额键」与「真正被裁的键」就会不一致。
func IsAmountKey(k string) bool {
	return isAmountKey(k)
}

// amountKeySuffixes 金额类键名的识别规则（小写、后缀匹配）。
var amountKeySuffixes = []string{"_cents", "_display"}

// isAmountKey 判断键名是否为**金额类**（用于禁金额角色的一并裁剪，见 ProjectDeep 注释）。
//
// 用「后缀规则」而非白名单枚举，是为了让**将来新增的金额键名自动被覆盖**——
// 白名单枚举过一处漏一处，而这个函数的漏项就是一次越权。
func isAmountKey(k string) bool {
	k = strings.ToLower(strings.TrimSpace(k))
	if k == "" {
		return false
	}
	if k == "formula_flags" || k == "amount" {
		return true
	}
	for _, suf := range amountKeySuffixes {
		if strings.HasSuffix(k, suf) {
			return true
		}
	}
	return false
}

// keepKey 顶层单键的保留判定（与 Project 原语义一致）。
func keepKey(k string, allowSet, denySet map[string]bool, hasAllow bool, sensitive map[string]bool) bool {
	if denySet[k] {
		return false // deny 优先
	}
	if sensitive[k] {
		return allowSet[k] // 敏感列需显式 allow
	}
	if hasAllow && !allowSet[k] {
		return false
	}
	return true
}

// projectNested 递归裁剪嵌套结构：只施加 deny 与敏感列两类规则（不做 allow 白名单）。
//
// ★★ N-079（治本）：**不再按具体类型枚举**。原实现只识别 `map[string]any` / `[]any` /
// `[]map[string]any` 三类，其余一律 `default: return v`（**原样返回、不裁列、不报错**）——
// 于是**每一种新容器类型**（typed slice `[]T` / `[]*T` / array / 裸 struct / 非 string-key map…）
// 都会**静默落 `default`**，其内的金额类键照漏（与 `S16`「部分覆盖冒充全部覆盖」同族）。
// ⇒ 现按 `reflect.Kind` 判定：**凡「容器类」（map / slice / array / struct / 指针）**
// 一律先经 **JSON 往返归一** 为 `map[string]any` / `[]any` / 标量，**再递归投影** ——
// 归一后的形态**与最终线缆 JSON 逐字一致**（`time.Time`→字符串、`[]byte`→base64、自定义
// `MarshalJSON` 被尊重）⇒ **语义不丢**，且金额键在任意容器内都会被裁掉。
// ★ 为什么不「反射直接改结构体字段」：无法安全重建带未导出字段 / 自定义序列化器的类型
// （典型 `time.Time`：反射取不到字段值、重建即失真），而 **JSON 往返把「如何序列化」交回
// 标准库这一唯一权威**。
func projectNested(v any, denySet, sensitive map[string]bool, amountsDenied bool) any {
	switch t := v.(type) {
	case nil:
		return nil
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, vv := range t {
			if denySet[k] || sensitive[k] {
				continue
			}
			if amountsDenied && isAmountKey(k) {
				continue
			}
			out[k] = projectNested(vv, denySet, sensitive, amountsDenied)
		}
		return out
	case []any:
		out := make([]any, 0, len(t))
		for _, e := range t {
			out = append(out, projectNested(e, denySet, sensitive, amountsDenied))
		}
		return out
	case []map[string]any:
		out := make([]any, 0, len(t))
		for _, e := range t {
			out = append(out, projectNested(e, denySet, sensitive, amountsDenied))
		}
		return out
	default:
		// ★ 容器类（未在上列显式枚举的 map/slice/array/struct/指针）：JSON 往返归一后再递归
		//   —— 从根上消除「按具体类型枚举」的漏面：**任何新容器类型都不会再静默落 `default`**。
		if rv := reflect.ValueOf(v); rv.IsValid() && isContainerKind(rv.Kind()) {
			if norm, ok := jsonNormalize(v); ok {
				return projectNested(norm, denySet, sensitive, amountsDenied)
			}
		}
		// 标量（string / number / bool …）原样：**无键可漏**；
		// 不可 JSON 序列化的值（chan / func）原样 ⇒ 序列化阶段必然**可见失败**（非静默放过）。
		return v
	}
}

// isContainerKind 判定某 `reflect.Kind` 是否为「可能承载任意键、**必须递归投影**」的容器类。
//
// ★ 这是 N-079 机制的**唯一白名单**：任何**容器类** Kind 必须在此集合内，否则其内键值
// 不会被裁剪（漏面）。★ 标量类（string / int / float / bool / complex）无键可漏，天然安全。
// ★ 由 `TestProjectDeepContainerKindWhitelist` 逐 Kind 机检（白名单少一类 ⇒ **恰红**）。
func isContainerKind(k reflect.Kind) bool {
	switch k {
	case reflect.Map, reflect.Slice, reflect.Array, reflect.Struct, reflect.Pointer:
		return true
	default:
		return false
	}
}

// jsonNormalize 把任意值与其**线缆 JSON 形态**对齐：`Marshal` → `Unmarshal` into `any`。
//
// ★ 用标准库作「如何序列化」的唯一权威（尊重 `MarshalJSON`、时间 / 字节 / 字段标签语义）。
// 第二个返回值＝是否成功；**不可序列化**的类型会失败 ⇒ 由调用方原样保留，
// 该值在序列化阶段必然**可见失败**（非静默放过）。
func jsonNormalize(v any) (any, bool) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, false
	}
	var out any
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, false
	}
	return out, true
}

// RuleHidesAmounts 判断「角色 × 规则」是否禁看金额（供 handler 决定是否输出"金额已隐藏"提示）。
func RuleHidesAmounts(rule Rule) bool {
	return AmountsDenied(toSet(rule.ColumnAllow), toSet(rule.ColumnDeny), nil)
}

// CanWrite 判断某业务字段对该规则是否可写（仅运营表可写字段，TC-31）。
func CanWrite(rule Rule, field string) bool {
	for _, f := range rule.WritableFields {
		if strings.EqualFold(strings.TrimSpace(f), strings.TrimSpace(field)) {
			return true
		}
	}
	return false
}

// IsDenyAll 判断规则是否对全部行拒绝（无规则 / DENY 令牌）。
func IsDenyAll(rule Rule) bool {
	return rule.RowScope == ScopeDeny || strings.TrimSpace(string(rule.RowScope)) == ""
}

func toSet(in []string) map[string]bool {
	out := make(map[string]bool, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s != "" {
			out[s] = true
		}
	}
	return out
}
