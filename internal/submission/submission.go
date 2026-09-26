// Package submission 承载 M6「报送与凭证包」的领域逻辑，并附 M1 备付金口径校验所需的
// 纯函数（日期 / 账期 / 金额格式化）。
//
// 制度与接口纪律（对齐 docs/05-API.md §3.5 / §3.6、docs/01-PRD.md §5.7、TC-15 / TC-26）：
//   - ★「无凭证视为未提交」：receipt_ref 为空 → submit_state 强制判为「未提交」（FR-M6-02）；
//   - 「3 个工作日提交时限」：自湖南侧完成日期起算，跳过周六 / 周日推算截止日（FR-M6-03）；
//   - 集团侧字段（集团受理编号 / 集团流程状态 / 付款完成日期）仅作人工登记，
//     本包不提供任何自动回填路径，字段来源统一标注为「人工登记」（FR-M6-07）；
//   - 备付金「不定额、有多少用多少，以实有金额为限」；按月凭单据核销，
//     余额以「核销后实有金额」为准（FR-M1-04 / FR-M1-07）。
package submission

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/chadhao/jx-procurement-platform/internal/store"
)

// 报送状态枚举（docs/05-API.md §7）。
const (
	StateUnsubmitted = "未提交"
	StateSubmitted   = "已提交"
	StateProcessing  = "办理中"
	StatePaid        = "已付款"
	StateRejected    = "已驳回"
)

// 集团驳回处置方式（FR-M6-08）：取消 / 驳回重走，二择一。
const (
	RejectActionCancel = "取消"
	RejectActionRedo   = "驳回重走"
)

const (
	dateLayout   = "2006-01-02"
	periodLayout = "2006-01"
	// submissionWorkingDays 提交时限：湖南侧流程完成后 3 个工作日（FR-M6-03）。
	submissionWorkingDays = 3
	// groupSourceLabel 集团侧字段来源标注（FR-M6-07：仅人工登记、不回填）。
	groupSourceLabel = "人工登记"
)

// ValidState 判断是否为合法报送状态枚举。
func ValidState(s string) bool {
	switch strings.TrimSpace(s) {
	case StateUnsubmitted, StateSubmitted, StateProcessing, StatePaid, StateRejected:
		return true
	default:
		return false
	}
}

// ComputeSubmitState 依据「无凭证视为未提交」计算最终报送状态（FR-M6-02 / TC-15）。
//
// 规则：receipt_ref 为空 → 一律判为「未提交」（忽略请求值）；否则采用请求的合法状态，
// 请求状态非法 / 为空时默认「已提交」。
func ComputeSubmitState(receiptRef, requested string) string {
	if strings.TrimSpace(receiptRef) == "" {
		return StateUnsubmitted
	}
	requested = strings.TrimSpace(requested)
	if ValidState(requested) {
		return requested
	}
	return StateSubmitted
}

// ValidRejectAction 判断驳回处置方式是否合法（FR-M6-08）。
func ValidRejectAction(action string) bool {
	action = strings.TrimSpace(action)
	return action == RejectActionCancel || action == RejectActionRedo
}

// ValidDate 判断字符串是否为合法的 YYYY-MM-DD 日期。
func ValidDate(s string) bool {
	_, ok := ParseDate(s)
	return ok
}

// ParseDate 解析 YYYY-MM-DD（UTC）。
func ParseDate(s string) (time.Time, bool) {
	t, err := time.Parse(dateLayout, strings.TrimSpace(s))
	if err != nil {
		return time.Time{}, false
	}
	return t.UTC(), true
}

// NormalizePeriod 校验并规范化账期 YYYY-MM。
func NormalizePeriod(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", false
	}
	t, err := time.Parse(periodLayout, s)
	if err != nil {
		return "", false
	}
	return t.Format(periodLayout), true
}

// CurrentPeriod 返回给定时刻所在账期（YYYY-MM，UTC）。
func CurrentPeriod(now time.Time) string {
	return now.UTC().Format(periodLayout)
}

// AddWorkingDays 在给定日期上叠加 n 个工作日（跳过周六 / 周日）。
//
// 口径说明：仅按「自然周的周六 / 周日」剔除，不引入法定节假日日历（节假日配置文件不在本期范围）。
func AddWorkingDays(from time.Time, n int) time.Time {
	d := time.Date(from.Year(), from.Month(), from.Day(), 0, 0, 0, 0, time.UTC)
	for i := 0; i < n; {
		d = d.AddDate(0, 0, 1)
		if d.Weekday() != time.Saturday && d.Weekday() != time.Sunday {
			i++
		}
	}
	return d
}

