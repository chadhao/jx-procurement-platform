// Package dashboard 实现 M5 的 4 张看板聚合逻辑（FR-M5-01/05/06/07）。
//
// 设计纪律（docs/01-PRD.md §6.3、docs/04-Architecture.md §1.2 / §5）：
//   - 数据源指向「运营表」：t_ledger_archive（只读同步存档）LEFT JOIN t_ledger_ops（可写运营表）
//     的合并视图。只看只读存档表会使「经办人指定集中度」「平均采购周期」等指标恒空（FR-M5-05 / TC-24）。
//   - 行级过滤：本包不改写任何权限口径，行过滤 SQL 片段（含 ? 占位）由调用方（接入层）
//     通过 permission.RowFilter 生成后经 Query 注入，在 SQL 层生效（架构 §5.2 要点 1）。
//   - 列级投影：本包只产出「指标行」；金额类指标以 amount_cents / amount_display 命名，
//     由接入层复用 permission.Project 在序列化阶段裁剪（架构 §5.3，TC-07）。
//   - 看板只读，本包不提供任何写入口（FR-M5-06）。
package dashboard

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"github.com/chadhao/jx-procurement-platform/internal/submission"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/chadhao/jx-procurement-platform/internal/jsonutil"
	"github.com/chadhao/jx-procurement-platform/internal/specload"
	"github.com/chadhao/jx-procurement-platform/internal/store"
)

// 看板 id → 名称（docs/01-PRD.md §6.3 第 13~16 行）。
const (
	DashboardBudget    = 13            // 预算执行看板（本期不启用，空态）
	DashboardPurchase  = 14            // 采购执行看板
	DashboardExpense   = 15            // 费用结构看板
	DashboardAnomaly   = 16            // 异常预警面板
	defaultSplitCents  = int64(100000) // 同供应商 + 同品类月累计阈值：1,000 元 = 100,000 分
	defaultCycleMonths = 6             // 趋势默认回看月数
	// N-060 H2：submit_overdue 的 key/label 单源（undefined_criteria 与 guardedAlert
	// 两分支共用 —— 改名一处、全路径生效；key 集合断言（key_align）即天然钉住两分支）。
	submitOverdueKey    = "submit_overdue"
	submitOverdueLabel  = "提交超期（湖南侧完成 3 个工作日未提交集团）"
	defaultEmergencyHrs = 24 // 紧急采购补录/闭合时限（小时）
	defaultReviewHours  = 72 // 超时未审口径（3 个工作日近似）
)

// 运营表（ops_json）与存档表（ext_json）中使用的业务键。
// ★ Q14 已定案（2026-09-26）：L11 订单执行台账为**派生视图**，其键名见 order_execution.go；
// 本块只保留**运营表确实可写**的中文键（L01/L03/L08/L09/L12 等）。键名以数据为准，不硬编码控件 id。
const (
	keyAssigned   = "assigned_open_id" // 被指定经办人（优先 ops，回退 archive ext）
	keyAssignedCN = "指定经办人"            // 中文等价键（兼容人工登记口径）
	keyStatus     = "经办状态"
	keyDoneDate   = "完成日期"
	keyClosed     = "是否已核销闭合"
	keyMethod     = "采购方式"
	keyAcctChange = "账户变更"
	keyOverBudget = "预警状态"
)

// Result 看板响应（docs/05-API.md §4.4：指标卡 + 图表序列 + 监督指标 + 异常预警）。
type Result struct {
	ID          int              `json:"id"`
	Name        string           `json:"name"`
	Period      string           `json:"period"`
	Cards       []map[string]any `json:"cards"`
	Charts      []Chart          `json:"charts"`
	Supervision map[string]any   `json:"supervision"`
	Alerts      []map[string]any `json:"alerts"`
	// Warnings 本次构建中发现的**数据质量问题**（如 ext_json/ops_json 解析失败的行数）。
	//
	// ★ 为什么要有：坏行原先只是"少几个字段"，指标**偏低却不报错**。
	//   有了它，"数字不对"至少有一个可追的线索（静默审计 C6）。
	Warnings []string `json:"warnings,omitempty"`
	// SourceStatus/SourceNote/SpecVersion 来自 spec/dashboard.json（global_rules.r1：
	// 非 connected 的看板全部指标显示「数据未接入」而非 0 —— 响应须如实带上状态与理由）。
	SourceStatus string `json:"source_status,omitempty"`
	SourceNote   string `json:"source_note,omitempty"`
	SpecVersion  string `json:"spec_version,omitempty"`
}

// Chart 图表序列（key/type/series）。
// Status/Message 为指标级可用性守卫（global_rules.r6）：源空且须守卫时
// 输出 status=not_connected 而非空序列 —— 空图与「没接上」不得同形。
type Chart struct {
	Key     string           `json:"key"`
	Type    string           `json:"type"`
	Series  []map[string]any `json:"series"`
	Status  string           `json:"status,omitempty"`
	Message string           `json:"message,omitempty"`
}

// Query 行级过滤条件（SQL 片段 + 参数），由接入层用 permission.RowFilter 生成后注入。
// 台账表别名固定为 a；实例表（t_instance）查询亦以别名 a 承载。
type Query struct {
	LedgerRowSQL    string
	LedgerRowArgs   []any
	InstanceRowSQL  string
	InstanceRowArgs []any
	// SubmissionRowSQL/Args 针对 t_submission（报送登记）的行过滤。
	// ★ 报送表既不在台账族内、也不在实例表内，必须单列一路：否则以报送为数据源的聚合指标
	// （如「集团驳回后未处置」）只能全表 COUNT → 向受限角色暴露全局值。
	SubmissionRowSQL  string
	SubmissionRowArgs []any
}

// Row 合并视图行（存档 + 运营）。金额以分为单位，HasAmount 标记存档是否带金额。
type Row struct {
	LedgerType      string
	BizNo           string
	InstanceCode    string
	Department      string
	ApplicantOpenID string
	AmountCents     int64
	HasAmount       bool
	Supplier        string
	// SupplierNorm 归一分组键（PRD Q20）：防拆分「同供应商当月累计」按它分组，
	// 否则同一家换个写法（空格 / 全角 / 大小写）即绕过阈值。展示仍用 Supplier 原名。
	SupplierNorm string
	PurposeL1    string
	PurposeL2    string
	BizDate      string
	ArchiveExt   map[string]any
	Ops          map[string]any
}

// Builder 看板聚合器。
type Builder struct {
	db         *store.DB
	now        func() time.Time
	splitCents int64
	// corruptRows 本次构建中 ext_json / ops_json 解析失败的行数（见 Result.Warnings）。
	corruptRows int
	// dash 看板规格（r1 灰态由其 source_status 驱动；nil = 不启用灰态，仅测试直造时出现）。
	dash *specload.DashboardDoc
	// deadlineWorkdays N-060 G2（R-33）：SUB 提交集团时限（工作日）——
	// 唯一来源＝spec doc_chains.SUB.deadline_workdays（WithDeadlineWorkdays 装配注入，
	// **不写死 3**）；0 ＝ 未装配 ⇒ submit_overdue 输出 undefined_criteria（不猜口径）。
	deadlineWorkdays int
}

