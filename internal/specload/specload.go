// Package specload 加载并校验内嵌的 spec/ 机读契约（WorkBuddy 维护），
// 产出供全系统消费的只读 Bundle。
//
// ★ 校验时机（COLLAB.md N-008 修正①/② 的落地）：
//
//	启动时一次性 Load + S1–S12 全量断言，**失败拒启**；运行期零读盘、无热更新。
//	单测直读同一份 embed 字节（specfs.FS），不复制快照副本 —— spec 一改、测试立刻见差异。
//
// ★ 判据来源：scripts/check_spec.py 文档串 S1–S12（N-011 checks.yaml 未交付前的降级方案，
//
//	判据文字与 Python 侧逐条对应；checks.yaml 交付后切换为清单驱动）。
package specload

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"sort"
	"strings"
)

// ---------------------------------------------------------------------------
// Bundle：加载产物（只读）
// ---------------------------------------------------------------------------

// Bundle 是 spec/ 的解析结果。所有指针字段在 Load 成功后非 nil。
type Bundle struct {
	// SpecVersion 聚合各规格版本（N-008 修正③）：
	// "chain=1.0;enums=1.1;ledger=1.0;forms=BA:1.0,PR:1.0,SA:1.0"
	SpecVersion string

	Chain       ChainDoc
	Forms       map[string]FormDoc // key = doc_type（BA/PR/SA/…）
	Enums       EnumsDoc
	Ledger      LedgerDoc
	Params      *ParamsDoc
	Constants   *ConstantsDoc
	ProblemsRaw map[string][]byte // 全部 spec/**/*.json 原始字节（键 "spec/xxx.json"，测试/诊断用）
}

// EnumsDoc 只带 version（M3 深度解析时扩展）；其余内容经 Raw 保留。
type EnumsDoc struct {
	Version string
	Raw     json.RawMessage
}

// LedgerDoc 只带 version（M8 深度解析时扩展）；其余内容经 Raw 保留。
type LedgerDoc struct {
	Version string
	Raw     json.RawMessage
}

// ---------------------------------------------------------------------------
// chain.json 类型（JSON tag 对齐 spec 原键名；未知键天然忽略，WorkBuddy 加字段不破坏加载）
// ---------------------------------------------------------------------------

type ChainDoc struct {
	Version          string                 `json:"version"`
	Roles            map[string]RoleDoc     `json:"roles"`
	Thresholds       Thresholds             `json:"thresholds"`
	ContractApproval ContractApprovalDoc    `json:"contract_approval"`
	Routes           map[string]RouteDoc    `json:"routes"`
	DocChains        map[string]DocChainDoc `json:"doc_chains"`
	// PaymentRouteRule 付款路径 4 条优先级（T3 / R-26 用户 B2 定案：
	// 判定轴＝是否签合同，**不是金额**；键名 payment_route —— 勿与审批流程线 route 混用）。
	PaymentRouteRule PaymentRouteRule `json:"payment_route_rule"`
	OpenItems        json.RawMessage  `json:"open_items"`
}

// PaymentRouteRule chain.json#payment_route_rule。
type PaymentRouteRule struct {
	Decisions []PaymentDecision `json:"decisions"`
}

// PaymentDecision 单条付款路径决策（按 priority 升序生效）。
type PaymentDecision struct {
	Priority     int    `json:"priority"`
	When         string `json:"when"` // 说明性条件串（实现按 priority 语义编码，见 chain.PaymentRouteOf）
	Label        string `json:"label"`
	PaymentRoute string `json:"payment_route"` // group_public_account / petty_cash / personal_advance_reimburse
	Note         string `json:"note"`
}

