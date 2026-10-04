package specload

// N-053 · 第 12 原语 csv_col_eq_json_by_key 单元测试（九条 C1–C9 ＋ 逐条红/绿断言）。
// 装配照 min_hits_per_file_test#mhSpec（合成 checks.json ＋ 内存 files 含 CSV/JSON 字节）。

import (
	"encoding/json"
	"strings"
	"testing"
)

// csvSpec 合成 spec/checks.json（声明原语＋单判据 id=T-CSV）＋ 透传 files。
func csvSpec(t *testing.T, args map[string]any, files map[string][]byte) map[string][]byte {
	t.Helper()
	doc := map[string]any{
		"version":    "test",
		"primitives": map[string]any{"csv_col_eq_json_by_key": map[string]any{"desc": "N-053 合成声明"}},
		"checks": []any{map[string]any{
			"id":        "T-CSV",
			"severity":  "must-green",
			"primitive": "csv_col_eq_json_by_key",
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

// csvProblems 跑清单、只筛 T-CSV。
func csvProblems(files map[string][]byte) []string {
	out := []string{}
	for _, p := range runChecklist(files) {
		if strings.Contains(p, "T-CSV") {
			out = append(out, p)
		}
	}
	return out
}

// csvDefaultArgs 任务包 §1 形态。
func csvDefaultArgs() map[string]any {
	return map[string]any{
		"file":         "spec/acceptance.csv",
		"key_cols":     []string{"doc_type", "check_id"},
		"json_glob":    "spec/forms/*.json",
		"json_collect": "checks[*]",
		"json_key":     []string{"$file_stem", "id"},
		"pairs": []any{
			map[string]any{"csv_col": "severity", "json_field": "severity"},
			map[string]any{"csv_col": "carrier_kind", "json_field": "carried_by_kind"},
		},
		"require_same_row_set": true,
		"csv_cols_expected":    11,
	}
}

const csvHeader = "doc_type,check_id,c3,c4,severity,c6,c7,c8,carrier_kind,c10,c11"

// csvBaseFiles 标准夹具：AA(c1 hard/code · c2 soft/manual) ＋ BB(b1 soft/code) ＋ 三行 CSV 全等。
func csvBaseFiles() map[string][]byte {
	return map[string][]byte{
		"spec/forms/AA.json": []byte(`{"checks":[
			{"id":"c1","severity":"hard","carried_by_kind":"code"},
			{"id":"c2","severity":"soft","carried_by_kind":"manual"}]}`),
		"spec/forms/BB.json": []byte(`{"checks":[
			{"id":"b1","severity":"soft","carried_by_kind":"code"}]}`),
		"spec/acceptance.csv": []byte(csvHeader + "\n" +
			"AA,c1,x,x,hard,x,x,x,code,x,x\n" +
			"AA,c2,x,x,soft,x,x,x,manual,x,x\n" +
			"BB,b1,x,x,soft,x,x,x,code,x,x\n"),
	}
}

func assertCSV(t *testing.T, probs []string, wantN int, name string, keywords ...string) {
	t.Helper()
	if len(probs) != wantN {
		t.Fatalf("%s：报错数 = %d，期望 %d（%v）", name, len(probs), wantN, probs)
	}
	if wantN == 0 {
		return
	}
	for _, kw := range keywords {
		if !strings.Contains(probs[0], kw) {
			t.Errorf("%s：报错应含 %q，实为：%s", name, kw, probs[0])
		}
	}
}

// C1 全等 ⇒ 零报错。
func TestCSVEqC1AllEqual(t *testing.T) {
	probs := csvProblems(csvSpec(t, csvDefaultArgs(), csvBaseFiles()))
	assertCSV(t, probs, 0, "C1")
}

// C2 值不等 ⇒ 恰 1 条，点名 AA/c1/severity 与两值。
func TestCSVEqC2ValueMismatch(t *testing.T) {
	files := csvBaseFiles()
	files["spec/acceptance.csv"] = []byte(csvHeader + "\n" +
		"AA,c1,x,x,soft,x,x,x,code,x,x\n" + // severity 改 soft（真源 hard）
		"AA,c2,x,x,soft,x,x,x,manual,x,x\n" +
		"BB,b1,x,x,soft,x,x,x,code,x,x\n")
	probs := csvProblems(csvSpec(t, csvDefaultArgs(), files))
	assertCSV(t, probs, 1, "C2", "AA", "c1", "severity", "soft", "hard")
}

// C3 map 生效 ⇒ 零报错（CSV carrier_kind=code、真源 carried_by_kind=submit ＋ map）。
func TestCSVEqC3MapApplied(t *testing.T) {
	files := csvBaseFiles()
	files["spec/forms/AA.json"] = []byte(`{"checks":[
		{"id":"c1","severity":"hard","carried_by_kind":"code"},
		{"id":"c2","severity":"soft","carried_by_kind":"submit"}]}`)
	// CSV c2 行 carrier 写 code（真源 submit ⇒ 依赖 map{submit:code} 归一）
	files["spec/acceptance.csv"] = []byte(csvHeader + "\n" +
		"AA,c1,x,x,hard,x,x,x,code,x,x\n" +
		"AA,c2,x,x,soft,x,x,x,code,x,x\n" +
		"BB,b1,x,x,soft,x,x,x,code,x,x\n")
	args := csvDefaultArgs()
	args["pairs"] = []any{
		map[string]any{"csv_col": "severity", "json_field": "severity"},
		map[string]any{"csv_col": "carrier_kind", "json_field": "carried_by_kind",
			"map": map[string]any{"submit": "code"}},
	}
	probs := csvProblems(csvSpec(t, args, files))
	assertCSV(t, probs, 0, "C3（map 未生效则必红）")
}

// C4 漏登记 ⇒ 恰 1 条「未登记」。
func TestCSVEqC4MissingRow(t *testing.T) {
	files := csvBaseFiles()
	files["spec/acceptance.csv"] = []byte(csvHeader + "\n" +
		"AA,c1,x,x,hard,x,x,x,code,x,x\n" + // 删掉 AA,c2
		"BB,b1,x,x,soft,x,x,x,code,x,x\n")
	probs := csvProblems(csvSpec(t, csvDefaultArgs(), files))
	assertCSV(t, probs, 1, "C4", "未登记", "c2")
}

// C5 CSV 多一行（真源无此键）⇒ 恰 1 条「键不在真源」。
func TestCSVEqC5ExtraRow(t *testing.T) {
	files := csvBaseFiles()
	files["spec/acceptance.csv"] = []byte(csvHeader + "\n" +
		"AA,c1,x,x,hard,x,x,x,code,x,x\n" +
		"AA,c2,x,x,soft,x,x,x,manual,x,x\n" +
		"BB,b1,x,x,soft,x,x,x,code,x,x\n" +
		"CC,c9,x,x,hard,x,x,x,code,x,x\n")
	probs := csvProblems(csvSpec(t, csvDefaultArgs(), files))
	assertCSV(t, probs, 1, "C5", "键不在真源", "CC", "c9")
}

// C6 真源键重复（同文件同 id 两条）⇒ 报错（不静默取后者）。
func TestCSVEqC6DuplicateSourceKey(t *testing.T) {
	files := csvBaseFiles()
	files["spec/forms/AA.json"] = []byte(`{"checks":[
		{"id":"c1","severity":"hard","carried_by_kind":"code"},
		{"id":"c1","severity":"soft","carried_by_kind":"manual"},
		{"id":"c2","severity":"soft","carried_by_kind":"manual"}]}`)
	probs := csvProblems(csvSpec(t, csvDefaultArgs(), files))
	if len(probs) == 0 {
		t.Fatal("C6：真源键重复应报错（静默取后者＝假绿）")
	}
	found := false
	for _, p := range probs {
		if strings.Contains(p, "键重复") {
			found = true
		}
	}
	if !found {
		t.Errorf("C6 报错应点名「键重复」：%v", probs)
	}
}

// C7 fail-closed：json_glob 命中 0 文件 ⇒ 报错。
func TestCSVEqC7GlobZero(t *testing.T) {
	args := csvDefaultArgs()
	args["json_glob"] = "spec/nope/*.json"
	probs := csvProblems(csvSpec(t, args, csvBaseFiles()))
	assertCSV(t, probs, 1, "C7", "命中 0 个文件")
}

// C8 fail-closed：收集面 0 项 ⇒ 报错。
func TestCSVEqC8CollectZero(t *testing.T) {
	files := csvBaseFiles()
	files["spec/forms/AA.json"] = []byte(`{"checks":[]}`)
	files["spec/forms/BB.json"] = []byte(`{"checks":[]}`)
	probs := csvProblems(csvSpec(t, csvDefaultArgs(), files))
	if len(probs) == 0 {
		t.Fatal("C8：json_collect 命中 0 项应报错")
	}
	found := false
	for _, p := range probs {
		if strings.Contains(p, "命中 0 项") {
			found = true
		}
	}
	if !found {
		t.Errorf("C8 应点名「命中 0 项」：%v", probs)
	}
}

// C9 列数不符 ⇒ 报错。
func TestCSVEqC9ColumnCount(t *testing.T) {
	files := csvBaseFiles()
	files["spec/acceptance.csv"] = []byte(csvHeader + "\n" +
		"AA,c1,x,x,hard,x,x,x,code,x,x\n" +
		"AA,c2,x,x,soft,x,x,x,manual\n" + // 9 列 ≠ 11
		"BB,b1,x,x,soft,x,x,x,code,x,x\n")
	probs := csvProblems(csvSpec(t, csvDefaultArgs(), files))
	assertCSV(t, probs, 1, "C9", "列数")
}