// New 构造聚合器（默认时钟 time.Now，默认拆分阈值 1,000 元）。
func New(db *store.DB) *Builder {
	return &Builder{db: db, now: func() time.Time { return time.Now().UTC() }, splitCents: defaultSplitCents}
}

// WithDeadlineWorkdays 注入 SUB 提交集团时限（N-060 G2；值来自 spec 装载，非字面量）。
func (b *Builder) WithDeadlineWorkdays(n int) *Builder {
	b.deadlineWorkdays = n
	return b
}

// WithNow 注入时钟（供测试与可控时间窗）。
func (b *Builder) WithNow(fn func() time.Time) *Builder {
	if fn != nil {
		b.now = fn
	}
	return b
}

// WithDashboard 注入看板规格（spec/dashboard.json）—— global_rules.r1 的灰态判据。
func (b *Builder) WithDashboard(dash *specload.DashboardDoc) *Builder {
	b.dash = dash
	return b
}

// WithSplitThresholdCents 覆盖拆分阈值（来自 t_config_mapping: threshold.split_supplier_month）。
func (b *Builder) WithSplitThresholdCents(cents int64) *Builder {
	if cents > 0 {
		b.splitCents = cents
	}
	return b
}

// DashboardName 返回看板名称。
func DashboardName(id int) string {
	switch id {
	case DashboardBudget:
		return "预算执行看板"
	case DashboardPurchase:
		return "采购执行看板"
	case DashboardExpense:
		return "费用结构看板"
	case DashboardAnomaly:
		return "异常预警面板"
	default:
		return ""
	}
}

// Build 计算某看板数据。id=13（预算执行）本期不启用 → 返回空序列且不报错（HTTP 200）。
func (b *Builder) Build(ctx context.Context, id int, period string, q Query) (res Result, err error) {
	b.corruptRows = 0
	// ★ 静默审计 C6：`ext_json` / `ops_json` 解析失败原先只是"这一行少了些字段"，
	//   指标会**偏低却不报错**。这里把坏行数带上响应（`warnings`），
	//   让"数字不对"至少有一个可追的线索，而不是只看到一个小了的数字。
	defer func() {
		if err == nil && b.corruptRows > 0 {
			res.Warnings = append(res.Warnings, fmt.Sprintf(
				"有 %d 行的扩展/运营字段无法解析（数据损坏），相关指标可能偏低 —— 请检查写入方",
				b.corruptRows))
		}
	}()

	if strings.TrimSpace(period) == "" {
		period = b.now().Format("2006-01")
	}
	res = Result{
		ID: id, Name: DashboardName(id), Period: period,
		Cards: []map[string]any{}, Charts: []Chart{}, Alerts: []map[string]any{},
		Supervision: emptySupervision(),
	}
	// ★ global_rules.r1（灰态短路）：source_status 非 connected ⇒ 本看板**全部指标**
	//   显示「数据未接入」而非 0 —— "一旦显示 0，半年后没人说得清那是真 0 还是没接上"。
	//   指标清单（key/label）由 spec/dashboard.json 驱动，不走下面的聚合计算。
	if board := b.dash.Board(id); board != nil && board.SourceStatus != specload.DashboardStatusConnected {
		res.SourceStatus = board.SourceStatus
		res.SourceNote = board.SourceNote
		res.SpecVersion = b.dash.Version
		for _, ind := range board.Indicators {
			// ★ global_rules.r7 第三态：口径未定 ≠ 数据未接入（"标准没给"与"没接上"
			//   成因不同、渲染必须互不相同 —— 否则读成「没有异常科目」）。
			if formulaUndefined(ind.Formula) {
				greyOut(&res, ind, "undefined_criteria", "口径未定 —— 待财务/集团给判定标准")
				continue
			}
			greyOut(&res, ind, "not_connected", "数据未接入")
		}
		// 板内无 render=supervision 的指标时，历史 supervision 块整体置灰 ——
		// 否则 emptySupervision() 的 requester_as_handler_count=0 会在灰态冒出「真 0」。
		if _, isMap := res.Supervision["status"].(string); !isMap || res.Supervision["status"] == "" {
			res.Supervision = map[string]any{"status": "not_connected", "message": "数据未接入"}
		}
		return res, nil
	}
	var berr error
	switch id {
	case DashboardBudget:
		// 本期预算不启用：空态，不报错（docs/05-API.md §3.3）。
	case DashboardPurchase:
		res, berr = b.buildPurchase(ctx, res, period, q)
	case DashboardExpense:
		res, berr = b.buildExpense(ctx, res, period, q)
	case DashboardAnomaly:
		res, berr = b.buildAnomaly(ctx, res, period, q)
	default:
		return res, fmt.Errorf("dashboard: 未知看板 id %d", id)
	}
	if berr != nil {
		return res, berr
	}
	// ★ 指标 key/label 的**唯一真相是 spec**（known_gaps 第 7 条：两份真相须收敛）——
	//   key 已在实现侧对齐；label 按 spec 回填（spec 注入时以 spec 为准，
	//   实现侧 desc 只作未注入时的兜底，避免「label 又成第二份真相」）。
	b.applySpecIndicatorMeta(&res, id)
	return res, nil
}

// greyOut 把一个指标按 spec 的 render 归位到灰态容器（r8：形态由规格定 ——
// 灰态也不例外：card→Cards / chart→Charts / alert→Alerts / supervision→Supervision）。
func greyOut(res *Result, ind specload.IndicatorDoc, status, message string) {
	item := map[string]any{
		"key": ind.Key, "label": ind.Label,
		"status": status, "message": message,
	}
	switch ind.Render {
	case "card":
		res.Cards = append(res.Cards, item)
	case "chart":
		res.Charts = append(res.Charts, Chart{
			Key: ind.Key, Status: status, Message: message,
		})
	case "supervision":
		res.Supervision = item
	default: // alert 及未知（[D4] 已拦未知值；此处兜底走 Alerts 不丢指标）
		res.Alerts = append(res.Alerts, item)
	}
}

// formulaUndefined 指标 formula 声明「未定义」（global_rules.r7 的判据形态：
// formula 为「未定义」⇒ availability_guard 须声明 undefined_criteria）。
func formulaUndefined(formula string) bool {
	return strings.Contains(formula, "未定义")
}

// applySpecIndicatorMeta 把 spec 看板指标的 label 回填到聚合产出的 alerts
// （key 已对齐 spec；未注入 spec 或 key 不在 spec 中则保留原样 —— 便于发现错位）。
func (b *Builder) applySpecIndicatorMeta(res *Result, id int) {
	board := b.dash.Board(id)
	if board == nil {
		return
	}
	byKey := make(map[string]specload.IndicatorDoc, len(board.Indicators))
	for _, ind := range board.Indicators {
		byKey[ind.Key] = ind
	}
	for i := range res.Alerts {
		k, _ := res.Alerts[i]["key"].(string)
		if ind, ok := byKey[k]; ok {
			res.Alerts[i]["label"] = ind.Label
		}
	}
}

// ---------- 14 采购执行看板 ----------