type RoleDoc struct {
	Label               string   `json:"label"`
	Desc                string   `json:"desc"`
	ResolveBy           string   `json:"resolve_by"`
	IsAlsoSupervisorFor []string `json:"is_also_supervisor_for"`
	IsSupervisorFor     []string `json:"is_supervisor_for"`
	DesignatedAt        string   `json:"designated_at"`
	// Fallback 主管领导回落规则（N-016 裁定后结构化；原散文判据＝漂移面）。
	Fallback *RoleFallback `json:"fallback"`
	// MultiCandidatePolicy 同角色多候选处置（N-014 裁定：all_sign 全员会签）。
	MultiCandidatePolicy *MultiCandidatePolicy `json:"multi_candidate_policy"`
}

// RoleFallback chain.json#roles.supervisor.fallback（N-016）。
type RoleFallback struct {
	MatchBy    []string `json:"match_by"` // ["department","extra_depts"]
	Source     string   `json:"source"`   // t_user_role
	RoleName   string   `json:"role_name"`
	Unresolved string   `json:"unresolved"` // "block" ⇒ 0 候选阻断（FR-M9-02）
}

// MultiCandidatePolicy 多候选处置（N-014）：warn_threshold 起 preview 必须告警（不阻断）。
type MultiCandidatePolicy struct {
	Rule          string `json:"rule"` // "all_sign（全员会签）"
	WarnThreshold int    `json:"warn_threshold"`
	Note          string `json:"note"`
}

type Thresholds struct {
	Purchase                PurchaseThresholds `json:"purchase"`
	Expense                 json.RawMessage    `json:"expense"`
	ContractApprovalTrigger json.RawMessage    `json:"contract_approval_trigger"`
}

type PurchaseThresholds struct {
	AmountBasis string `json:"amount_basis"`
	Bands       []Band `json:"bands"`
}

// Band 采档区间（闭区间；nil 端点 = ±∞）。
type Band struct {
	ID             string `json:"id"`
	Label          string `json:"label"`
	LowerInclusive *int64 `json:"lower_inclusive"`
	UpperInclusive *int64 `json:"upper_inclusive"`
	RawText        string `json:"raw_text"`
}

type ContractApprovalDoc struct {
	ID           string        `json:"id"`
	Label        string        `json:"label"`
	Rule         string        `json:"rule"`
	Signer       string        `json:"signer"`
	ReviewPoints []string      `json:"review_points"`
	Order        []CAOrderStep `json:"order"`
	Branches     []CABranch    `json:"branches"`
}

type CAOrderStep struct {
	Seq     int    `json:"seq"`
	ID      string `json:"id"`
	Label   string `json:"label"`
	Actor   string `json:"actor"`
	OnlyFor string `json:"only_for"`
	Note    string `json:"note"`
}

type CABranch struct {
	ID     string `json:"id"`
	Label  string `json:"label"`
	When   string `json:"when"`
	Effect string `json:"effect"`
}

type RouteDoc struct {
	Label    string                 `json:"label"`
	When     string                 `json:"when"`
	EnvCount int                    `json:"env_count"`
	Payment  string                 `json:"payment"`
	Nodes    []NodeDoc              `json:"nodes"`
	Branches map[string]RouteBranch `json:"branches"`
	// 其余键（exemptions/required_materials/notes/hard_rules/rules/…）
	// 由消费方按需扩展；M1 不建模。
}

// RouteBranch 流程线级条件分支（如 tier3_plus）。
type RouteBranch struct {
	Label         string    `json:"label"`
	When          string    `json:"when"`
	Effect        string    `json:"effect"`
	InsertBefore  string    `json:"insert_before"`
	InsertedNodes []NodeDoc `json:"inserted_nodes"`
	Resolution    string    `json:"resolution"`
}

