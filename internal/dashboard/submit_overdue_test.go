package dashboard

// N-060 G2（FR-M6-03 · R-33）：看板 16 第 13 指标 submit_overdue（提交超期）。
// 判据：countSubmitOverdue 纯函数（spec formula 逐字）＋ 守卫/未装配分支组合。

import (
	"strings"
	"testing"
	"time"
)

func TestSubmitOverdueCountAndGuards(t *testing.T) {
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	deadline := 3 // doc_chains.SUB.deadline_workdays（测试装配值；生产由 spec 装载注入）

	l06 := func(submit, hunanDone string) []Row {
		return []Row{{
			LedgerType: "L06", BizNo: "SUB-2610-0001",
			Ops:        map[string]any{"submit_group_at": submit},
			ArchiveExt: map[string]any{"hunan_completed_at": hunanDone},
		}}
	}
	// 8 天前的完成日（跨多个周末 ⇒ 一定 > 3 工作日）
	long := now.AddDate(0, 0, -8).Format("2006-01-02")
	// 昨天（未超 3 工作日）
	fresh := now.AddDate(0, 0, -1).Format("2006-01-02")

	// ① 公式：提交集团日期为空 ∧ 距完成超 3 工作日 ⇒ 计 1
	if got := countSubmitOverdue(l06("", long), deadline, now); got != 1 {
		t.Errorf("超期未提交 = %d, 期望 1（spec formula 逐字）", got)
	}
	// ② 已提交（submit_group_at 非空）⇒ 不计（公式前半不成立）
	if got := countSubmitOverdue(l06("2026-10-01", long), deadline, now); got != 0 {
		t.Errorf("已提交 = %d, 期望 0", got)
	}
	// ③ 未超 3 工作日 ⇒ 不计（工作日口径经 submission.AddWorkingDays）
	if got := countSubmitOverdue(l06("", fresh), deadline, now); got != 0 {
		t.Errorf("未超期 = %d, 期望 0（3 工作日内）", got)
	}
	// ④ 完成日期缺失/不可解析 ⇒ 不误报
	if got := countSubmitOverdue(l06("", ""), deadline, now); got != 0 {
		t.Errorf("缺完成日期 = %d, 期望 0（不误报）", got)
	}
	if got := countSubmitOverdue(l06("", "not-a-date"), deadline, now); got != 0 {
		t.Errorf("不可解析 = %d, 期望 0（不误报）", got)
	}

	// ⑤ 守卫组合（r3）：L06 无行 ⇒ guardedAlert not_connected 且**无 count**
	a0 := guardedAlert("submit_overdue", "提交超期（湖南侧完成 3 个工作日未提交集团）",
		countSubmitOverdue(nil, deadline, now), 0, true)
	if a0["status"] != "not_connected" {
		t.Errorf("L06 无行应 not_connected（不得显示 0），实为 %v", a0)
	}
	if _, has := a0["count"]; has {
		t.Errorf("not_connected 不得带 count：%v", a0)
	}
	// ⑥ 未装配（deadline<=0）⇒ count 恒 0（早退；调用方 buildAnomaly 据
	//    deadlineWorkdays<=0 输出 undefined_criteria —— 不把口径未配显示成超期数）
	if got := countSubmitOverdue(l06("", long), 0, now); got != 0 {
		t.Errorf("deadline 未装配应恒 0（早退），实为 %d（不得拿 0 当 3 天用）", got)
	}
	// ⑦ 有行未超期 ⇒ 正常出 count=0（真实 0，非守卫态）
	aOK := guardedAlert("submit_overdue", "提交超期（湖南侧完成 3 个工作日未提交集团）",
		countSubmitOverdue(l06("", fresh), deadline, now), 1, true)
	if c, has := aOK["count"]; !has || c != 0 {
		t.Errorf("有行未超期应 count=0（真实 0），实为 %v（has=%v）", aOK, has)
	}
	if aOK["status"] == "not_connected" {
		t.Errorf("有行不得 not_connected：%v", aOK)
	}
	// ⑧ 中文文案含「工作日」（人读可辨）
	if !strings.Contains(aOK["label"].(string), "工作日") {
		t.Errorf("label 应含工作日：%v", aOK["label"])
	}
}
