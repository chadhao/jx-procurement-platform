package httpapi

// GET /api/admin/permission-column-catalog —— 「字段值域目录」（权限矩阵填表辅助）。
//
// ★ 动因（用户原话）：「我都不清楚黑名单、白名单和可写字段允许的值分别都有什么，
//   且每个是什么含义，很难填这些空。」⇒ 规则明细的三列此前是**逗号分隔文本框**，
//   且两套命名混用（列黑/白名单＝英文键、可写字段＝中文列名）而**无合法值提示**。
//
// ★★ 合法值与中文标签的权威来源＝**运行时的 specload**（`Bundle.Forms`，key=doc_type；
//   每个 `fields[]` 有 `name`（英文键）＋ `label`（中文名））—— ★ **不新增 spec 文件**：
//   另立一份字段清单＝制造第二份真相，必然与 spec 漂移。
//
// ★★ catalog 必须与「真实可填值」一致：除表单字段外，还与**既有权限规则的取值**并对 ——
//   · 金额类台账键 `amount_display` / `formula_flags` **不在任何表单里**（它们在列投影的
//     递归裁剪语义里生效，policy.go:31-36），但确实是合法的 `column_deny` 值；
//   · `writable_fields` 的取值是**中文列名**（seed 的运营可写清单），同样不是表单字段。
//   ⇒ 只从 Forms 提取会漏掉这两类「真在填」的值，故以规则侧取值补并。
//
// ★ 本端点**只读、不参与保存校验**：PUT /api/admin/permission-rules 的请求结构与值域
//   校验零变化（仍只校验 row_scope 白名单）—— 目录是「填表辅助」，不是新约束。

import (
	"net/http"
	"sort"
	"strings"

	"github.com/labstack/echo/v4"

	"github.com/chadhao/jx-procurement-platform/internal/permission"
	"github.com/chadhao/jx-procurement-platform/internal/specload"
	"github.com/chadhao/jx-procurement-platform/internal/store"
)

// 字段类别（catalog#items[].kinds 的取值；含义见 kinds_legend）。
const (
	// kindMoney 金额类键名 —— 判定与 internal/permission.isAmountKey **同一函数**
	// （policy.go:31-36：`amount` / `formula_flags` / `*_cents` / `*_display`，
	// 被「禁金额」角色在【任意层级】一律裁掉）。
	kindMoney = "money"
	// kindData 普通数据列 —— 来自机读规格 spec/forms 的单据字段（附中文标签）。
	kindData = "data"
	// kindWritable 可写业务列 —— 已配置在既有规则的 writable_fields 中
	// （允许在台账里修改；取值为中文列名）。
	kindWritable = "writable"
	// kindLedger 台账键 —— 来自既有规则配置、非表单规格字段（如集团侧人工登记列；
	// 可能无中文标签，按原键使用）。
	kindLedger = "ledger"
)

// kindDisplayOrder kinds 的固定输出序（稳定渲染；不随 map 遍历序漂移）。
var kindDisplayOrder = []string{kindMoney, kindData, kindWritable, kindLedger}

// columnCatalogLabelConflict 同名不同标签的如实登记（不静默挑一个）。
type columnCatalogLabelConflict struct {
	Form  string `json:"form"`  // 出现该标签的表单（doc_type）
	Label string `json:"label"` // 该表单下的中文标签
}

// columnCatalogItem 目录条目（按 key 归并的全量字段）。
type columnCatalogItem struct {
	Key           string                       `json:"key"`            // 英文键（column_allow/deny 的取值）
	Label         string                       `json:"label"`          // 首个非空中文标签（无则空串）
	Kinds         []string                     `json:"kinds"`          // 类别（money/data/writable/ledger 的子集）
	Forms         []string                     `json:"forms"`          // 出现该字段的表单（doc_type，排序）
	LabelConflict []columnCatalogLabelConflict `json:"label_conflict"` // 同名不同标签（首个之外的全部）
}

// columnCatalogKind 类别图例（kind → 中文含义）。
type columnCatalogKind struct {
	Kind    string `json:"kind"`
	Meaning string `json:"meaning"`
}

// columnCatalogKindsLegend 类别图例（语义与 policy.go / seed 口径一致）。
func columnCatalogKindsLegend() []columnCatalogKind {
	return []columnCatalogKind{
		{Kind: kindMoney, Meaning: "金额类键名（`amount` / `formula_flags` / `*_cents` / `*_display`）—— 与列投影规则同口径：被「禁金额」角色在【任意层级】一律裁掉"},
		{Kind: kindData, Meaning: "普通数据列：来自机读规格（spec/forms）的单据字段，附中文标签"},
		{Kind: kindWritable, Meaning: "可写业务列：已配置在权限规则的「可写字段」中，允许在台账里修改；取值为中文列名"},
		{Kind: kindLedger, Meaning: "台账键：来自既有权限规则配置、非表单规格字段（可能无中文标签，按原键使用）"},
	}
}