type NodeDoc struct {
	Seq            int          `json:"seq"`
	ID             string       `json:"id"`
	Label          string       `json:"label"`
	Actor          string       `json:"actor"`
	Required       bool         `json:"required"`
	Doc            string       `json:"doc"`
	Docs           []string     `json:"docs"`
	Ref            string       `json:"ref"`
	Condition      string       `json:"condition"`
	RequiredFields []string     `json:"required_fields"`
	RecordFields   []string     `json:"record_fields"`
	Ledger         string       `json:"ledger"`
	Note           string       `json:"note"`
	Formula        string       `json:"formula"`
	DeadlineHours  int          `json:"deadline_hours"`
	Branches       []NodeBranch `json:"branches"` // 节点级条件分支（R-03 备付金上抬等）
}

// NodeBranch 节点级条件分支：when 命中时 actor 被替换/行为修正。
type NodeBranch struct {
	When   string `json:"when"`
	Actor  string `json:"actor"`
	Effect string `json:"effect"`
}

type DocChainDoc struct {
	Label            string            `json:"label"`
	Prefix           string            `json:"prefix"`
	Ledger           []string          `json:"ledger"`
	Route            string            `json:"route"`
	RouteByTier      map[string]string `json:"route_by_tier"`
	RouteByCondition string            `json:"route_by_condition"`
	EnvCount         int               `json:"env_count"`
	FinalApprover    string            `json:"final_approver"`
}

// UnmarshalJSON 兼容 doc_chains 中的注释性字符串条目（如 "_note"）——
// 其值是说明文字而非链定义，跳过即可（校验侧对 "_" 前缀键的处理见 check_spec.py S4）。
func (d *DocChainDoc) UnmarshalJSON(b []byte) error {
	if len(b) > 0 && b[0] == '"' {
		return nil
	}
	type alias DocChainDoc
	return json.Unmarshal(b, (*alias)(d))
}

// ---------------------------------------------------------------------------
// forms/*.json 类型
// ---------------------------------------------------------------------------

type FormDoc struct {
	DocType      string          `json:"doc_type"`
	Label        string          `json:"label"`
	Prefix       string          `json:"prefix"`
	NumberFormat string          `json:"number_format"`
	Version      string          `json:"version"`
	Ledger       []string        `json:"ledger"`
	Route        json.RawMessage `json:"route"` // 路线提示（档位/条件），M2 深度消费
	Sections     []SectionDoc    `json:"sections"`
	Checks       []CheckDoc      `json:"checks"`
}

type SectionDoc struct {
	ID       string     `json:"id"`
	Label    string     `json:"label"`
	FilledAt string     `json:"filled_at"`
	Fields   []FieldDoc `json:"fields"`
}

type FieldDoc struct {
	Name                string   `json:"name"`
	Label               string   `json:"label"`
	Type                string   `json:"type"`
	Required            bool     `json:"required"`
	RequiredConditional string   `json:"required_conditional"`
	Source              string   `json:"source"`
	EnumRef             string   `json:"enum_ref"`
	Values              []string `json:"values"`
	IsAmountBasis       bool     `json:"is_amount_basis"`
}

type CheckDoc struct {
	ID        string `json:"id"`
	When      string `json:"when"`
	Assert    string `json:"assert"`
	Else      string `json:"else"`
	Origin    string `json:"origin"`
	OriginRef string `json:"origin_ref"`
	// Severity hard = 提交期机判硬拦截（T4：amount_vs_pr / no_self_purchaser）；缺省 soft。
	Severity string `json:"severity"`
}

// ---------------------------------------------------------------------------
// Load
// ---------------------------------------------------------------------------

// Load 从 fsys（生产环境传 specfs.FS）读取 spec/**/*.json 并做 S1–S12 全量断言。
// 任一判据不过即返回聚合错误 —— 调用方（bootstrap）应拒启。
func Load(fsys fs.FS) (*Bundle, error) {
	files := map[string][]byte{}
	err := fs.WalkDir(fsys, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".json") {
			return nil
		}
		b, rerr := fs.ReadFile(fsys, path)
		if rerr != nil {
			return rerr
		}
		// 键名与 Python 侧一致：相对 embed 根的正斜杠路径（spec/chain.json）
		files[path] = b
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("specload: 遍历 spec 失败: %w", err)
	}
	return loadFiles(files)
}