func (b *Builder) buildPurchase(ctx context.Context, res Result, period string, q Query) (Result, error) {
	win := windowStart(period, defaultCycleMonths-1)
	// ★ L11 是**派生视图**（Q14 定案），不落 `t_ledger_archive` —— 此处改走与台账派生视图
	//   同一口径（L04 骨架 + 关联 L07）。原实现直接读 ledger_type='L11'，而该值永不落行
	//   → 本看板五项指标恒空/恒 0 且不报错（架构审查 A-1）。
	r11, err := b.orderExecutionRows(ctx, win, q)
	if err != nil {
		return res, err
	}
	// 经办登记台账（L03）仍读存档行（它是实例级台账）。
	r03, err := b.fetchLedgerRows(ctx, []string{"L03"}, win, q)
	if err != nil {
		return res, err
	}

	// 在途订单数：订单执行台账中尚无「实际到货」的订单。
	inFlight := 0
	for _, r := range r11 {
		if strings.TrimSpace(r.OpsStr(keyActualArrival)) == "" {
			inFlight++
		}
	}
	// in_flight_orders **可不守卫**（connected_requires 14：无在途订单 ＝ 真的没有）。
	res.Cards = append(res.Cards, card("in_flight_orders", "在途订单数", inFlight))

	// 平均采购周期：订单日期 → 运营表「完成日期（回退实际到货）」，单位天。
	var sumDays float64
	var n int
	for _, r := range r11 {
		if d, ok := cycleDays(r); ok {
			sumDays += float64(d)
			n++
		}
	}
	// avg_cycle_days **须守卫**（connected_requires 14：无完成日期时恒空/0 会被误读为「周期为 0」）。
	res.Cards = append(res.Cards, guardedValue("avg_cycle_days", "平均采购周期", round1(sumDays/float64(n)), n, true))

	// 延期订单 TOP5（运营表「延期天数」> 0）。
	type delay struct {
		biz  string
		days int64
	}
	var delays []delay
	for _, r := range r11 {
		if d, ok := r.OpsInt(keyDelayDays); ok && d > 0 {
			delays = append(delays, delay{r.BizNo, d})
		}
	}
	sort.Slice(delays, func(i, j int) bool {
		if delays[i].days != delays[j].days {
			return delays[i].days > delays[j].days
		}
		return delays[i].biz < delays[j].biz
	})
	if len(delays) > 5 {
		delays = delays[:5]
	}
	delaySeries := make([]map[string]any, 0, len(delays))
	for _, d := range delays {
		delaySeries = append(delaySeries, countPoint(d.biz, d.days))
	}
	// delay_top5 **须守卫**：0 与「延期天数列没人填」同形 ⇒ 源＝有延期天数值的行数。
	delaySrc := 0
	for _, r := range r11 {
		if _, ok := r.OpsInt(keyDelayDays); ok {
			delaySrc++
		}
	}
	res.Charts = append(res.Charts, guardedChart("delay_top5", "bar", delaySeries, delaySrc, true))

	// 月度采购金额趋势（近 N 月）。
	sumByMonth := map[string]int64{}
	for _, r := range r11 {
		if m := monthOf(r.BizDate); m != "" {
			sumByMonth[m] += r.AmountCents
		}
	}
	months := lastMonths(period, defaultCycleMonths)
	trend := make([]map[string]any, 0, len(months))
	for _, m := range months {
		trend = append(trend, moneyPoint(m, sumByMonth[m]))
	}
	// monthly_amount_trend **可不守卫**（connected_requires 14 明示：某月无采购 ＝ 真的没有，
	// 0 是真实语义、无空源歧义 —— 「不守卫」是登记在 spec 的决定，不是遗漏）。
	res.Charts = append(res.Charts, Chart{Key: "monthly_amount_trend", Type: "line", Series: trend})

	// 同供应商当月累计 TOP（本期，**按归一分组键**聚合；Q20）。
	// ★ 按原名聚合时，同一家换个写法即拆成两行、各自都达不到阈值 —— 防拆分形同虚设。
	supSum := map[string]int64{}
	supName := map[string]string{}
	for _, r := range r11 {
		if monthOf(r.BizDate) != period || strings.TrimSpace(r.Supplier) == "" {
			continue
		}
		k := r.SupplierNorm
		if k == "" { // 极端兜底：历史行未回填归一值时退回原名，绝不因缺归一值而漏计
			k = r.Supplier
		}
		supSum[k] += r.AmountCents
		if _, ok := supName[k]; !ok {
			supName[k] = r.Supplier // 展示用：取该组**首个出现的原名**
		}
	}
	// supplier_monthly_accum_top **须守卫**（spec key 对齐 known_gaps 第 7 条的同族收口：
	// 无累计时 0 与「没有拆分」同形）。
	res.Charts = append(res.Charts, guardedChart("supplier_monthly_accum_top", "bar",
		topMoneyBarsNamed(supSum, supName, 5), len(r11), true))

	// ★ 监督指标（FR-M5-07）：需求提出人任经办人的笔数（应恒为 0） + 经办人指定集中度。
	res.Supervision = buildSupervision(r03, period, q, b)
	return res, nil
}

// ---------- 15 费用结构看板 ----------

func (b *Builder) buildExpense(ctx context.Context, res Result, period string, q Query) (Result, error) {
	rows, err := b.fetchLedgerRows(ctx, []string{"L05"}, windowStart(period, defaultCycleMonths-1), q)
	if err != nil {
		return res, err
	}
	r05 := splitByType(rows, "L05")["L05"]

	periodRows := filterByMonth(r05, period)

	// ★ N-033② 裁定（dashboard.json V1.2 · known_gaps 第 8 条）：spec 只列 4 项 ——
	//   原 expense_total / expense_count / expense_monthly_trend **已删除**（两条权威源
	//   工具表 R18 + PRD §6.3 都没有它们；monthly_trend 与 by_department 是同一件事的
	//   两种算法 ⇒ 保留即第二份真相）。业务确需须由 WorkBuddy 补进 spec 再实现。

	// 按部门分布（spec key：by_department —— 不再私设 expense_ 前缀，r8）。
	res.Charts = append(res.Charts, Chart{Key: "by_department", Type: "bar", Series: groupMoney(periodRows, func(r Row) string { return r.Department }, 0)})
	// 按类别分布（二级明细，回退一级）。
	cat := func(r Row) string {
		if strings.TrimSpace(r.PurposeL2) != "" {
			return r.PurposeL2
		}
		return r.PurposeL1
	}
	res.Charts = append(res.Charts, Chart{Key: "by_category", Type: "pie", Series: groupMoney(periodRows, cat, 0)})
	// 按供应商分布（TOP5）。
	res.Charts = append(res.Charts, Chart{Key: "by_supplier", Type: "bar", Series: groupMoney(periodRows, func(r Row) string { return r.Supplier }, 5)})

	// 异常科目提示：★ spec 明令「口径未定 —— 我方不编」（known_gaps 第 3 条：
	// 工具表只给五个字、无判定标准）⇒ 输出**第三态 undefined_criteria**，
	// 不得显示任何数字（自编的离群公式已删除 —— 编了就是无依据的"事实标准"）。
	res.Alerts = append(res.Alerts, map[string]any{
		"key": "abnormal_subject_hint", "label": "异常科目提示",
		"status": "undefined_criteria", "message": "口径未定 —— 待财务/集团给判定标准",
	})

	return res, nil
}

