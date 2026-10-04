package specload

// N-055 · min_hits 逐文件语义回归钉（两侧同结论是本批核心证据）。
// ★ 三组语义（任务包 §2 T2）：
//   (a) 两文件一命中一零命中、min_hits:1 ⇒ 必须报（★ 改前 Go 汇总=1 ⇒ 不报＝鉴别力）；
//   (b) 两文件各命中 1、min_hits:2 ⇒ 必须报（★ 改前汇总=2 ⇒ 不报）；
//   (c) 两文件各命中 1、min_hits:1 ⇒ 必须过（防改完恒红）。
// ★ coverage 无 min_hits 判定面（checklist.go case coverage 无 collect/min_hits 参数），
//   单列一行证明「注入也不生效」——如实登记，不强造。

import (
	"encoding/json"
	"strings"
	"testing"
)

// mhSpec 构造合成 checks.json（单判据）——primitives 声明所用原语（[META] 要求）。
func mhSpec(t *testing.T, primitive string, args map[string]any, files map[string][]byte) map[string][]byte {
	t.Helper()
	doc := map[string]any{
		"version":    "test",
		"primitives": map[string]any{primitive: map[string]any{"desc": "min_hits 逐文件回归钉（N-055 合成）"}},
		"checks": []any{map[string]any{
			"id":        "T-MH",
			"severity":  "must-green",
			"primitive": primitive,
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

// mhProblems 跑清单、返回全部问题（只含 T-MH 的）。
func mhProblems(files map[string][]byte) []string {
	out := []string{}
	for _, p := range runChecklist(files) {
		if strings.Contains(p, "T-MH") {
			out = append(out, p)
		}
	}
	return out
}

type mhCase struct {
	primitive string
	args      func(minHits int) map[string]any
	// 文件 a：命中 1；文件 b：命中 0（(b)(c) 组由 argmin 控制，文件形态固定）
	files map[string][]byte
}

// mhFixtures 六个可测原语的 (a) 夹具（spec/testA.json 命中 1、spec/testB.json 命中 0）。
func mhFixtures() []mhCase {
	return []mhCase{
		{"enum_subset", func(m int) map[string]any {
			return map[string]any{"file": "spec/test*.json", "collect": "kinds[*]", "allowed": []string{"x"}, "min_hits": m}
		}, map[string][]byte{
			"spec/testA.json": []byte(`{"kinds":["x"]}`),
			"spec/testB.json": []byte(`{"other":1}`),
		}},
		{"ref_exists", func(m int) map[string]any {
			return map[string]any{"file": "spec/test*.json", "collect": "refs[*]",
				"target_file": "spec/target.json", "target_dict": "dict", "min_hits": m}
		}, map[string][]byte{
			"spec/testA.json":  []byte(`{"refs":["k1"]}`),
			"spec/testB.json":  []byte(`{"other":1}`),
			"spec/target.json": []byte(`{"dict":{"k1":1}}`),
		}},
		{"path_exists", func(m int) map[string]any {
			return map[string]any{"file": "spec/test*.json", "collect": "ptrs[*]", "min_hits": m}
		}, map[string][]byte{
			"spec/testA.json":  []byte(`{"ptrs":["spec/target.json"]}`),
			"spec/testB.json":  []byte(`{"other":1}`),
			"spec/target.json": []byte(`{"ok":1}`),
		}},
		{"range_contiguous", func(m int) map[string]any {
			return map[string]any{"file": "spec/test*.json", "collect": "ranges",
				"lower_key": "lower", "upper_key": "upper", "min_hits": m}
		}, map[string][]byte{
			"spec/testA.json": []byte(`{"ranges":[{"lower":null,"upper":null}]}`),
			"spec/testB.json": []byte(`{"other":1}`),
		}},
		{"pattern_absent", func(m int) map[string]any {
			return map[string]any{"file": "spec/test*.json", "collect": "items[*]",
				"forbidden_regex": "bad", "min_hits": m}
		}, map[string][]byte{
			"spec/testA.json": []byte(`{"items":["good"]}`),
			"spec/testB.json": []byte(`{"other":1}`),
		}},
		{"set_covers", func(m int) map[string]any {
			return map[string]any{"file": "spec/test*.json", "collect": "vals[*]",
				"required": []string{"r1"}, "min_hits": m}
		}, map[string][]byte{
			"spec/testA.json": []byte(`{"vals":["r1"]}`),
			"spec/testB.json": []byte(`{"other":1}`),
		}},
	}
}

// TestMinHitsPerFileGroupA (a)：一命中一零命中、min_hits:1 ⇒ 六原语必须各报。
// ★ 改前（跨文件汇总=1）⇒ 全不报 ⇒ 本用例整组红 —— 这就是鉴别力取证点。
func TestMinHitsPerFileGroupA(t *testing.T) {
	for _, c := range mhFixtures() {
		t.Run(c.primitive, func(t *testing.T) {
			files := mhSpec(t, c.primitive, c.args(1), c.files)
			probs := mhProblems(files)
			if len(probs) == 0 {
				t.Fatalf("(a) %s：一文件命中 0、min_hits=1 应逐文件报 —— 实为不报（跨文件汇总假绿）", c.primitive)
			}
			found := false
			for _, p := range probs {
				if strings.Contains(p, "min_hits") {
					found = true
				}
			}
			if !found {
				t.Errorf("(a) %s 报错须保留 min_hits 字样：%v", c.primitive, probs)
			}
		})
	}
}

// TestMinHitsPerFileGroupB (b)：两文件各命中 1、min_hits:2 ⇒ 必须报。
// ★ 改前汇总=2 ⇒ 不报。判据选 set_covers（★ 不选 enum_subset —— M1 变异恰打
// enum_subset，若 (b) 也用它则 M1 会连带打红 (b)、破坏任务包「M1 下 (b) 绿」的
// 隔离预期；set_covers 在 M1/M2 下不受影响、M3 下必红）。
func TestMinHitsPerFileGroupB(t *testing.T) {
	files := map[string][]byte{
		"spec/testA.json": []byte(`{"vals":["r1"]}`), // 命中 1
		"spec/testB.json": []byte(`{"vals":["r1"]}`), // 命中 1 —— 本组唯一红因=min_hits 数值
	}
	mh := mhFixtures()[5] // set_covers
	probs := mhProblems(mhSpec(t, mh.primitive, mh.args(2), files))
	hit := false
	for _, p := range probs {
		if strings.Contains(p, "min_hits") && strings.Contains(p, "命中 1") {
			hit = true
		}
	}
	if !hit {
		t.Fatalf("(b) 每文件 1 < min_hits=2 应逐文件报（改前汇总=2 不报）：%v", probs)
	}
}

// TestMinHitsPerFileGroupC (c)：两文件各命中 1、min_hits:1 ⇒ 必须过（防恒红）。
func TestMinHitsPerFileGroupC(t *testing.T) {
	for _, c := range mhFixtures() {
		t.Run(c.primitive, func(t *testing.T) {
			files := map[string][]byte{}
			for k, v := range c.files {
				if k == "spec/testB.json" {
					// 命中 1 的形态：按各原语的 A 文件克隆一份（不同内容但命中≥1 且值合法）
					files[k] = cloneHitOne(c.primitive, c.files)
					continue
				}
				files[k] = v
			}
			probs := mhProblems(mhSpec(t, c.primitive, c.args(1), files))
			for _, p := range probs {
				if strings.Contains(p, "min_hits") {
					t.Errorf("(c) %s 两文件各命中 1、min_hits=1 不应报 min_hits：%s", c.primitive, p)
				}
			}
		})
	}
}

// cloneHitOne 按原语给出「命中 1 且值合法」的 B 文件内容。
func cloneHitOne(primitive string, files map[string][]byte) []byte {
	a := files["spec/testA.json"]
	switch primitive {
	case "enum_subset":
		return []byte(`{"kinds":["x"]}`)
	case "ref_exists":
		return []byte(`{"refs":["k1"]}`)
	case "path_exists":
		return []byte(`{"ptrs":["spec/target.json"]}`)
	case "range_contiguous":
		return []byte(`{"ranges":[{"lower":null,"upper":null}]}`)
	case "pattern_absent":
		return []byte(`{"items":["good"]}`)
	case "set_covers":
		return []byte(`{"vals":["r1"]}`)
	default:
		return a
	}
}

// TestMinHitsCoverageNoFace coverage 无 min_hits/collect 判定面（checklist.go case
// coverage 只消费 file/dict_path/required/allow_extra）⇒ 注入 min_hits 参数也不生效、
// (a) 形态恒不报 —— 如实登记（任务包 §1.2 列了 coverage，实际无面可改）。
func TestMinHitsCoverageNoFace(t *testing.T) {
	args := map[string]any{"file": "spec/test*.json", "dict_path": "", "required": []string{"k1"}, "min_hits": 1}
	files := map[string][]byte{
		"spec/testA.json": []byte(`{"k1":1}`),
		"spec/testB.json": []byte(`{"other":1}`), // 无 k1 ⇒ required 缺会报（与 min_hits 无关）
	}
	probs := mhProblems(mhSpec(t, "coverage", args, files))
	for _, p := range probs {
		if strings.Contains(p, "min_hits") {
			t.Errorf("coverage 不应产 min_hits 报错（无该判定面）：%s", p)
		}
	}
	// required 缺键照报（coverage 自身语义未动）
	found := false
	for _, p := range probs {
		if strings.Contains(p, "缺") {
			found = true
		}
	}
	if !found {
		t.Errorf("coverage 自身 required 缺键语义应保持：%v", probs)
	}
}
