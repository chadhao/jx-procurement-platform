package config

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/chadhao/jx-procurement-platform/internal/store"
)

// 配置映射导入（PRD Q1 闭合路径）。
//
// ★ 背景：11 张审批模板**必须人工在飞书审批后台建**（开放平台原文「API 方式不支持设置条件分支」，
// 且 API 建的审批定义无法停用/删除、官方不推荐）。模板建好后会产生两组「只有人能看到」的值：
//
//	① approval_code（每张模板一个）；
//	② 各模板表单控件的 field_id。
//
// 这两组值按纪律 7/8 **不得硬编码进代码**，一律落 `t_config_mapping`。本文件提供**带校验的导入**，
// 免去手工写 SQL（那是唯一曾有其它写法的口子，也是错一处就全线静默无数据的地方）。
//
// 导入是**幂等**的：同 (map_kind, map_key, doc_type) 覆盖更新，可反复执行。

// DocTypes 11 张单据的编号前缀（权威：PRD §6.1 / 工具表·单据清单）。
// ★ 顺序即业务顺序，不参与排序，仅作白名单校验。
var DocTypes = []string{"BA", "PR", "SA", "RFQ", "BJ", "SS", "CT", "PC", "GR", "QC", "SUB"}

// LedgerTypes 12 张台账语义键（权威：架构 §3.4）。
var LedgerTypes = []string{"L01", "L02", "L03", "L04", "L05", "L06", "L07", "L08", "L09", "L10", "L11", "L12"}

// PerInstanceLedgerTypes 可承载「实例级落一行」的台账键。
//
// ★ 为什么必须区分：`doc_type → ledger_type` 的映射结果会被写入 `t_ledger_archive`
// **每个实例一行**（`internal/worker/ingest.go`）。但架构 §3.4 中并非每张台账都是实例级的：
//
//	L08 供应商档案与绩效表 —— 「本身可写」，是**手工维护的主数据**，不随单据产生行；
//	L10 用途分类汇总台账   —— 「只读」**汇总**，行由其他台账聚合而来；
//	L11 订单执行台账       —— 「数据源＝CT + GR」，是**派生**表；
//	L12 预算执行台账       —— 「保留结构 / 本期不启用」。
//
// 若把单据映射到上述任一键，就会往汇总/主数据台账里逐单插行 —— 结果是**台账数字翻倍、
// 且看不出哪里错了**（同一类"静默错数据"缺陷）。故此处在导入层直接拦死。
var PerInstanceLedgerTypes = []string{"L01", "L02", "L03", "L04", "L05", "L06", "L07", "L09"}

// aggregateLedgerHint 给出非实例级台账的成因说明（用于错误信息，避免使用者以为是笔误）。
var aggregateLedgerHint = map[string]string{
	"L08": "供应商档案与绩效表是手工维护的主数据，不由单据产生行",
	"L10": "用途分类汇总台账是只读汇总，行由其他台账聚合而来",
	"L11": "订单执行台账的数据源＝CT + GR（Q6），属派生表",
	"L12": "预算执行台账本期不启用",
}

// ImportApprovalCode approval_code → 单据类型。
type ImportApprovalCode struct {
	Code    string `json:"code"`
	DocType string `json:"doc_type"`
	Remark  string `json:"remark"`
}

// ImportFieldID 表单控件 field_id → 业务字段名（doc_type 为空表示全局映射）。
type ImportFieldID struct {
	DocType   string `json:"doc_type"`
	FieldID   string `json:"field_id"`
	FieldName string `json:"field_name"`
	BizField  string `json:"biz_field"`
}

// ImportKV 通用键值项（台账类型映射 / 阈值）。
type ImportKV struct {
	Key    string `json:"key"`
	Value  string `json:"value"`
	Remark string `json:"remark"`
}