// ---------- 16 异常预警面板 ----------

func (b *Builder) buildAnomaly(ctx context.Context, res Result, period string, q Query) (Result, error) {
	// L11 已为派生视图、不落行，不再列入（原列入但从未消费 byType["L11"]）。
	// ★ N-034：L08 已从看板数据源移除（手工主数据 producer=[]、零写入通路 ——
	//   指向它的指标生产上永不亮）；account_changed 改读 L06。
	types := []string{"L01", "L03", "L06", "L09", "L12"}
	rows, err := b.fetchLedgerRows(ctx, types, windowStart(period, defaultCycleMonths-1), q)
	if err != nil {
		return res, err
	}
	byType := splitByType(rows, types...)
	r01, r03, r06, r09, r12 := byType["L01"], byType["L03"], byType["L06"], byType["L09"], byType["L12"]

	// ★ 12 个指标**每个各自**带可用性守卫（connected_requires 16① / global_rules.r6）——
	//   看板级只有一个 source_status，而 12 个指标分布在 6 个台账上
	//   （source_ledgers＝L01,L02,L03,L06,L09,L12 —— N-046 遗留⑦，实测订正）。
	srcInst, err := b.countInstanceRows(ctx, q)
	if err != nil {
		return res, err
	}
	srcSubm, err := b.countSubmissionRows(ctx, q)
	if err != nil {
		return res, err
	}

	// ① 超时未审：t_instance 处于 PENDING 且创建时间早于 3 个工作日（近似 72 小时）。
	overdueReview, err := b.countOverduePending(ctx, q)
	if err != nil {
		return res, err
	}
	res.Alerts = append(res.Alerts,
		guardedAlert("overdue_unapproved", "超时未审", overdueReview, srcInst, true),

		// ② 超预算：L12「预警状态」含「超支」；L12 未启用 ⇒ 源恒 0 ⇒ 恒显「数据未接入」而非 0
		//   （正是 r1 批评的「显示 0 让人以为预算执行率为 0%」的形态）。
		guardedAlert("over_budget", "超预算", countOpsContains(r12, keyOverBudget, "超支"), len(r12), true),

		// ③ 紧急采购：例外事项台账（L09）采购方式含「紧急」。
		guardedAlert("emergency_purchase", "紧急采购", countOpsContains(r09, keyMethod, "紧急"), len(r09), true),

		// ④ 单一来源：例外事项台账（L09）采购方式含「单一来源」或「独家」。
		guardedAlert("sole_source", "单一来源", countOpsContainsAny(r09, keyMethod, "单一来源", "独家"), len(r09), true),

		// ④′ 采购变更异常（N-046）：L09.exception_type = 采购变更 ∧ is_anomaly_listed = true。
		//   ★ 两列的唯一生产者＝httpapi#injectPCSSSystemFields（PC 提交期注入），
		//   落点＝t_ledger_archive.ext_json（finalize 的 L09 六列自检同源）⇒ 读 ArchiveExt
		//   （与 account_changed 的 countL06Unverified 同范式；★ 不读 ops——
		//   is_anomaly_listed 在 ops 无生产者）。列级守卫＝countExtRegistered（该键
		//   无人登记 ⇒ not_connected 不报 0，r3：0 会被读成「没有变更异常」）。
		guardedAlert("change_anomaly_listed", "采购变更异常（90 天内 ≥2 次）", countChangeAnomalyListed(r09),
			countExtRegistered(r09, "is_anomaly_listed"), true),

		// ⑤ 账户变更：★ N-034 改判 —— `L06.收款账户已核验` 为「否/false」（＝变更过）的笔数；
		//   该列由 SUB 落账带入（writable:false ⇒ 存 archive ext）⇒ 列级守卫＝ext 中
		//   **有登记值**的行数（一行都没登记 ⇒ not_connected 而非 0，r3 列级照旧）。
		guardedAlert("account_changed", "账户变更", countL06Unverified(r06),
			countExtRegistered(r06, "payee_account_verified"), true),

		// ⑥ 拆分嫌疑：同供应商 + 同品类月累计 ≥ 1,000 元（且 ≥2 笔）。
		func() map[string]any {
			// ★ N-060 F9（FR-M4-06）：计数之外挂**组明细 detail**（按月清单可导出）；
			//   仅 count>0 时附带（not_connected/0 不挂 —— 与「不显示假 0」同精神）。
			a := guardedAlert("split_suspicion", "拆分嫌疑", b.countSplitSuspect(r01), len(r01), true)
			if groups := b.listSplitSuspect(r01); len(groups) > 0 {
				a["detail"] = groups
			}
			return a
		}(),
	)

	// ⑦ 经办超期未完成：采购经办登记台账（L03）未完成且无完成日期。
	res.Alerts = append(res.Alerts,
		guardedAlert("purchaser_overdue", "经办超期未完成", countHandlerIncomplete(r03), len(r03), true))

	// ⑧ 紧急采购超 24 小时未补录或未核销闭合。
	res.Alerts = append(res.Alerts,
		guardedAlert("emergency_not_closed_24h", "紧急采购超 24 小时未闭合", b.countEmergencyUnclosed(r09), len(r09), true))

	// ⑨ 集团驳回后未处置：报送登记 grp_state 含「驳回」且无驳回原因/处置。
	rej, err := b.countGroupRejectedUndisposed(ctx, q)
	if err != nil {
		return res, err
	}
	res.Alerts = append(res.Alerts,
		guardedAlert("group_rejected_unhandled", "集团驳回后未处置", rej, srcSubm, true))

	// ⑬ 提交超期（N-060 G2 · FR-M6-03 · R-33 · spec 第 13 指标 submit_overdue）：
	//   L06.submit_group_at 空 ∧ 距 L06.hunan_completed_at 超 deadlineWorkdays 个工作日。
	//   r3 守卫：L06 无行 ⇒ not_connected（guardedAlert needGuard）；
	//   阈值未装配 ⇒ undefined_criteria（不把「口径未配」显示成 0）。
	//   ★ N-060 H2：key/label 收成常量（两分支单源 —— 改名必同步，key 集合断言两侧路径都钉住）。
	{
		submitCnt := countSubmitOverdue(r06, b.deadlineWorkdays, b.now())
		var submitAlert map[string]any
		if b.deadlineWorkdays <= 0 {
			submitAlert = map[string]any{
				"key": submitOverdueKey, "label": submitOverdueLabel,
				"status": "undefined_criteria", "message": "阈值未装配（doc_chains.SUB.deadline_workdays）—— 口径未定不显示 0",
			}
		} else {
			submitAlert = guardedAlert(submitOverdueKey, submitOverdueLabel,
				submitCnt, len(r06), true)
		}
		res.Alerts = append(res.Alerts, submitAlert)
	}

	// ★ 监督指标（FR-M5-07）：单独成项。
	sup := buildSupervision(r03, period, q, b)
	requesterCount := intFromAny(sup["requester_as_handler_count"])

	// ⑩ 需求提出人任经办人的笔数（应恒为 0，异常信号 → 红标 high）。
	// ★ r3：L03 过滤后 0 行 ⇒ 不得报 0（0 与「没接上」同形）—— 显示「数据未接入」。
	if sup["source_status"] == "not_connected" {
		res.Alerts = append(res.Alerts, map[string]any{
			"key": "self_purchaser_count", "status": "not_connected", "message": "数据未接入",
		})
		res.Alerts = append(res.Alerts, map[string]any{
			"key": "purchaser_concentration", "status": "not_connected", "message": "数据未接入",
		})
	} else {
		level := "warn"
		if requesterCount > 0 {
			level = "high"
		}
		res.Alerts = append(res.Alerts, map[string]any{
			"key": "self_purchaser_count", "level": level, "count": requesterCount,
		})
		// ⑪ 经办人指定集中度（异常信号判读：零违规 + 长期固定指定同一人）。
		res.Alerts = append(res.Alerts, map[string]any{
			"key": "purchaser_concentration", "level": "warn",
			"count": intFromAny(sup["concentration_max_count"]),
		})
	}

	res.Supervision = sup
	return res, nil
}