// Deadline 计算「湖南侧完成日期 + 3 个工作日」的提交截止日（FR-M6-03）。
func Deadline(hnFinishDate string) (time.Time, bool) {
	t, ok := ParseDate(hnFinishDate)
	if !ok {
		return time.Time{}, false
	}
	return AddWorkingDays(t, submissionWorkingDays), true
}

// IsOverdue 判断「3 个工作日提交时限」是否超期（FR-M6-03）。
//
//   - 已填提交日期且已实际提交 → 实际提交日晚于截止日即超期；
//   - 尚未提交 → 当前日期晚于截止日即超期；
//   - 无湖南侧完成日期 → 无法判定，返回 false（不误报）。
func IsOverdue(hnFinishDate, submitDate, state string, now time.Time) bool {
	dl, ok := Deadline(hnFinishDate)
	if !ok {
		return false
	}
	if sd, ok := ParseDate(submitDate); ok && Submitted(state) {
		return sd.After(dl)
	}
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	return today.After(dl)
}

// Submitted 判断状态是否表示「已实际提交集团」（非未提交）。
func Submitted(state string) bool {
	switch strings.TrimSpace(state) {
	case StateUnsubmitted, "":
		return false
	default:
		return true
	}
}

// FormatCents 将分格式化为带千分位的金额字符串。
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

// ItemMaps 将关联单据行转为出参结构。
func ItemMaps(items []store.SubmissionItem) []map[string]any {
	out := make([]map[string]any, 0, len(items))
	for _, it := range items {
		out = append(out, map[string]any{
			"item_biz_no": it.ItemBizNo,
			"item_type":   it.ItemType,
		})
	}
	return out
}

// RecordMap 组装报送记录出参（docs/05-API.md §4.5）。
//
// ★ submit_state 在读取时按「无凭证视为未提交」重算，保证任何写入路径都无法绕过 FR-M6-02。
func RecordMap(s store.Submission, items []store.SubmissionItem, now time.Time) map[string]any {
	state := ComputeSubmitState(s.ReceiptRef, s.SubmitState)
	m := map[string]any{
		"id":             s.ID,
		"biz_no":         s.BizNo,
		"subject_type":   s.SubjectType,
		"pay_method":     s.PayMethod,
		"hn_finish_date": s.HNFinishDate,
		"submit_date":    s.SubmitDate,
		"receipt_ref":    s.ReceiptRef,
		"submit_state":   state,
		"overdue":        IsOverdue(s.HNFinishDate, s.SubmitDate, state, now),
		// 集团侧字段：仅人工登记、不回填（FR-M6-07）。
		"group": map[string]any{
			"accept_no": s.GrpAcceptNo,
			"state":     s.GrpState,
			"paid_date": s.PaidDate,
			"source":    groupSourceLabel,
		},
		"items":      ItemMaps(items),
		"created_at": fmtTime(s.CreatedAt),
		"updated_at": fmtTime(s.UpdatedAt),
	}
	if s.AmountCents != nil {
		m["amount_cents"] = *s.AmountCents
		m["amount_display"] = FormatCents(*s.AmountCents)
	} else {
		m["amount_cents"] = nil
		m["amount_display"] = ""
	}
	if dl, ok := Deadline(s.HNFinishDate); ok {
		m["deadline"] = dl.Format(dateLayout)
	} else {
		m["deadline"] = nil
	}
	if strings.TrimSpace(s.RejectReason) != "" {
		m["reject_reason"] = s.RejectReason
	}
	return m
}

// ---------- 凭证包组装（FR-M6-05 / FR-M6-06） ----------

// PackageFile 凭证包内的单个文件。
type PackageFile struct {
	Name string
	Data []byte
}

