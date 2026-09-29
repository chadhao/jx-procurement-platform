package specload

// M1 验收：真实 spec 加载 + S1–S12 负向探针。
//
// ★ N-008 修正②：测试输入 = 与生产同一份 embed 字节（specfs.FS），不复制快照副本；
//   负向探针在内存中变异字节后复用 loadFiles，不落盘第二份 spec。

import (
	"encoding/json"
	"strings"
	"testing"

	specfs "github.com/chadhao/jx-procurement-platform"
)

func loadReal(t *testing.T) *Bundle {
	t.Helper()
	b, err := Load(specfs.FS)
	if err != nil {
		t.Fatalf("真实 spec 加载失败: %v", err)
	}
	return b
}

func TestLoadRealSpec(t *testing.T) {
	b := loadReal(t)

	if b.Chain.Version == "" {
		t.Fatal("chain version 为空")
	}
	if got := len(b.Chain.Routes); got != 9 {
		t.Errorf("routes 应 9 条，实为 %d", got)
	}
	n := 0
	for k := range b.Chain.DocChains {
		if !strings.HasPrefix(k, "_") {
			n++
		}
	}
	if n != 11 {
		t.Errorf("doc_chains 应 11 类，实为 %d", n)
	}
	for _, dt := range []string{"BA", "PR", "SA"} {
		if _, ok := b.Forms[dt]; !ok {
			t.Errorf("缺 forms/%s", dt)
		}
	}

	// 分档边界锚定（chain.json conventions.boundary_semantics.verified 的四条）
	wantBands := []struct {
		id     string
		lo, hi *int64
	}{
		{"purchase_tier1", nil, int64p(99999)},
		{"purchase_tier2", int64p(100000), int64p(500000)},
		{"purchase_tier3", int64p(500001), nil},
	}
	bands := b.Chain.Thresholds.Purchase.Bands
	if len(bands) != 3 {
		t.Fatalf("bands 应 3 档，实为 %d", len(bands))
	}
	for i, w := range wantBands {
		got := bands[i]
		if got.ID != w.id {
			t.Errorf("bands[%d].id = %s，应为 %s", i, got.ID, w.id)
		}
		if !int64Eq(got.LowerInclusive, w.lo) {
			t.Errorf("bands[%d].lower = %v，应为 %v", i, got.LowerInclusive, w.lo)
		}
		if !int64Eq(got.UpperInclusive, w.hi) {
			t.Errorf("bands[%d].upper = %v，应为 %v", i, got.UpperInclusive, w.hi)
		}
	}

	// spec_version 聚合（N-008 修正③）
	wantSV := "chain=1.0;enums=1.1;ledger=1.0;forms=BA:1.0,PR:1.0,SA:1.0"
	if b.SpecVersion != wantSV {
		t.Errorf("SpecVersion = %q，应为 %q", b.SpecVersion, wantSV)
	}
}

// ---------------------------------------------------------------------------
// 负向探针：内存变异 → loadFiles → 断言命中对应 [Sx]
// ---------------------------------------------------------------------------

type probe struct {
	name   string
	expect string // 错误文案中必须出现的判据前缀，如 "[S5]"
	mutate func(t *testing.T, files map[string][]byte)
}

func runProbes(t *testing.T, probes []probe) {
	t.Helper()
	base := loadReal(t).ProblemsRaw
	for _, p := range probes {
		t.Run(p.name, func(t *testing.T) {
			files := cloneFiles(base)
			p.mutate(t, files)
			_, err := loadFiles(files)
			if err == nil {
				t.Fatalf("期望违规 %s，但加载通过", p.expect)
			}
			if !strings.Contains(err.Error(), p.expect) {
				t.Errorf("错误未包含 %s，实际: %v", p.expect, err)
			}
		})
	}
}

func mutateJSON(t *testing.T, files map[string][]byte, key string, fn func(m map[string]any)) {
	t.Helper()
	raw, ok := files[key]
	if !ok {
		t.Fatalf("缺文件 %s", key)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("%s 解析失败: %v", key, err)
	}
	fn(m)
	out, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("回写 %s 失败: %v", key, err)
	}
	files[key] = out
}