// ---------- 监督指标（FR-M5-07） ----------

// buildSupervision 产出监督指标块：
//   - requester_as_handler_count：需求提出人任经办人的笔数（应恒为 0）；
//   - handler_concentration：经办人指定集中度（某人被指定笔数 ÷ 总笔数）。
//
// 数据源：采购经办登记台账（L03）的运营表口径（被指定经办人优先取 ops_json）。
func buildSupervision(r03 []Row, period string, _ Query, b *Builder) map[string]any {
	out := emptySupervision()
	rows := r03
	if strings.TrimSpace(period) != "" {
		rows = filterByMonth(r03, period)
	}
	total := len(rows)
	counts := map[string]int{}
	requester := 0
	maxCount := 0
	for _, r := range rows {
		assigned := r.Assigned()
		if assigned == "" {
			continue
		}
		counts[assigned]++
		if strings.TrimSpace(r.ApplicantOpenID) != "" && assigned == r.ApplicantOpenID {
			requester++
		}
	}
	conc := make([]map[string]any, 0, len(counts))
	for h, c := range counts {
		ratio := 0.0
		if total > 0 {
			ratio = float64(c) / float64(total)
		}
		if c > maxCount {
			maxCount = c
		}
		conc = append(conc, map[string]any{"handler": h, "ratio": round4(ratio), "count": c})
	}
	sort.Slice(conc, func(i, j int) bool {
		ci, _ := conc[i]["count"].(int)
		cj, _ := conc[j]["count"].(int)
		if ci != cj {
			return ci > cj
		}
		return conc[i]["handler"].(string) < conc[j]["handler"].(string)
	})
	out["requester_as_handler_count"] = requester
	out["handler_concentration"] = conc
	out["concentration_max_count"] = maxCount
	out["handler_total"] = total
	// ★ global_rules.r3：「应恒为 0」类指标必须带数据源非空前置断言 ——
	//   过滤后 0 行时「真的 0」与「数据没接上」完全同形（R-02 实证：L03 恒空时
	//   requester_as_handler 恒 0 而无人发现）。渲染层据此显示「数据未接入」。
	if total == 0 {
		out["source_status"] = "not_connected"
	} else {
		out["source_status"] = "connected"
	}
	if b != nil {
		out["split_threshold_cents"] = b.splitCents
	}
	return out
}

func emptySupervision() map[string]any {
	return map[string]any{
		"requester_as_handler_count": 0,
		"handler_concentration":      []map[string]any{},
	}
}

// ---------- 数据读取（运营表合并视图） ----------

// fetchLedgerRows 读取指定台账类型在时间窗内的合并视图行（存档 LEFT JOIN 运营）。
// 行过滤在 SQL 层生效：where 内联 q.LedgerRowSQL（含 ? 占位）及其参数。
func (b *Builder) fetchLedgerRows(ctx context.Context, types []string, windowStart string, q Query) ([]Row, error) {
	if len(types) == 0 {
		return nil, nil
	}
	args := make([]any, 0, len(types)+2)
	holders := make([]string, len(types))
	for i, t := range types {
		holders[i] = "?"
		args = append(args, t)
	}
	where := "a.ledger_type IN (" + strings.Join(holders, ",") + ")"
	if strings.TrimSpace(windowStart) != "" {
		// 含无业务日期的行（其指标不依赖账期，如「在途订单数」）。
		where += " AND (COALESCE(a.biz_date,'') = '' OR substr(a.biz_date,1,7) >= ?)"
		args = append(args, windowStart)
	}
	if strings.TrimSpace(q.LedgerRowSQL) != "" {
		where += " AND (" + q.LedgerRowSQL + ")"
		args = append(args, q.LedgerRowArgs...)
	}

	query := `
SELECT a.ledger_type, COALESCE(a.biz_no,''), COALESCE(a.instance_code,''), COALESCE(a.department,''),
       COALESCE(a.applicant_open_id,''), a.amount_cents, COALESCE(a.supplier,''),
       COALESCE(a.supplier_norm,''),
       COALESCE(a.purpose_class_l1,''), COALESCE(a.purpose_class_l2,''), COALESCE(a.biz_date,''),
       COALESCE(a.ext_json,'{}'), COALESCE(o.ops_json,'{}')
FROM t_ledger_archive a
LEFT JOIN t_ledger_ops o ON o.ledger_type = a.ledger_type AND o.biz_no = a.biz_no
WHERE ` + where

	sqlRows, err := b.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("dashboard: 读取运营表合并视图失败: %w", err)
	}
	defer func() { _ = sqlRows.Close() }()

	var out []Row
	for sqlRows.Next() {
		var (
			r      Row
			amount sql.NullInt64
			extRaw string
			opsRaw string
		)
		if err := sqlRows.Scan(&r.LedgerType, &r.BizNo, &r.InstanceCode, &r.Department, &r.ApplicantOpenID,
			&amount, &r.Supplier, &r.SupplierNorm, &r.PurposeL1, &r.PurposeL2, &r.BizDate, &extRaw, &opsRaw); err != nil {
			return nil, err
		}
		if amount.Valid {
			r.AmountCents = amount.Int64
			r.HasAmount = true
		}
		// ★ 解析失败不再静默当空（静默审计 C6）：记数并让 Build 把它带进 `warnings`。
		var badExt, badOps bool
		r.ArchiveExt, badExt = decodeJSONMap(extRaw)
		r.Ops, badOps = decodeJSONMap(opsRaw)
		if badExt || badOps {
			b.corruptRows++
		}
		out = append(out, r)
	}
	return out, sqlRows.Err()
}