// loadFiles 以「路径 → 原始字节」为输入完成解析与校验。
// 单测的负向探针通过传入内存变异字节复用本函数（不落盘第二份 spec）。
func loadFiles(files map[string][]byte) (*Bundle, error) {
	problems := validate(files)
	if len(problems) > 0 {
		return nil, fmt.Errorf("specload: spec/ 校验失败 %d 处:\n  %s",
			len(problems), strings.Join(problems, "\n  "))
	}

	// ---- S1 已过：全部 JSON 可解析 ----
	decode := func(key string, v any) error {
		b, ok := files[key]
		if !ok {
			return fmt.Errorf("specload: 缺少 %s", key)
		}
		if err := json.Unmarshal(b, v); err != nil {
			return fmt.Errorf("specload: %s 解析失败: %w", key, err)
		}
		return nil
	}

	b := &Bundle{ProblemsRaw: files}

	if err := decode("spec/chain.json", &b.Chain); err != nil {
		return nil, err
	}
	if err := decode("spec/enums.json", &b.Enums); err != nil {
		return nil, err
	}
	if err := decode("spec/ledger-mapping.json", &b.Ledger); err != nil {
		return nil, err
	}
	// params.json：类型化解析 + [P1]-[P3] 自检（README #24：每个参数必须声明 consumer）
	params, err := decodeParams(files)
	if err != nil {
		return nil, err
	}
	b.Params = params
	if p := validateParams(params); len(p) > 0 {
		return nil, fmt.Errorf("specload: spec/params.json 校验失败 %d 处:\n  %s",
			len(p), strings.Join(p, "\n  "))
	}

	// constants.json：类型化解析 + [C1]/[C2] 自检（R-24：只停用不删等纪律在声明侧先拦）
	constants, err := decodeConstants(files)
	if err != nil {
		return nil, err
	}
	b.Constants = constants
	if p := validateConstants(constants); len(p) > 0 {
		return nil, fmt.Errorf("specload: spec/constants.json 校验失败 %d 处:\n  %s",
			len(p), strings.Join(p, "\n  "))
	}

	// forms：任何 spec/forms/*.json（M1 期为 BA/PR/SA，随 WorkBuddy 产出增多自动纳入）
	b.Forms = map[string]FormDoc{}
	for key, raw := range files {
		if !strings.HasPrefix(key, "spec/forms/") || !strings.HasSuffix(key, ".json") {
			continue
		}
		var f FormDoc
		if err := json.Unmarshal(raw, &f); err != nil {
			return nil, fmt.Errorf("specload: %s 解析失败: %w", key, err)
		}
		if f.DocType == "" {
			return nil, fmt.Errorf("specload: %s 缺 doc_type", key)
		}
		if _, dup := b.Forms[f.DocType]; dup {
			return nil, fmt.Errorf("specload: %s doc_type 重复: %s", key, f.DocType)
		}
		b.Forms[f.DocType] = f
	}

	b.SpecVersion = buildSpecVersion(b)
	return b, nil
}

// buildSpecVersion 聚合各规格版本号（N-008 修正③：meta 响应须带 spec_version）。
func buildSpecVersion(b *Bundle) string {
	docTypes := make([]string, 0, len(b.Forms))
	for dt := range b.Forms {
		docTypes = append(docTypes, dt)
	}
	sort.Strings(docTypes)
	formParts := make([]string, 0, len(docTypes))
	for _, dt := range docTypes {
		formParts = append(formParts, fmt.Sprintf("%s:%s", dt, b.Forms[dt].Version))
	}
	return fmt.Sprintf("chain=%s;enums=%s;ledger=%s;forms=%s",
		b.Chain.Version, b.Enums.Version, b.Ledger.Version, strings.Join(formParts, ","))
}
