package specload

// N-053 落地段③ · T2：Load 装载面扩到 .csv 的回归钉（L1–L4）。
// ★ 目的：证明 CSV 真的进了 files map、且没有把 .md 等误装进来。

import (
	"encoding/csv"
	"io/fs"
	"strings"
	"testing"

	specfs "github.com/chadhao/jx-procurement-platform"
)

// L1 CSV 已进入装载面：键存在、非空、csv 解析 ≥2 行、表头含 doc_type/check_id。
func TestLoaderCSVScopeL1CSVInScope(t *testing.T) {
	b, err := Load(specfs.FS)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	raw, ok := b.ProblemsRaw["spec/acceptance.csv"]
	if !ok {
		t.Fatal("L1：ProblemsRaw 缺 spec/acceptance.csv —— 装载面未含 CSV")
	}
	if len(raw) == 0 {
		t.Fatal("L1：spec/acceptance.csv 字节长度为 0")
	}
	rows, err := csv.NewReader(strings.NewReader(string(raw))).ReadAll()
	if err != nil {
		t.Fatalf("L1：CSV 解析失败：%v", err)
	}
	if len(rows) < 2 {
		t.Fatalf("L1：CSV 行数 = %d，期望 ≥2（表头＋≥1 数据行）", len(rows))
	}
	header := strings.Join(rows[0], ",")
	if !strings.Contains(header, "doc_type") || !strings.Contains(header, "check_id") {
		t.Errorf("L1：表头应含 doc_type 与 check_id，实为：%s", header)
	}
	t.Logf("L1：acceptance.csv %d 行，表头 %d 列：%s", len(rows), len(rows[0]), header)
}

// L2 不误伤：全部键以 .json/.csv 结尾（.md 等不在）——并按实际 json 键数与
// fs.WalkDir 实数对照（防「只断言后半句」）。
func TestLoaderCSVScopeL2NonCSVJSONExcluded(t *testing.T) {
	b, err := Load(specfs.FS)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	jsonN, csvN := 0, 0
	for k := range b.ProblemsRaw {
		switch {
		case strings.HasSuffix(k, ".json"):
			jsonN++
		case strings.HasSuffix(k, ".csv"):
			csvN++
		default:
			t.Errorf("L2：装载面出现非 json/csv 键 %q（.md 等不应装载）", k)
		}
		if k == "spec/README.md" || k == "spec/RESOLUTIONS.md" {
			t.Errorf("L2：markdown 文件不得装载：%s", k)
		}
	}
	// ★ 不在此断言 csvN==1（那是 L1 的职责）—— 保持 L2 主题单一（后缀白名单＋
	//   json 实数对照），使 M1（过滤回退）恰红 L1、L2 保持绿（任务包隔离预期）。
	// json 键数与磁盘实数一致（不写死数字）
	diskJSON := 0
	_ = fs.WalkDir(specfs.FS, ".", func(path string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(path, ".json") {
			diskJSON++
		}
		return nil
	})
	if jsonN != diskJSON {
		t.Errorf("L2：json 键数 = %d，fs 实数 = %d（不一致即漏装/多装）", jsonN, diskJSON)
	}
	t.Logf("L2：json=%d csv=%d（磁盘 json=%d）", jsonN, csvN, diskJSON)
}

// L3 既有结论不变：Load 成功且 json 键数与 spec/ 下 *.json 实数一致（独立复核 L2 的对照）。
func TestLoaderCSVScopeL3JSONCountUnchanged(t *testing.T) {
	b, err := Load(specfs.FS)
	if err != nil {
		t.Fatalf("L3：真 spec Load 应成功（既有结论不变），实为：%v", err)
	}
	diskJSON := 0
	_ = fs.WalkDir(specfs.FS, ".", func(path string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(path, ".json") {
			diskJSON++
		}
		return nil
	})
	got := 0
	for k := range b.ProblemsRaw {
		if strings.HasSuffix(k, ".json") {
			got++
		}
	}
	if got != diskJSON {
		t.Fatalf("L3：ProblemsRaw json 键 = %d，磁盘实数 = %d", got, diskJSON)
	}
	if got == 0 {
		t.Fatal("L3：json 键为 0 —— 装载面异常")
	}
}

// L4 负向（当前语义）：去掉 acceptance.csv 的同源 files ⇒ 因 checks.json 尚未引用
// S26 ⇒ err == nil。
// ★ 注释备案：我方落地 S26 后此例语义会自然翻转为「缺 CSV ⇒ 报错」，
// 届时由我方同批更新本用例（任务包 §1 T2 L4 明示 —— 本批不写会红的用例）。
func TestLoaderCSVScopeL4MissingCSVStillOK(t *testing.T) {
	base, err := Load(specfs.FS)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	files := map[string][]byte{}
	for k, v := range base.ProblemsRaw {
		if k == "spec/acceptance.csv" {
			continue // 刻意去掉 CSV
		}
		files[k] = v
	}
	if _, ok := files["spec/acceptance.csv"]; ok {
		t.Fatal("L4 构造失败：CSV 仍在 files 内")
	}
	if _, err := loadFiles(files); err != nil {
		t.Fatalf("L4：去 CSV 后当前应 err==nil（checks.json 未引用 S26）—— 实为：%v"+
			"（若我方已落 S26，此例应翻转为报错，请同批更新本用例）", err)
	}
}
