package specload

// N-049 ② 第二半 · ref_exists 派生参数 target_filter 单元测试（R1–R9）。
// 全内存 files 夹具、每用例独立 t.Run；装配照 min_hits_per_file_test 范式。

import (
	"encoding/json"
	"strings"
	"testing"
)

// rfSpec 合成 spec/checks.json（声明 ref_exists ＋ 单判据 id=T-RF）。
func rfSpec(t *testing.T, args map[string]any, files map[string][]byte) map[string][]byte {
	t.Helper()
	doc := map[string]any{
		"version":    "test",
		"primitives": map[string]any{"ref_exists": map[string]any{"desc": "N-049② 合成声明"}},
		"checks": []any{map[string]any{
			"id":        "T-RF",
			"severity":  "must-green",
			"primitive": "ref_exists",
			"args":      args,
		}},
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string][]byte{"spec/checks.json": raw}
	for k, v := range files {
		out[k] = v
	}
	return out
}

func rfProblems(files map[string][]byte) []string {
	out := []string{}
	for _, p := range runChecklist(files) {
		if strings.Contains(p, "T-RF") {
			out = append(out, p)
		}
	}
	return out
}

// rfTarget 目标表（默认）：approver/action/none 三类 —— ★ 不含 r_missing，
// 防止 fail-closed② 在非 R4 用例上连带（每用例只测自己的机制）。
var rfTarget = []byte(`{"roles":{
	"r_approver":{"node_actor_kind":"approver"},
	"r_action":{"node_actor_kind":"action"},
	"r_none":{"node_actor_kind":"none"}}}`)

// rfTargetMissing 含缺字段键（仅 R4 使用）。
var rfTargetMissing = []byte(`{"roles":{
	"r_approver":{"node_actor_kind":"approver"},
	"r_missing":{"other":1}}}`)

// rfArgs 基础 args（不含 target_filter）。
func rfArgs(refs string) map[string]any {
	return map[string]any{
		"file":        "spec/src.json",
		"collect":     "refs[*]",
		"target_file": "spec/tgt.json",
		"target_dict": "roles",
		"min_hits":    1,
	}
}

func rfFiles(srcJSON string) map[string][]byte {
	return map[string][]byte{
		"spec/src.json": []byte(srcJSON),
		"spec/tgt.json": rfTarget,
	}
}

func rfAssert(t *testing.T, probs []string, wantN int, name string, keywords ...string) {
	t.Helper()
	if len(probs) != wantN {
		t.Fatalf("%s：报错数 = %d，期望 %d（%v）", name, len(probs), wantN, probs)
	}
	for _, kw := range keywords {
		if len(probs) == 0 {
			t.Fatalf("%s：期望报错含 %q 却 0 条", name, kw)
		}
		if !strings.Contains(probs[0], kw) {
			t.Errorf("%s：报错应含 %q，实为：%s", name, kw, probs[0])
		}
	}
}

// R1 缺省（无 target_filter）⇒ 不过滤：引用 r_none（node_actor_kind=none）也放行。
func TestRefFilterR1DefaultNoFilter(t *testing.T) {
	args := rfArgs("")
	probs := rfProblems(rfSpec(t, args, rfFiles(`{"refs":["r_none"]}`)))
	rfAssert(t, probs, 0, "R1（缺省＝不过滤；滤掉了即红）")
}

// R2 field 命中 + 值 ∈ in ⇒ 无报错。
func TestRefFilterR2AllowedPasses(t *testing.T) {
	args := rfArgs("")
	args["target_filter"] = map[string]any{"field": "node_actor_kind", "in": []string{"approver", "action"}}
	probs := rfProblems(rfSpec(t, args, rfFiles(`{"refs":["r_approver"]}`)))
	rfAssert(t, probs, 0, "R2")
}

// R3 引用「值不在 in」的键 ⇒ 报错（本参数的存在理由：旧白名单会放行）。
func TestRefFilterR3ValueNotInFilterRejected(t *testing.T) {
	args := rfArgs("")
	args["target_filter"] = map[string]any{"field": "node_actor_kind", "in": []string{"approver", "action"}}
	probs := rfProblems(rfSpec(t, args, rfFiles(`{"refs":["r_none"]}`)))
	rfAssert(t, probs, 1, "R3", "r_none", "不存在于目标键集合")
}

// R4 条目缺 field ⇒ 报错并点名该键。
func TestRefFilterR4MissingFieldNamed(t *testing.T) {
	args := rfArgs("")
	args["target_filter"] = map[string]any{"field": "node_actor_kind", "in": []string{"approver", "action"}}
	files := rfFiles(`{"refs":["r_approver"]}`)
	files["spec/tgt.json"] = rfTargetMissing
	probs := rfProblems(rfSpec(t, args, files))
	rfAssert(t, probs, 1, "R4", "r_missing", "缺字段")
}