// countOverduePending 统计超时未审实例（PENDING 且早于 72 小时）。
func (b *Builder) countOverduePending(ctx context.Context, q Query) (int, error) {
	cutoff := b.now().Add(-time.Duration(defaultReviewHours) * time.Hour).UTC().Format(time.RFC3339)
	where := "a.status = 'PENDING' AND a.created_at < ?"
	args := []any{cutoff}
	if strings.TrimSpace(q.InstanceRowSQL) != "" {
		where += " AND (" + q.InstanceRowSQL + ")"
		args = append(args, q.InstanceRowArgs...)
	}
	var n int
	if err := b.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM t_instance a WHERE `+where, args...).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// countGroupRejectedUndisposed 统计集团驳回后未处置的报送登记。
//
// ★ 必须在 SQL 层施加报送行过滤（`q.SubmissionRowSQL`）：本指标是异常面板的对外指标，
// 只有 `ALL` 范围的角色才应看到全量；受限角色只统计「本人登记」的报送
// （`t_submission` 的身份锚点为 department / applicant_open_id / assigned_open_id / acceptors，
//
//	见 permission.RowFilterForSubmission；migrations/0003 补齐了这四列）。
func (b *Builder) countGroupRejectedUndisposed(ctx context.Context, q Query) (int, error) {
	where := []string{
		`COALESCE(grp_state,'') LIKE '%驳回%'`,
		`COALESCE(reject_reason,'') = ''`,
	}
	var args []any
	if strings.TrimSpace(q.SubmissionRowSQL) != "" {
		where = append(where, "("+q.SubmissionRowSQL+")")
		args = append(args, q.SubmissionRowArgs...)
	}
	var n int
	// ★ 表别名 `s` 必须与 permission.RowFilterForSubmission(..., "s") 的别名一致
	//（Q14-B 第 5 项改为按真实列过滤后，谓词里带别名，FROM 不加别名会直接 SQL 报错）。
	err := b.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM t_submission s WHERE `+strings.Join(where, " AND "), args...).Scan(&n)
	if err != nil {
		return 0, err
	}
	return n, nil
}

// SplitSuspectGroup 拆分嫌疑组明细（N-060 F9 · FR-M4-06：从「仅计数」到「可导出清单」）。
// 月度＝month 字段；BizNose 供导出定位到行。
type SplitSuspectGroup struct {
	Supplier  string   `json:"supplier"`
	PurposeL2 string   `json:"purpose_l2"`
	Month     string   `json:"month"`
	SumCents  int64    `json:"sum_cents"`
	Count     int      `json:"count"`
	BizNos    []string `json:"biz_nos"`
}

// listSplitSuspect 组明细：同供应商 + 同品类（二级明细）月累计 ≥ 阈值且 ≥2 笔。
// 分组口径与原 countSplitSuspect 逐字一致（Q20 归一分组键）；count ＝ len(命中组)。
func (b *Builder) listSplitSuspect(rows []Row) []SplitSuspectGroup {
	type gk struct{ supplier, cat, month string }
	type gval struct {
		sum   int64
		cnt   int
		bizNo []string
	}
	groups := map[gk]*gval{}
	order := []gk{}
	for _, r := range rows {
		if strings.TrimSpace(r.Supplier) == "" {
			continue
		}
		m := monthOf(r.BizDate)
		if m == "" {
			continue
		}
		// ★ Q20：按**归一分组键**分组 —— 用原名分组会让"换个写法"直接绕过转档预警。
		sup := r.SupplierNorm
		if sup == "" {
			sup = r.Supplier
		}
		k := gk{sup, r.PurposeL2, m}
		g := groups[k]
		if g == nil {
			g = &gval{}
			groups[k] = g
			order = append(order, k)
		}
		g.sum += r.AmountCents
		g.cnt++
		if r.BizNo != "" {
			g.bizNo = append(g.bizNo, r.BizNo)
		}
	}
	out := []SplitSuspectGroup{}
	for _, k := range order { // 按行序稳定输出（可测、可复算）
		g := groups[k]
		if g.sum >= b.splitCents && g.cnt >= 2 {
			out = append(out, SplitSuspectGroup{
				Supplier: k.supplier, PurposeL2: k.cat, Month: k.month,
				SumCents: g.sum, Count: g.cnt, BizNos: g.bizNo,
			})
		}
	}
	return out
}

// countSubmitOverdue 提交超期笔数（N-060 G2 · FR-M6-03 · R-33，逐字 spec formula）：
// L06 行的 submit_group_at 为空 ∧ 距 hunan_completed_at 已超 deadlineWorkdays 个工作日。
//   - 工作日口径＝submission.AddWorkingDays（跳周六日；HolidayChecker 非空一并跳法定节假日
//     —— Q18 定案，复用该挂点、不另起一套）；
//   - deadline<=0 ＝ 未装配 ⇒ 返回 0 且由调用方输出 undefined_criteria（不猜 3）；
//   - hunan_completed_at 缺失/不可解析 ⇒ 不计（不误报）。
func countSubmitOverdue(rows []Row, deadlineWorkdays int, now time.Time) int {
	if deadlineWorkdays <= 0 {
		return 0
	}
	n := 0
	for _, r := range rows {
		if strings.TrimSpace(r.OpsStr("submit_group_at")) != "" {
			continue // 公式前半：提交集团日期为空
		}
		done := strings.TrimSpace(jsonStr(r.ArchiveExt["hunan_completed_at"]))
		if done == "" {
			continue
		}
		d, ok := submission.ParseDate(done)
		if !ok {
			continue // 不可解析不误报（可见性由行级数据质量另行负责）
		}
		deadline := submission.AddWorkingDays(d, deadlineWorkdays)
		if now.After(deadline) {
			n++
		}
	}
	return n
}

// countSplitSuspect 统计拆分嫌疑组数（＝命中组数；F9 改为 list 的薄封装，调用面不变）。
func (b *Builder) countSplitSuspect(rows []Row) int {
	return len(b.listSplitSuspect(rows))
}

// countEmergencyUnclosed 统计紧急采购超 24 小时未补录或未核销闭合。
func (b *Builder) countEmergencyUnclosed(rows []Row) int {
	cutoff := b.now().Add(-time.Duration(defaultEmergencyHrs) * time.Hour)
	n := 0
	for _, r := range rows {
		if !strings.Contains(r.OpsStr(keyMethod), "紧急") {
			continue
		}
		if strings.TrimSpace(r.OpsStr(keyClosed)) == "是" {
			continue
		}
		if t, ok := parseDayStrict(r.BizDate); ok && !t.After(cutoff) {
			n++
		}
	}
	return n
}

// ---------- 行访问器 ----------

// Assigned 解析被指定经办人：优先运营表，回退存档扩展字段（Q14 口径：运营表可写）。
func (r Row) Assigned() string {
	if v := jsonStr(r.Ops[keyAssigned]); v != "" {
		return v
	}
	if v := jsonStr(r.ArchiveExt[keyAssigned]); v != "" {
		return v
	}
	if v := jsonStr(r.Ops[keyAssignedCN]); v != "" {
		return v
	}
	if v := jsonStr(r.ArchiveExt[keyAssignedCN]); v != "" {
		return v
	}
	// ★ N-015 批 2：审批时点指定经办落 ext 的**规格列名**（designated_purchaser）——
	//   它是 L03「指定经办人」的权威列，前四个键是运营表/人工登记口径的兼容读法。
	return jsonStr(r.ArchiveExt["designated_purchaser"])
}