func TestValidateProbes(t *testing.T) {
	runProbes(t, []probe{
		{"S1_语法错", "[S1]", func(t *testing.T, f map[string][]byte) {
			f["spec/chain.json"] = []byte("{broken json")
		}},
		{"S2_缺顶层键", "[S2]", func(t *testing.T, f map[string][]byte) {
			mutateJSON(t, f, "spec/chain.json", func(m map[string]any) {
				delete(m, "roles")
			})
		}},
		{"S3_缺流程线", "[S3]", func(t *testing.T, f map[string][]byte) {
			mutateJSON(t, f, "spec/chain.json", func(m map[string]any) {
				delete(asMap(m["routes"]), "emergency")
			})
		}},
		{"S4_缺单据类型", "[S4]", func(t *testing.T, f map[string][]byte) {
			mutateJSON(t, f, "spec/chain.json", func(m map[string]any) {
				delete(asMap(m["doc_chains"]), "PC")
			})
		}},
		{"S5_禁落账目标", "[S5]", func(t *testing.T, f map[string][]byte) {
			mutateJSON(t, f, "spec/chain.json", func(m map[string]any) {
				asMap(asMap(m["doc_chains"])["RFQ"])["ledger"] = []any{"L10"}
			})
		}},
		{"S6_引用悬空", "[S6]", func(t *testing.T, f map[string][]byte) {
			mutateJSON(t, f, "spec/chain.json", func(m map[string]any) {
				asMap(asMap(m["doc_chains"])["SS"])["route"] = "no_such_route"
			})
		}},
		{"S7_档位不连续", "[S7]", func(t *testing.T, f map[string][]byte) {
			mutateJSON(t, f, "spec/chain.json", func(m map[string]any) {
				bands := asMap(asMap(m["thresholds"])["purchase"])["bands"].([]any)
				asMap(bands[1])["lower_inclusive"] = 99999.0 // 与首档重叠
			})
		}},
		{"S8_字段名不规范", "[S8]", func(t *testing.T, f map[string][]byte) {
			mutateJSON(t, f, "spec/forms/BA.json", func(m map[string]any) {
				secs := m["sections"].([]any)
				sec0 := asMap(secs[0])
				fields := sec0["fields"].([]any)
				asMap(fields[0])["name"] = "bad name<br>"
			})
		}},
		{"S9_台账未覆盖", "[S9]", func(t *testing.T, f map[string][]byte) {
			mutateJSON(t, f, "spec/ledger-mapping.json", func(m map[string]any) {
				delete(m, "ledgers")
			})
		}},
		{"S10_指向未定义台账", "[S10]", func(t *testing.T, f map[string][]byte) {
			mutateJSON(t, f, "spec/ledger-mapping.json", func(m map[string]any) {
				asMap(asMap(m["doc_to_ledger"])["PR"])["ledger"] = []any{"L99"}
			})
		}},
		{"S11_落账目标被禁", "[S11]", func(t *testing.T, f map[string][]byte) {
			mutateJSON(t, f, "spec/ledger-mapping.json", func(m map[string]any) {
				asMap(asMap(m["doc_to_ledger"])["PR"])["ledger"] = []any{"L10"}
			})
		}},
		{"S12_forms与映射不一致", "[S12]", func(t *testing.T, f map[string][]byte) {
			mutateJSON(t, f, "spec/forms/PR.json", func(m map[string]any) {
				m["ledger"] = []any{"L02"} // 缺 L03（R-02 守卫：PR 必须同时落 L02+L03）
			})
		}},
	})
}

// ---------------------------------------------------------------------------

func cloneFiles(in map[string][]byte) map[string][]byte {
	out := make(map[string][]byte, len(in))
	for k, v := range in {
		cp := make([]byte, len(v))
		copy(cp, v)
		out[k] = cp
	}
	return out
}

func int64p(v int64) *int64 { return &v }

func int64Eq(a, b *int64) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}