// PackageFiles 组装凭证包文件集合：
//   - 报送登记.json（关联单据清单 + 移交凭证 + 湖南侧完成日期 + 付款方式等）；
//   - 关联单据清单.csv；
//   - 移交凭证.txt；
//   - 凭证包说明.txt。
func PackageFiles(s store.Submission, items []store.SubmissionItem, now time.Time) []PackageFile {
	rec := RecordMap(s, items, now)
	jsonBytes, _ := json.MarshalIndent(rec, "", "  ")

	var csv strings.Builder
	csv.WriteString("序号,单据类型,单据编号\n")
	for i, it := range items {
		csv.WriteString(fmt.Sprintf("%d,%s,%s\n", i+1, csvField(it.ItemType), csvField(it.ItemBizNo)))
	}

	var receipt strings.Builder
	receipt.WriteString("移交凭证（签收记录）\n====================\n")
	if strings.TrimSpace(s.ReceiptRef) == "" {
		receipt.WriteString("凭证号：无\n")
		receipt.WriteString("★ 无凭证视为未提交（制度第四十八条 / FR-M6-02）。\n")
	} else {
		receipt.WriteString("凭证号：" + s.ReceiptRef + "\n")
		receipt.WriteString("报送状态：" + ComputeSubmitState(s.ReceiptRef, s.SubmitState) + "\n")
	}
	receipt.WriteString("湖南侧完成日期：" + s.HNFinishDate + "\n")
	receipt.WriteString("付款方式：" + s.PayMethod + "\n")

	return []PackageFile{
		{Name: "报送登记.json", Data: jsonBytes},
		{Name: "关联单据清单.csv", Data: []byte(csv.String())},
		{Name: "移交凭证.txt", Data: []byte(receipt.String())},
		{Name: "凭证包说明.txt", Data: []byte(buildReadme(s, items, now))},
	}
}

// BuildPackageZip 生成 zip 凭证包。
func BuildPackageZip(s store.Submission, items []store.SubmissionItem, now time.Time) ([]byte, error) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, f := range PackageFiles(s, items, now) {
		w, err := zw.Create(f.Name)
		if err != nil {
			return nil, fmt.Errorf("submission: 创建凭证包文件失败: %w", err)
		}
		if _, err := w.Write(f.Data); err != nil {
			return nil, fmt.Errorf("submission: 写入凭证包失败: %w", err)
		}
	}
	if err := zw.Close(); err != nil {
		return nil, fmt.Errorf("submission: 关闭凭证包失败: %w", err)
	}
	return buf.Bytes(), nil
}

// BuildPackagePDF 生成 pdf 凭证包。
//
// 说明：本机无法新增第三方依赖，且 PDF 内置字体（Helvetica）不含中文字形，
// 故 PDF 采用英文标签 + ASCII 降级渲染（非 ASCII 字符以 '.' 代替）；
// 完整 UTF-8 数据以 zip 包内「报送登记.json」为准（zip 为凭证包规范格式）。
func BuildPackagePDF(s store.Submission, items []store.SubmissionItem, now time.Time) []byte {
	return minimalPDF(pdfLines(s, items, now))
}

func buildReadme(s store.Submission, items []store.SubmissionItem, now time.Time) string {
	state := ComputeSubmitState(s.ReceiptRef, s.SubmitState)
	var b strings.Builder
	b.WriteString("报送凭证包\n==========\n")
	b.WriteString("生成时间：" + fmtTime(now) + "\n")
	b.WriteString(fmt.Sprintf("报送 id：%d\n", s.ID))
	b.WriteString("业务单号：" + s.BizNo + "\n")
	b.WriteString("事项类型：" + s.SubjectType + "\n")
	if s.AmountCents != nil {
		b.WriteString("金额：" + FormatCents(*s.AmountCents) + " 元\n")
	}
	b.WriteString("付款方式：" + s.PayMethod + "\n")
	b.WriteString("湖南侧完成日期：" + s.HNFinishDate + "\n")
	b.WriteString("提交集团日期：" + s.SubmitDate + "\n")
	if strings.TrimSpace(s.ReceiptRef) == "" {
		b.WriteString("移交凭证（签收记录）：（无）★ 无凭证视为未提交\n")
	} else {
		b.WriteString("移交凭证（签收记录）：" + s.ReceiptRef + "\n")
	}
	b.WriteString("报送状态：" + state + "\n")
	if dl, ok := Deadline(s.HNFinishDate); ok {
		ov := "否"
		if IsOverdue(s.HNFinishDate, s.SubmitDate, state, now) {
			ov = "是"
		}
		b.WriteString("3 个工作日截止日：" + dl.Format(dateLayout) + "；是否超期：" + ov + "\n")
	}
	b.WriteString(fmt.Sprintf("关联单据数量：%d\n", len(items)))
	b.WriteString("\n集团侧字段（仅人工登记，不回填，FR-M6-07）：\n")
	b.WriteString("  集团受理编号：" + s.GrpAcceptNo + "\n")
	b.WriteString("  集团流程状态：" + s.GrpState + "\n")
	b.WriteString("  付款完成日期：" + s.PaidDate + "\n")
	return b.String()
}