// OpsStr 读取运营字段字符串值。
func (r Row) OpsStr(key string) string { return jsonStr(r.Ops[key]) }

// OpsInt 读取运营字段整数值。
func (r Row) OpsInt(key string) (int64, bool) { return jsonInt(r.Ops[key]) }

// ---------- 通用小工具 ----------

func splitByType(rows []Row, types ...string) map[string][]Row {
	out := make(map[string][]Row, len(types))
	for _, t := range types {
		out[t] = nil
	}
	for _, r := range rows {
		if _, ok := out[r.LedgerType]; ok {
			out[r.LedgerType] = append(out[r.LedgerType], r)
		}
	}
	return out
}

func filterByMonth(rows []Row, month string) []Row {
	out := make([]Row, 0, len(rows))
	for _, r := range rows {
		if monthOf(r.BizDate) == month {
			out = append(out, r)
		}
	}
	return out
}

func groupMoney(rows []Row, keyFn func(Row) string, topN int) []map[string]any {
	sums := map[string]int64{}
	for _, r := range rows {
		k := strings.TrimSpace(keyFn(r))
		if k == "" {
			k = "（未填）"
		}
		sums[k] += r.AmountCents
	}
	return topMoneyBars(sums, topN)
}

// topMoneyBarsNamed 同 topMoneyBars，但允许用**展示名**替换分组键（Q20：分组按归一值、
// 展示按原名）。names 为空时等价于 topMoneyBars。
func topMoneyBarsNamed(sums map[string]int64, names map[string]string, topN int) []map[string]any {
	out := topMoneyBars(sums, topN)
	for _, p := range out {
		if k, ok := p["x"].(string); ok && names != nil {
			if n, ok2 := names[k]; ok2 && n != "" {
				p["x"] = n
			}
		}
	}
	return out
}

func topMoneyBars(sums map[string]int64, topN int) []map[string]any {
	type kv struct {
		k string
		v int64
	}
	list := make([]kv, 0, len(sums))
	for k, v := range sums {
		list = append(list, kv{k, v})
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].v != list[j].v {
			return list[i].v > list[j].v
		}
		return list[i].k < list[j].k
	})
	if topN > 0 && len(list) > topN {
		list = list[:topN]
	}
	out := make([]map[string]any, 0, len(list))
	for _, it := range list {
		out = append(out, moneyPoint(it.k, it.v))
	}
	return out
}

func countOpsContains(rows []Row, key, sub string) int {
	n := 0
	for _, r := range rows {
		if strings.Contains(r.OpsStr(key), sub) {
			n++
		}
	}
	return n
}

func countOpsContainsAny(rows []Row, key string, subs ...string) int {
	n := 0
	for _, r := range rows {
		v := r.OpsStr(key)
		for _, s := range subs {
			if strings.Contains(v, s) {
				n++
				break
			}
		}
	}
	return n
}

// countChangeAnomalyListed 统计 `L09.exception_type = 采购变更` 且
// `is_anomaly_listed = true` 的行数（N-046）。
// ★ 两列生产者＝httpapi#injectPCSSSystemFields（PC 提交期）落 archive ext_json；
// 值形态兼容：is_anomaly_listed 的 bool true 与 ext 往返后的字符串「是」/「true」；
// 「否」/「false」不得计入（isTruthy 的 false 词表天然排除）。
func countChangeAnomalyListed(rows []Row) int {
	n := 0
	for _, r := range rows {
		ev, ok := r.ArchiveExt["exception_type"]
		if !ok || ev == nil {
			continue
		}
		if strings.TrimSpace(fmt.Sprint(ev)) != "采购变更" {
			continue
		}
		lv, ok := r.ArchiveExt["is_anomaly_listed"]
		if !ok || lv == nil {
			continue
		}
		if isTruthy(lv) {
			n++
		}
	}
	return n
}

// countL06Unverified 统计 `L06.收款账户已核验` 为「否」的笔数（N-034：＝变更过的笔数）。
// 值形态兼容两种落账编码：bool false（SUB 表单原值）与字符串「否」。
func countL06Unverified(rows []Row) int {
	n := 0
	for _, r := range rows {
		v, ok := r.ArchiveExt["payee_account_verified"]
		if !ok || v == nil {
			continue
		}
		switch x := v.(type) {
		case bool:
			if !x {
				n++
			}
		case string:
			s := strings.TrimSpace(x)
			if s == "否" || strings.EqualFold(s, "false") {
				n++
			}
		}
	}
	return n
}

// countExtRegistered 存档 ext 列的**列级守卫源**（N-034：writable:false 的落账带入列
// 存于 archive ext_json、不在 ops）：该键有登记值（非 null、非空串）的行数。
func countExtRegistered(rows []Row, key string) int {
	n := 0
	for _, r := range rows {
		v, ok := r.ArchiveExt[key]
		if !ok || v == nil {
			continue
		}
		if s, isStr := v.(string); isStr && strings.TrimSpace(s) == "" {
			continue
		}
		n++
	}
	return n
}

// countRegistered 人工登记列的**列级守卫源**：该列有登记值（非 null、非空串）的行数。
// 「登记为否」也是登记（算源）；整列无人登记 ⇒ 0 ⇒ 守卫触发 not_connected（r3）。
func countRegistered(rows []Row, key string) int {
	n := 0
	for _, r := range rows {
		v, ok := r.Ops[key]
		if !ok || v == nil {
			continue
		}
		if s, isStr := v.(string); isStr && strings.TrimSpace(s) == "" {
			continue
		}
		n++
	}
	return n
}

func countTruthy(rows []Row, key string) int {
	n := 0
	for _, r := range rows {
		if isTruthy(r.Ops[key]) {
			n++
		}
	}
	return n
}

func countHandlerIncomplete(rows []Row) int {
	n := 0
	for _, r := range rows {
		if strings.TrimSpace(r.OpsStr(keyDoneDate)) != "" {
			continue
		}
		if strings.TrimSpace(r.OpsStr(keyStatus)) == "已完成" {
			continue
		}
		n++
	}
	return n
}

// ---------- 指标级可用性守卫（global_rules.r6 / connected_requires 逐条落地） ----------
//
// ★ 语义：`connected` 的判据是「每个指标都能自证有没有数据」——守卫在**指标层**：
//   needGuard=false ⇒ 0 是真实语义，直接出数（14 的 in_flight_orders / monthly_amount_trend）；
//   needGuard=true 且源行=0 ⇒ 输出 not_connected「数据未接入」，绝不显示 0
//   （0 与「没接上」同形 —— r3 的逐指标化，r6 要求「每个指标各自声明」）。

const notConnectedMsg = "数据未接入"

// guardedAlert 带守卫的告警指标。
func guardedAlert(key, desc string, count, srcRows int, needGuard bool) map[string]any {
	if needGuard && srcRows == 0 {
		return map[string]any{
			"key": key, "label": desc,
			"status": "not_connected", "message": notConnectedMsg,
		}
	}
	return alert(key, desc, count)
}