// R5 field 的点路径（meta.kind）⇒ dig 语义取值正确（命中放行、不命中排除）。
func TestRefFilterR5DottedPath(t *testing.T) {
	tgt := map[string][]byte{
		"spec/src.json": []byte(`{"refs":["d_in"]}`),
		"spec/tgt.json": []byte(`{"roles":{
			"d_in":{"meta":{"kind":"approver"}},
			"d_out":{"meta":{"kind":"none"}}}}`),
	}
	args := rfArgs("")
	args["target_filter"] = map[string]any{"field": "meta.kind", "in": []string{"approver"}}
	probs := rfProblems(rfSpec(t, args, tgt))
	rfAssert(t, probs, 0, "R5 点路径命中")

	// 反向：引用被排除的 d_out ⇒ 报
	tgt["spec/src.json"] = []byte(`{"refs":["d_out"]}`)
	probs = rfProblems(rfSpec(t, args, tgt))
	rfAssert(t, probs, 1, "R5 点路径排除", "d_out")
}

// R6 形态非法三子例（缺 in / field 空串 / in 非数组）⇒ 各报形态非法。
func TestRefFilterR6Malformed(t *testing.T) {
	cases := []struct {
		name   string
		filter map[string]any
		kw     string
	}{
		{"缺 in", map[string]any{"field": "node_actor_kind"}, "形态非法"},
		{"field 空串", map[string]any{"field": "", "in": []string{"approver"}}, "形态非法"},
		{"in 非数组", map[string]any{"field": "node_actor_kind", "in": "not-array"}, "形态非法"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			args := rfArgs("")
			args["target_filter"] = c.filter
			probs := rfProblems(rfSpec(t, args, rfFiles(`{"refs":["r_approver"]}`)))
			rfAssert(t, probs, 1, "R6/"+c.name, c.kw)
		})
	}
}

// R7 过滤后白名单为空（target 全被滤掉、无 extra、in 为空数组也走本机制）⇒ 报错。
func TestRefFilterR7EmptyWhitelist(t *testing.T) {
	args := rfArgs("")
	args["target_filter"] = map[string]any{"field": "node_actor_kind", "in": []string{}}
	probs := rfProblems(rfSpec(t, args, rfFiles(`{"refs":["r_approver"]}`)))
	// 恰 2 条：(a) fail-closed③ 白名单为空 ＋ (b) 引用 miss（两机制独立可见、不互相掩盖）
	if len(probs) != 2 {
		t.Fatalf("R7：报错数 = %d，期望 2（白名单空 + 引用 miss）：%v", len(probs), probs)
	}
	foundEmpty, foundMiss := false, false
	for _, p := range probs {
		if strings.Contains(p, "白名单为空") {
			foundEmpty = true
		}
		if strings.Contains(p, "不存在于目标键集合") {
			foundMiss = true
		}
	}
	if !foundEmpty || !foundMiss {
		t.Errorf("R7：两条机制文案应各自可见（白名单空=%v 引用miss=%v）：%v", foundEmpty, foundMiss, probs)
	}
}

// R8 extra_allowed 与过滤并存 ⇒ extra 值仍被接受（并入顺序未变）。
func TestRefFilterR8ExtraStillAccepted(t *testing.T) {
	args := rfArgs("")
	args["target_filter"] = map[string]any{"field": "node_actor_kind", "in": []string{"approver"}}
	args["extra_allowed"] = []string{"extra_sys"}
	probs := rfProblems(rfSpec(t, args, rfFiles(`{"refs":["extra_sys"]}`)))
	rfAssert(t, probs, 0, "R8（extra 并入不受过滤影响）")
}

// R9 split ＋ target_filter 同时设置 ⇒ 互不影响：拆段后各自过（extra 段靠 extra、
// 目标段靠 filter）。
func TestRefFilterR9SplitAndFilterCoexist(t *testing.T) {
	args := rfArgs("")
	args["split"] = "|"
	args["target_filter"] = map[string]any{"field": "node_actor_kind", "in": []string{"approver"}}
	args["extra_allowed"] = []string{"extra_sys"}
	probs := rfProblems(rfSpec(t, args, rfFiles(`{"refs":["r_approver|extra_sys"]}`)))
	rfAssert(t, probs, 0, "R9 split+filter 互不影响")
	// 反向：拆出 none 段 ⇒ 报（split 照拆、filter 照滤 —— 两机制都在工作）
	args2 := rfArgs("")
	args2["split"] = "|"
	args2["target_filter"] = map[string]any{"field": "node_actor_kind", "in": []string{"approver"}}
	probs = rfProblems(rfSpec(t, args2, rfFiles(`{"refs":["r_approver|r_none"]}`)))
	rfAssert(t, probs, 1, "R9 反向", "r_none")
}