// ImportLedgerField 台账字段定义（`t_ledger_field_def`）。
//
// ★ 只开放**有消费端**的三项（纪律 J-9）：
//   - `ledger_type` + `field_key` → 被 `PATCH /api/ledger/{table}/{id}` 用作 `fields` 键名白名单；
//   - `is_sensitive`             → 被 `SensitiveFields` 用作列级权限兜底。
//
// ★ `field_label` / `is_formula` / `formula_kind` **刻意不开放导入**：当前代码不读它们
// （公式红标由 `formulaFlags` 在代码里现算），放进来就是"配了没人读"的假配置。
// 待将来真正数据驱动公式列时再一并开放。
type ImportLedgerField struct {
	LedgerType  string `json:"ledger_type"`
	FieldKey    string `json:"field_key"`
	IsSensitive bool   `json:"is_sensitive"`
}

// ImportPayload 导入载荷（一个 JSON 对象装齐五类映射）。
type ImportPayload struct {
	ApprovalCode []ImportApprovalCode `json:"approval_code"`
	FieldID      []ImportFieldID      `json:"field_id"`
	LedgerType   []ImportKV           `json:"ledger_type"`
	Threshold    []ImportKV           `json:"threshold"`
	LedgerField  []ImportLedgerField  `json:"ledger_field"`
}

// ImportResult 导入结果计数。
type ImportResult struct {
	ApprovalCode int
	FieldID      int
	LedgerType   int
	Threshold    int
	LedgerField  int

	// ReplacedKinds 本次被**全量替换**（先清空再写入）的映射类；
	// EmptyKinds 在载荷中为空、因而**未被替换**（保留表中既有）的映射类。
	//
	// ★ 为什么要暴露：全量替换是「配置以文件为准」的唯一正确实现（见 ImportMappings 注释），
	//   但若某个类在文件中是空的，静默不动它同样是隐患 —— 调用方必须把 EmptyKinds 打出来。
	ReplacedKinds []string
	EmptyKinds    []string
}

// Total 返回导入条目总数。
func (r ImportResult) Total() int {
	return r.ApprovalCode + r.FieldID + r.LedgerType + r.Threshold + r.LedgerField
}

