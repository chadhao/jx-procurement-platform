package specload

// path_exists —— N-048 · 第 11 原语 Go 侧单测（与 Python prim_path_exists 同契约）。
//
// 三类反例必须各一（任务包 §2 T2）：选择器值写错 / 文件不存在 / 含点键写成裸键；
// 正例＝真 spec/institution-anchors.json 的 clauses[*].spec[*].at 全量 0 报错。

import (
	"encoding/json"
	"strings"
	"testing"
)

// runPathExists 以给定 files 跑单条 path_exists 判据，返回 problems。
func runPathExists(t *testing.T, files map[string][]byte, filePat, collect string) []string {
	t.Helper()
	args := map[string]json.RawMessage{
		"file":    json.RawMessage(`"` + filePat + `"`),
		"collect": json.RawMessage(`"` + collect + `"`),
	}
	decoded := map[string]any{}
	for k, b := range files {
		var v any
		if err := json.Unmarshal(b, &v); err == nil {
			decoded[k] = v
		}
	}
	return runOne(checkDef{ID: "T-PE", Primitive: "path_exists", Args: args}, files, decoded)
}

// peFiles 最小指针夹具：一条指针文件 + 一张目标表单。
func peFiles(ptrs []string) map[string][]byte {
	pl, _ := json.Marshal(map[string]any{"p": ptrs})
	fk, _ := json.Marshal(map[string]any{
		"sections": []any{
			map[string]any{"id": "header", "fields": []any{
				map[string]any{"name": "applicant", "rule": "required"},
			}},
		},
		"checks": []any{
			map[string]any{"id": "ok_check", "origin_ref": "docs/02-UseCase.md#UC-01"},
		},
	})
	return map[string][]byte{
		"spec/test_ptrs.json":  pl,
		"spec/forms/FAKE.json": fk,
	}
}

// TestPathExistsRejectsBadSelector 反例 1：选择器值写错 ⇒ 报「命中 0」。
func TestPathExistsRejectsBadSelector(t *testing.T) {
	files := peFiles([]string{"spec/forms/FAKE.json#sections[id=nope].fields"})
	probs := runPathExists(t, files, "spec/test_ptrs.json", "p[*]")
	if len(probs) != 1 {
		t.Fatalf("应恰 1 条报错，实为 %d: %v", len(probs), probs)
	}
	if !strings.Contains(probs[0], "命中 0 个节点") || !strings.Contains(probs[0], "sections[id=nope]") {
		t.Errorf("报错应含「命中 0 个节点」与指针原文：%s", probs[0])
	}
}

// TestPathExistsRejectsMissingFile 反例 2：文件不存在 ⇒ 报「文件不存在」（与命中0区分）。
func TestPathExistsRejectsMissingFile(t *testing.T) {
	files := peFiles([]string{"spec/forms/NO_SUCH_FILE.json#sections[id=header].fields"})
	probs := runPathExists(t, files, "spec/test_ptrs.json", "p[*]")
	if len(probs) != 1 {
		t.Fatalf("应恰 1 条报错，实为 %d: %v", len(probs), probs)
	}
	if !strings.Contains(probs[0], "文件不存在") {
		t.Errorf("报错应分类为「文件不存在」：%s", probs[0])
	}
	if strings.Contains(probs[0], "命中 0") {
		t.Errorf("文件不存在不应误报成命中 0：%s", probs[0])
	}
}

// TestPathExistsRejectsDottedKeyAsBare 反例 3：含点键写成裸键 ⇒ 命中 0
// （证明「必须用 [键]」不是空话；对照正例 TestPathExistsAcceptsDottedLiteralKey）。
func TestPathExistsRejectsDottedKeyAsBare(t *testing.T) {
	// 真 spec：params 下的键是字面 "reporting.monthly_cutoff_day"；
	// 写成裸键 reporting.monthly_cutoff_day ⇒ 切三段 ⇒ miss。
	files := map[string][]byte{}
	for k, v := range loadReal(t).ProblemsRaw {
		files[k] = v
	}
	ptrs, _ := json.Marshal(map[string]any{"p": []string{"spec/params.json#params.reporting.monthly_cutoff_day.critical_note"}})
	files["spec/test_ptrs.json"] = ptrs
	probs := runPathExists(t, files, "spec/test_ptrs.json", "p[*]")
	if len(probs) != 1 {
		t.Fatalf("应恰 1 条报错，实为 %d: %v", len(probs), probs)
	}
	if !strings.Contains(probs[0], "命中 0 个节点") {
		t.Errorf("含点键写裸键应报命中 0：%s", probs[0])
	}
}

// TestPathExistsAcceptsDottedLiteralKey 正例对照：含点键必须写成 [键] ⇒ 通过。
func TestPathExistsAcceptsDottedLiteralKey(t *testing.T) {
	files := map[string][]byte{}
	for k, v := range loadReal(t).ProblemsRaw {
		files[k] = v
	}
	ptrs, _ := json.Marshal(map[string]any{"p": []string{
		"spec/params.json#params.[reporting.monthly_cutoff_day].critical_note",
	}})
	files["spec/test_ptrs.json"] = ptrs
	if probs := runPathExists(t, files, "spec/test_ptrs.json", "p[*]"); len(probs) != 0 {
		t.Fatalf("[字面键] 正例应 0 报错，实为 %v", probs)
	}
}

// TestPathExistsAcceptsSelectorAndHashless 正例：[k=v] 选择器命中 ＋ 省略 # 只验文件。
func TestPathExistsAcceptsSelectorAndHashless(t *testing.T) {
	files := map[string][]byte{}
	for k, v := range loadReal(t).ProblemsRaw {
		files[k] = v
	}
	ptrs, _ := json.Marshal(map[string]any{"p": []string{
		"spec/forms/GR.json#checks[id=no_approver_in_acceptance_group].origin_ref",
		"spec/enums.json", // 省略 # ⇒ 只校验文件存在且可解析
	}})
	files["spec/test_ptrs.json"] = ptrs
	if probs := runPathExists(t, files, "spec/test_ptrs.json", "p[*]"); len(probs) != 0 {
		t.Fatalf("选择器/无# 正例应 0 报错，实为 %v", probs)
	}
}

// TestPathExistsRealAnchors 全量正例：真 spec/institution-anchors.json 的
// clauses[*].spec[*].at 全部指针 0 报错（该文件当前 158 条，全部可解析）。
func TestPathExistsRealAnchors(t *testing.T) {
	files := map[string][]byte{}
	for k, v := range loadReal(t).ProblemsRaw {
		files[k] = v
	}
	if _, ok := files["spec/institution-anchors.json"]; !ok {
		t.Fatal("缺 spec/institution-anchors.json（N-006 交付）")
	}
	probs := runPathExists(t, files, "spec/institution-anchors.json", "clauses[*].spec[*].at")
	if len(probs) != 0 {
		t.Fatalf("真锚点全量应 0 报错，实为 %d:\n%s", len(probs), strings.Join(probs, "\n"))
	}
}

// TestPathExistsMinHits min_hits 默认 1：collect 命中不足 ⇒ 报「声明可能写错」。
func TestPathExistsMinHits(t *testing.T) {
	files := peFiles([]string{}) // 空数组 ⇒ 0 hits
	probs := runPathExists(t, files, "spec/test_ptrs.json", "p[*]")
	if len(probs) != 1 || !strings.Contains(probs[0], "min_hits") {
		t.Fatalf("0 命中应报 min_hits 守卫，实为 %v", probs)
	}
}