// guardedValue 带守卫的指标卡（守卫时无 value —— 前端按 status 渲染文案）。
func guardedValue(key, label string, v any, srcRows int, needGuard bool) map[string]any {
	if needGuard && srcRows == 0 {
		return map[string]any{
			"key": key, "label": label,
			"status": "not_connected", "message": notConnectedMsg,
		}
	}
	return card(key, label, v)
}

// guardedChart 带守卫的图表（守卫时 series 不给 —— 空图与没接上不得同形）。
func guardedChart(key, chartType string, series []map[string]any, srcRows int, needGuard bool) Chart {
	if needGuard && srcRows == 0 {
		return Chart{Key: key, Type: chartType, Status: "not_connected", Message: notConnectedMsg}
	}
	return Chart{Key: key, Type: chartType, Series: series}
}

// countInstanceRows 实例行数（overdue_unapproved 守卫源 —— 行级过滤同 SQL 层）。
func (b *Builder) countInstanceRows(ctx context.Context, q Query) (int, error) {
	where := "1=1"
	var args []any
	if strings.TrimSpace(q.InstanceRowSQL) != "" {
		where = "(" + q.InstanceRowSQL + ")"
		args = append(args, q.InstanceRowArgs...)
	}
	var n int
	if err := b.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM t_instance a WHERE `+where, args...).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// countSubmissionRows 报送行数（group_rejected_unhandled 守卫源，别名 s 同 RowFilter）。
func (b *Builder) countSubmissionRows(ctx context.Context, q Query) (int, error) {
	where := "1=1"
	var args []any
	if strings.TrimSpace(q.SubmissionRowSQL) != "" {
		where = "(" + q.SubmissionRowSQL + ")"
		args = append(args, q.SubmissionRowArgs...)
	}
	var n int
	if err := b.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM t_submission s WHERE `+where, args...).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

func card(key, label string, v any) map[string]any {
	return map[string]any{"key": key, "label": label, "value": v}
}

func moneyCard(key, label string, cents int64) map[string]any {
	return map[string]any{"key": key, "label": label, "amount_cents": cents, "amount_display": FormatCents(cents)}
}

func countPoint(x string, y int64) map[string]any {
	return map[string]any{"x": x, "y": y}
}

func moneyPoint(x string, cents int64) map[string]any {
	return map[string]any{"x": x, "amount_cents": cents, "amount_display": FormatCents(cents)}
}

func alert(key, desc string, count int) map[string]any {
	return map[string]any{"key": key, "label": desc, "level": "warn", "count": count}
}

// ---------- 解析与格式化小工具 ----------

// decodeJSONMap 解析 JSON 文本为对象；第二个返回值表示**是否解析失败**。
//
// ★ 必须把"失败"返回出去（静默审计 C6）：原先 `_ = json.Unmarshal` 让坏数据
//
//	与"没有数据"完全等价，指标静默偏低而无人察觉。
func decodeJSONMap(raw string) (map[string]any, bool) {
	return jsonutil.ObjectOrEmpty(raw)
}

func jsonStr(v any) string {
	switch t := v.(type) {
	case string:
		return strings.TrimSpace(t)
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case int64:
		return strconv.FormatInt(t, 10)
	case json.Number:
		return t.String()
	default:
		return ""
	}
}

func jsonInt(v any) (int64, bool) {
	switch t := v.(type) {
	case float64:
		return int64(t), true
	case int64:
		return t, true
	case int:
		return int64(t), true
	case json.Number:
		if i, err := t.Int64(); err == nil {
			return i, true
		}
	case string:
		if i, err := strconv.ParseInt(strings.TrimSpace(t), 10, 64); err == nil {
			return i, true
		}
	}
	return 0, false
}

func isTruthy(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case float64:
		return t != 0
	case int:
		return t != 0
	case int64:
		return t != 0
	case string:
		s := strings.TrimSpace(t)
		if s == "" {
			return false
		}
		switch strings.ToLower(s) {
		case "0", "false", "否", "no", "无", "none":
			return false
		}
		return true
	}
	return false
}

func intFromAny(v any) int {
	if i, ok := jsonInt(v); ok {
		return int(i)
	}
	return 0
}

func monthOf(bizDate string) string {
	s := strings.TrimSpace(bizDate)
	if len(s) >= 7 {
		return s[:7]
	}
	return ""
}

func cycleDays(r Row) (int, bool) {
	start, ok := parseDayStrict(r.BizDate)
	if !ok {
		return 0, false
	}
	doneStr := r.OpsStr(keyDoneDate)
	if doneStr == "" {
		doneStr = r.OpsStr(keyActualArrival) // L11 派生行的到货日期
	}
	end, ok := parseDayStrict(doneStr)
	if !ok {
		return 0, false
	}
	days := int(math.Round(end.Sub(start).Hours() / 24))
	if days < 0 {
		days = 0
	}
	return days, true
}

func parseDayStrict(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	layouts := []string{"2006-01-02", time.RFC3339, "2006-01-02T15:04:05", "2006/01/02"}
	for _, l := range layouts {
		if t, err := time.Parse(l, s); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}

// lastMonths 返回截至 period（含）的 n 个月（YYYY-MM，升序）。
func lastMonths(period string, n int) []string {
	y, m, ok := parsePeriod(period)
	if !ok || n <= 0 {
		return nil
	}
	out := make([]string, 0, n)
	for i := n - 1; i >= 0; i-- {
		yy, mm := shiftMonth(y, m, -i)
		out = append(out, fmt.Sprintf("%04d-%02d", yy, mm))
	}
	return out
}

// windowStart 返回 period 往前 back 个月的账期（用于时间窗下界）。
func windowStart(period string, back int) string {
	y, m, ok := parsePeriod(period)
	if !ok {
		return ""
	}
	yy, mm := shiftMonth(y, m, -back)
	return fmt.Sprintf("%04d-%02d", yy, mm)
}

func parsePeriod(period string) (int, int, bool) {
	s := strings.TrimSpace(period)
	if len(s) < 7 {
		return 0, 0, false
	}
	var y, m int
	if _, err := fmt.Sscanf(s[:7], "%d-%d", &y, &m); err != nil || y == 0 || m < 1 || m > 12 {
		return 0, 0, false
	}
	return y, m, true
}

func shiftMonth(year, month, delta int) (int, int) {
	idx := year*12 + (month - 1) + delta
	return idx / 12, idx%12 + 1
}

func round1(f float64) float64 { return math.Round(f*10) / 10 }
func round4(f float64) float64 { return math.Round(f*10000) / 10000 }

// FormatCents 将分格式化为带千分位的字符串（金额出参 * 元）。
func FormatCents(cents int64) string {
	neg := cents < 0
	if neg {
		cents = -cents
	}
	yuan := cents / 100
	frac := cents % 100
	s := strconv.FormatInt(yuan, 10)
	var b strings.Builder
	pre := len(s) % 3
	if pre > 0 {
		b.WriteString(s[:pre])
		if len(s) > pre {
			b.WriteString(",")
		}
	}
	for i := pre; i < len(s); i += 3 {
		b.WriteString(s[i : i+3])
		if i+3 < len(s) {
			b.WriteString(",")
		}
	}
	out := b.String() + "." + fmt.Sprintf("%02d", frac)
	if neg {
		out = "-" + out
	}
	return out
}