func inList(v string, list []string) bool {
	v = strings.TrimSpace(v)
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// placeholderMarkers 未填写占位符的标记。
//
// ★ 为什么必须拦：样例文件若被**原样导入**，会写入一批指向「不存在的 approval_code」的映射。
// 其后果不是报错——而是**模板永远订阅不到事件、系统一条数据都没有、且没有任何错误日志**。
// 这正是本项目最危险的失败模式（静默无数据），故在导入层直接拦死。
var placeholderMarkers = []string{"REPLACE_ME", "替换", "TODO:", "<待填>"}

// isPlaceholder 判断取值是否为未替换的占位符。
func isPlaceholder(v string) bool {
	v = strings.TrimSpace(v)
	if v == "" {
		return false
	}
	for _, m := range placeholderMarkers {
		if strings.Contains(v, m) {
			return true
		}
	}
	return false
}

// Validate 严格校验导入载荷；任一条不合规即整体拒绝（**不部分导入**，避免半个映射上线的静默错配）。
func (p *ImportPayload) Validate() error {
	if p == nil {
		return fmt.Errorf("导入载荷为空")
	}

	seen := map[string]bool{}
	for i, e := range p.ApprovalCode {
		code := strings.TrimSpace(e.Code)
		if code == "" {
			return fmt.Errorf("approval_code[%d]：code 不能为空", i)
		}
		if isPlaceholder(code) {
			return fmt.Errorf("approval_code[%d]（%s）：仍是未替换的占位符——请填入飞书审批后台的真实 approval_code", i, code)
		}
		if !inList(e.DocType, DocTypes) {
			return fmt.Errorf("approval_code[%d]（%s）：doc_type 必须是 %s 之一，实际 %q",
				i, code, strings.Join(DocTypes, "/"), e.DocType)
		}
		k := "ac\x00" + code
		if seen[k] {
			return fmt.Errorf("approval_code 重复：%s", code)
		}
		seen[k] = true
	}

	for i, e := range p.FieldID {
		fid := strings.TrimSpace(e.FieldID)
		if fid == "" {
			return fmt.Errorf("field_id[%d]：field_id 不能为空", i)
		}
		if isPlaceholder(fid) {
			return fmt.Errorf("field_id[%d]（%s）：仍是未替换的占位符——请填入模板中控件的真实 field_id", i, fid)
		}
		if strings.TrimSpace(e.BizField) == "" {
			return fmt.Errorf("field_id[%d]（%s）：biz_field 不能为空（否则该字段落库但不进业务列）", i, fid)
		}
		dt := strings.TrimSpace(e.DocType)
		if dt != "" && !inList(dt, DocTypes) {
			return fmt.Errorf("field_id[%d]（%s）：doc_type 必须为空或 %s 之一，实际 %q",
				i, fid, strings.Join(DocTypes, "/"), e.DocType)
		}
		k := "fid\x00" + dt + "\x00" + fid
		if seen[k] {
			return fmt.Errorf("field_id 重复：(doc_type=%q, field_id=%q)", dt, fid)
		}
		seen[k] = true
	}

	for i, e := range p.LedgerType {
		if !inList(e.Key, DocTypes) {
			return fmt.Errorf("ledger_type[%d]：key（doc_type）必须是 %s 之一，实际 %q",
				i, strings.Join(DocTypes, "/"), e.Key)
		}
		if !inList(e.Value, LedgerTypes) {
			return fmt.Errorf("ledger_type[%d]（%s）：value 必须是 %s 之一，实际 %q",
				i, e.Key, strings.Join(LedgerTypes, "/"), e.Value)
		}
		if !inList(e.Value, PerInstanceLedgerTypes) {
			hint := aggregateLedgerHint[strings.TrimSpace(e.Value)]
			if hint == "" {
				hint = "该台账不是实例级台账"
			}
			return fmt.Errorf("ledger_type[%d]（%s → %s）：%s，不能作为单据的落账目标；"+
				"可落账的键为 %s", i, e.Key, e.Value, hint, strings.Join(PerInstanceLedgerTypes, "/"))
		}
		// ★ 唯一性键为 (doc_type, ledger_type) —— 一对多（B47）：
		//   同一 doc_type 允许多个台账（如 PR → L02 + L03），但同一对不允许重复。
		k := "lt\x00" + strings.TrimSpace(e.Key) + "\x00" + strings.TrimSpace(e.Value)
		if seen[k] {
			return fmt.Errorf("ledger_type 重复：(doc_type=%q, ledger_type=%q)", e.Key, e.Value)
		}
		seen[k] = true
	}

	for i, e := range p.Threshold {
		key := strings.TrimSpace(e.Key)
		if key == "" {
			return fmt.Errorf("threshold[%d]：key 不能为空", i)
		}
		if !validThresholdValue(e.Value) {
			return fmt.Errorf("threshold[%d]（%s）：value 必须是数字或「左-右」区间，实际 %q", i, key, e.Value)
		}
		k := "th\x00" + key
		if seen[k] {
			return fmt.Errorf("threshold 重复：%s", key)
		}
		seen[k] = true
	}

	for i, e := range p.LedgerField {
		lt := strings.TrimSpace(e.LedgerType)
		if !inList(lt, LedgerTypes) {
			return fmt.Errorf("ledger_field[%d]：ledger_type 必须是 %s 之一，实际 %q",
				i, strings.Join(LedgerTypes, "/"), e.LedgerType)
		}
		fk := strings.TrimSpace(e.FieldKey)
		if fk == "" {
			return fmt.Errorf("ledger_field[%d]（%s）：field_key 不能为空", i, lt)
		}
		if isPlaceholder(fk) {
			return fmt.Errorf("ledger_field[%d]（%s）：field_key 仍是未替换的占位符", i, fk)
		}
		k := "lf\x00" + lt + "\x00" + fk
		if seen[k] {
			return fmt.Errorf("ledger_field 重复：(ledger_type=%q, field_key=%q)", lt, fk)
		}
		seen[k] = true
	}
	return nil
}

// validThresholdValue 校验阈值取值：单值（1000）或区间（800-1000）。
func validThresholdValue(v string) bool {
	v = strings.TrimSpace(v)
	if v == "" {
		return false
	}
	if _, err := strconv.ParseFloat(v, 64); err == nil {
		return true
	}
	parts := strings.SplitN(v, "-", 2)
	if len(parts) != 2 {
		return false
	}
	lo, err1 := strconv.ParseFloat(strings.TrimSpace(parts[0]), 64)
	hi, err2 := strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
	return err1 == nil && err2 == nil && lo <= hi
}

// ConsumedThresholdKeys 当前代码**真正读取**的阈值键。
//
// ★ 为什么必须登记：不在本表中的阈值键写进库后**不会改变任何行为**——这是「**假配置**」：
// 运维以为把 `purchase_tier` 设成 `1000-5000` 就实现了分档，实际代码从未读它。
// 与决策 #10（`api:petty-cash` 不入权限矩阵，避免"矩阵可配、处理器更严"的假配置）同一类问题。
//
// 新增阈值键时**必须同时有消费端**（否则等于重现 docs/06 §J.2 的 P0-C：配了、没人读）。
var ConsumedThresholdKeys = map[string]bool{
	"split_supplier_month": true, // dashboard 防拆分视图 + handlers_biz 台账红标
	"spot_check_range":     true, // handlers_biz 抽查区间红标
}

// UnconsumedThresholdKeys 返回载荷中「已登记但当前无消费端」的阈值键（升序、去重）。
func (p *ImportPayload) UnconsumedThresholdKeys() []string {
	if p == nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, e := range p.Threshold {
		k := strings.TrimSpace(e.Key)
		if k == "" || seen[k] || ConsumedThresholdKeys[k] {
			continue
		}
		seen[k] = true
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ParseImportPayload 解析并校验导入载荷。
func ParseImportPayload(data []byte) (*ImportPayload, error) {
	var p ImportPayload
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return nil, fmt.Errorf("解析配置映射失败: %w", err)
	}
	if err := p.Validate(); err != nil {
		return nil, err
	}
	return &p, nil
}

// LoadImportFile 从文件读取并校验导入载荷。
func LoadImportFile(path string) (*ImportPayload, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取配置文件失败: %w", err)
	}
	p, err := ParseImportPayload(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return p, nil
}

// ImportMappings 导入五类映射。
//
// ★ 语义：**全量替换「本载荷中出现的映射类」** —— 该类在表内先清空、再按载荷写入，
// 保证「表中内容 == 文件内容」。整个导入在**一个事务**内完成，任一步失败即整体回滚。
//
//   - **为什么必须全量替换**：migration 0006 把 `map_value` 纳入唯一键（为支持
//     「`doc_type` → 多个台账」，B47 修复），于是**改一个 value 会变成「新增一行」
//     而不是「覆盖」**。若不清空，旧行会残留 → 同一 `doc_type` 同时写两个台账、
//     同一阈值键出现两个值（`loadSimpleMap` 由后写者覆盖前者，行为随行序变化）
//     —— 全是「不报错但结果错」的静默缺陷。
//   - 某类在载荷中**为空**（`[]` 或缺失）时**不替换**该类，并计入 `EmptyKinds`
//     由调用方提示，避免「导入半份文件把配置清空」。
func ImportMappings(ctx context.Context, db *store.DB, p *ImportPayload) (ImportResult, error) {
	var res ImportResult
	if err := p.Validate(); err != nil {
		return res, err
	}

	kinds := []struct {
		kind  string
		count int
	}{
		{"approval_code", len(p.ApprovalCode)},
		{"field_id", len(p.FieldID)},
		{"ledger_type", len(p.LedgerType)},
		{"threshold", len(p.Threshold)},
	}
	for _, k := range kinds {
		if k.count == 0 {
			res.EmptyKinds = append(res.EmptyKinds, k.kind)
		}
	}
	if len(p.LedgerField) == 0 {
		res.EmptyKinds = append(res.EmptyKinds, "ledger_field")
	}

	err := db.WithTx(ctx, func(tx *sql.Tx) error {
		// ① 清空「载荷中出现」的映射类 —— 全量替换的第一步。
		//    没有这一步，「改 value」会残留旧行（见函数头注释）。
		for _, k := range kinds {
			if k.count == 0 {
				continue
			}
			if _, err := db.DeleteConfigMappingsKindTx(ctx, tx, k.kind); err != nil {
				return fmt.Errorf("清理 %s 失败: %w", k.kind, err)
			}
			res.ReplacedKinds = append(res.ReplacedKinds, k.kind)
		}
		if len(p.LedgerField) > 0 {
			if _, err := db.DeleteLedgerFieldDefsKindTx(ctx, tx); err != nil {
				return fmt.Errorf("清理 ledger_field 失败: %w", err)
			}
			res.ReplacedKinds = append(res.ReplacedKinds, "ledger_field")
		}

		// ② 按载荷写入
		for _, e := range p.ApprovalCode {
			row := store.ConfigMappingRow{
				MapKind: "approval_code", MapKey: strings.TrimSpace(e.Code),
				MapValue: strings.TrimSpace(e.DocType), DocType: strings.TrimSpace(e.DocType),
				Remark: defaultRemark(e.Remark, "飞书审批模板 approval_code → 单据类型"),
			}
			if err := db.UpsertConfigMappingTx(ctx, tx, row); err != nil {
				return fmt.Errorf("写 approval_code(%s) 失败: %w", e.Code, err)
			}
			res.ApprovalCode++
		}

		for _, e := range p.FieldID {
			biz := strings.TrimSpace(e.BizField)
			row := store.ConfigMappingRow{
				MapKind: "field_id", MapKey: strings.TrimSpace(e.FieldID),
				MapValue: biz, DocType: strings.TrimSpace(e.DocType),
				Remark: defaultRemark(strings.TrimSpace(e.FieldName), "表单控件 field_id → 业务字段名"),
			}
			if err := db.UpsertConfigMappingTx(ctx, tx, row); err != nil {
				return fmt.Errorf("写 field_id(%s) 失败: %w", e.FieldID, err)
			}
			res.FieldID++
		}

		for _, e := range p.LedgerType {
			row := store.ConfigMappingRow{
				MapKind: "ledger_type", MapKey: strings.TrimSpace(e.Key),
				MapValue: strings.TrimSpace(e.Value),
				Remark:   defaultRemark(e.Remark, "doc_type → 台账类型语义键（L01..L12）"),
			}
			if err := db.UpsertConfigMappingTx(ctx, tx, row); err != nil {
				return fmt.Errorf("写 ledger_type(%s) 失败: %w", e.Key, err)
			}
			res.LedgerType++
		}

		for _, e := range p.Threshold {
			row := store.ConfigMappingRow{
				MapKind: "threshold", MapKey: strings.TrimSpace(e.Key),
				MapValue: strings.TrimSpace(e.Value),
				Remark:   defaultRemark(e.Remark, "阈值（单位：元）"),
			}
			if err := db.UpsertConfigMappingTx(ctx, tx, row); err != nil {
				return fmt.Errorf("写 threshold(%s) 失败: %w", e.Key, err)
			}
			res.Threshold++
		}

		// 台账字段定义：不走 t_config_mapping，直接落 t_ledger_field_def
		// （它被写接口用作 fields 键名白名单、被 SensitiveFields 用作列级权限兜底）。
		for _, e := range p.LedgerField {
			def := store.LedgerFieldDef{
				LedgerType:  strings.TrimSpace(e.LedgerType),
				FieldKey:    strings.TrimSpace(e.FieldKey),
				IsSensitive: e.IsSensitive,
			}
			if err := db.UpsertLedgerFieldDefTx(ctx, tx, def); err != nil {
				return fmt.Errorf("写 ledger_field(%s/%s) 失败: %w", def.LedgerType, def.FieldKey, err)
			}
			res.LedgerField++
		}
		return nil
	})
	if err != nil {
		return res, err
	}
	return res, nil
}

func defaultRemark(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
}