func pdfLines(s store.Submission, items []store.SubmissionItem, now time.Time) []string {
	state := ComputeSubmitState(s.ReceiptRef, s.SubmitState)
	lines := []string{
		"Submission Package",
		"Generated: " + fmtTime(now),
		"ID: " + strconv.FormatInt(s.ID, 10),
		"BizNo: " + s.BizNo,
		"Subject: " + s.SubjectType,
		"PayMethod: " + s.PayMethod,
		"HN Finish Date: " + s.HNFinishDate,
		"Submit Date: " + s.SubmitDate,
		"Receipt Ref: " + s.ReceiptRef,
		"State: " + stateASCII(state),
		"Group(manual): accept_no=" + s.GrpAcceptNo + " state=" + s.GrpState + " paid_date=" + s.PaidDate,
		"--- Items ---",
	}
	for _, it := range items {
		lines = append(lines, it.ItemBizNo+" ["+it.ItemType+"]")
	}
	lines = append(lines, "Note: full UTF-8 data is inside the ZIP package (Submission JSON).")
	return lines
}

func stateASCII(state string) string {
	switch strings.TrimSpace(state) {
	case StateUnsubmitted:
		return "Unsubmitted"
	case StateSubmitted:
		return "Submitted"
	case StateProcessing:
		return "In-Process"
	case StatePaid:
		return "Paid"
	case StateRejected:
		return "Rejected"
	default:
		return "(none)"
	}
}

// minimalPDF 生成一个仅含单页文本的最小可用 PDF（ASCII）。
func minimalPDF(lines []string) []byte {
	const (
		lineHeight = 16
		startY     = 800
	)
	var content strings.Builder
	content.WriteString("BT\n/F1 11 Tf\n")
	y := startY
	for _, ln := range lines {
		content.WriteString(fmt.Sprintf("1 0 0 1 50 %d Tm (%s) Tj\n", y, escapePDF(asciiOnly(ln))))
		y -= lineHeight
		if y < 40 {
			break
		}
	}
	content.WriteString("ET\n")
	stream := content.String()

	var buf bytes.Buffer
	buf.WriteString("%PDF-1.4\n")
	offsets := make([]int, 0, 5)
	writeObj := func(s string) {
		offsets = append(offsets, buf.Len())
		buf.WriteString(s)
	}
	writeObj("1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n")
	writeObj("2 0 obj\n<< /Type /Pages /Kids [3 0 R] /Count 1 >>\nendobj\n")
	writeObj("3 0 obj\n<< /Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] /Resources << /Font << /F1 4 0 R >> >> /Contents 5 0 R >>\nendobj\n")
	writeObj("4 0 obj\n<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>\nendobj\n")
	offsets = append(offsets, buf.Len())
	buf.WriteString("5 0 obj\n<< /Length " + strconv.Itoa(len(stream)) + " >>\nstream\n")
	buf.WriteString(stream)
	buf.WriteString("endstream\nendobj\n")

	xrefPos := buf.Len()
	buf.WriteString("xref\n0 6\n")
	buf.WriteString("0000000000 65535 f \n")
	for _, off := range offsets {
		buf.WriteString(fmt.Sprintf("%010d 00000 n \n", off))
	}
	buf.WriteString("trailer\n<< /Size 6 /Root 1 0 R >>\nstartxref\n")
	buf.WriteString(strconv.Itoa(xrefPos))
	buf.WriteString("\n%%EOF\n")
	return buf.Bytes()
}

func asciiOnly(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= 32 && r < 127 {
			b.WriteRune(r)
		} else {
			b.WriteByte('.')
		}
	}
	return b.String()
}

func escapePDF(s string) string {
	r := strings.NewReplacer("\\", "\\\\", "(", "\\(", ")", "\\)")
	return r.Replace(s)
}

func csvField(s string) string {
	if strings.ContainsAny(s, ",\"\n\r") {
		return "\"" + strings.ReplaceAll(s, "\"", "\"\"") + "\""
	}
	return s
}

func fmtTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}