// handleAdminColumnCatalogGet 读取字段值域目录（admin 组 + requireSysAdmin，与 §3.9 同口径）。
func (d Deps) handleAdminColumnCatalogGet(c echo.Context) error {
	if _, ok := d.requireSysAdmin(c); !ok {
		return nil
	}
	if d.Spec == nil {
		return fail(c, http.StatusServiceUnavailable, codeNotReady, "机读规格未装配")
	}
	rules, err := d.DB.ListPermissionRules(c.Request().Context())
	if err != nil {
		return fail(c, http.StatusInternalServerError, codeInternal, err.Error())
	}
	return ok(c, map[string]any{
		"items":        buildColumnCatalog(d.Spec, rules),
		"kinds_legend": columnCatalogKindsLegend(),
	})
}

// buildColumnCatalog 聚合字段值域目录（纯函数：spec ＋ 既有规则 ⇒ 有序 items）。
//
// 归并口径：
//   - 按 key（英文键）归并；label 取**首个非空**中文标签（doc_type 字典序遍历 ⇒ 确定性）；
//     同名不同标签 ⇒ 首个之后的全部如实进 label_conflict（**不静默挑一个**）。
//   - forms：该字段出现的全部 doc_type（去重、排序）。
//   - kinds：money（IsAmountKey，与 policy.go 同一函数）· data（来自 Forms）·
//     writable（出现在任一规则 writable_fields）· ledger（仅在规则侧出现、不在 Forms）。
func buildColumnCatalog(bundle *specload.Bundle, rules []store.PermissionRule) []columnCatalogItem {
	type agg struct {
		label    string
		forms    map[string]bool
		conflict []columnCatalogLabelConflict
		fromForm bool
	}
	aggs := map[string]*agg{}
	writableKeys := map[string]bool{}
	ruleOnlyKeys := map[string]bool{} // 仅在规则侧出现的键（allow/deny）

	// ① Forms 侧：全量字段（权威的中文标签来源）。
	docTypes := make([]string, 0, len(bundle.Forms))
	for dt := range bundle.Forms {
		docTypes = append(docTypes, dt)
	}
	sort.Strings(docTypes) // BA, BJ, CT, GR, PC, PR, QC, RFQ, SA, SS, SUB —— 稳定归并序
	for _, dt := range docTypes {
		for _, sec := range bundle.Forms[dt].Sections {
			for _, f := range sec.Fields {
				key := strings.TrimSpace(f.Name)
				if key == "" {
					continue
				}
				a, ok := aggs[key]
				if !ok {
					a = &agg{forms: map[string]bool{}}
					aggs[key] = a
				}
				a.forms[dt] = true
				a.fromForm = true
				label := strings.TrimSpace(f.Label)
				if label == "" {
					continue
				}
				if a.label == "" {
					a.label = label // 首个非空
					continue
				}
				if label != a.label {
					a.conflict = append(a.conflict, columnCatalogLabelConflict{Form: dt, Label: label})
				}
			}
		}
	}

	// ② 规则侧：既有取值（「真实可填值」的另一半来源）。
	for _, r := range rules {
		for _, v := range r.WritableFields {
			if k := strings.TrimSpace(v); k != "" {
				writableKeys[k] = true
			}
		}
		for _, v := range append(append([]string{}, r.ColumnAllow...), r.ColumnDeny...) {
			if k := strings.TrimSpace(v); k != "" {
				ruleOnlyKeys[k] = true
			}
		}
	}

	items := make([]columnCatalogItem, 0, len(aggs)+len(writableKeys)+len(ruleOnlyKeys))
	for key, a := range aggs {
		items = append(items, columnCatalogItem{
			Key:           key,
			Label:         a.label,
			Kinds:         kindsOf(key, a.fromForm, writableKeys[key]),
			Forms:         sortedKeys(a.forms),
			LabelConflict: a.conflict,
		})
	}
	// 规则侧独有键（不在 Forms）：可写中文列名 / 金额类台账键 / 集团侧人工登记列等。
	seen := map[string]bool{}
	for key := range aggs {
		seen[key] = true
	}
	ruleOnly := map[string]bool{}
	for k := range writableKeys {
		ruleOnly[k] = true
	}
	for k := range ruleOnlyKeys {
		ruleOnly[k] = true
	}
	for key := range ruleOnly {
		if seen[key] {
			continue
		}
		label := ""
		if writableKeys[key] {
			label = key // 可写字段取值本身就是中文列名（自标签）
		}
		items = append(items, columnCatalogItem{
			Key:           key,
			Label:         label,
			Kinds:         kindsOf(key, false, writableKeys[key]),
			Forms:         []string{},
			LabelConflict: nil,
		})
	}

	sort.Slice(items, func(i, j int) bool { return items[i].Key < items[j].Key })
	for i := range items {
		if items[i].LabelConflict == nil {
			items[i].LabelConflict = []columnCatalogLabelConflict{}
		}
	}
	return items
}

// kindsOf 计算单键的类别集合（固定序）。
func kindsOf(key string, fromForm, writable bool) []string {
	kinds := make([]string, 0, 3)
	if permission.IsAmountKey(key) {
		kinds = append(kinds, kindMoney)
	}
	if fromForm {
		kinds = append(kinds, kindData)
	}
	if writable {
		kinds = append(kinds, kindWritable)
	}
	if !fromForm {
		kinds = append(kinds, kindLedger)
	}
	// 固定序（kindDisplayOrder）输出，避免集合序漂移。
	ordered := make([]string, 0, len(kinds))
	for _, k := range kindDisplayOrder {
		for _, got := range kinds {
			if got == k {
				ordered = append(ordered, k)
				break
			}
		}
	}
	return ordered
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
