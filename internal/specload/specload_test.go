package specload

// M1 验收：真实 spec 加载 + S1–S12 负向探针。
//
// ★ N-008 修正②：测试输入 = 与生产同一份 embed 字节（specfs.FS），不复制快照副本；
//   负向探针在内存中变异字节后复用 loadFiles，不落盘第二份 spec。

import (
	"encoding/json"
	"fmt"
	"sort"
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

	// spec_version 聚合（N-008 修正③）—— ★ **期望串由真源派生，不硬编码表单清单**
	//（N-023：硬编码 forms=BA:1.0,PR:1.0,SA:1.0 把「批 1 临时范围」写成系统不变量，
	//  批 2 每落一张表单撞一次；要守的是**格式与前缀**，不是"恰好几张表单"）。
	wantSV := fmt.Sprintf("chain=%s;enums=%s;ledger=%s;forms=%s",
		b.Chain.Version, b.Enums.Version, b.Ledger.Version, joinFormsVersion(b))
	if b.SpecVersion != wantSV {
		t.Errorf("SpecVersion = %q，应由真源派生为 %q", b.SpecVersion, wantSV)
	}
	// 契约不变量：批 1 三张必须在（**包含**语义，新表单到来不红）
	for _, dt := range []string{"BA:", "PR:", "SA:"} {
		if !strings.Contains(b.SpecVersion, dt) {
			t.Errorf("SpecVersion 缺批 1 表单 %s 段：%q", dt, b.SpecVersion)
		}
	}
}

// joinFormsVersion 按 doc_type 升序拼 "DT:version,…"（与 buildSpecVersion 同形，
// 但作为**测试侧独立实现**交叉验证，而非调用被测函数自证）。
func joinFormsVersion(b *Bundle) string {
	dts := make([]string, 0, len(b.Forms))
	for dt := range b.Forms {
		dts = append(dts, dt)
	}
	sort.Strings(dts)
	parts := make([]string, 0, len(dts))
	for _, dt := range dts {
		parts = append(parts, dt+":"+b.Forms[dt].Version)
	}
	return strings.Join(parts, ",")
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
		{"S5a_超出台账编号", "[S5a]", func(t *testing.T, f map[string][]byte) {
			// ★ 清单化后 S5a＝enum_subset(L01..L12)；L10 在集合内（禁落账目标由 S11 管 ledger-mapping
			//   侧 —— chain 侧禁目标的缺口已开议题 N-020），故用 L13（不存在的编号）触发。
			mutateJSON(t, f, "spec/chain.json", func(m map[string]any) {
				asMap(asMap(m["doc_chains"])["RFQ"])["ledger"] = []any{"L13"}
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
		// ---- 清单引擎自身的护栏（N-011 / checks.json V1.2）----
		{"META_未知原语", "[META]", func(t *testing.T, f map[string][]byte) {
			mutateJSON(t, f, "spec/checks.json", func(m map[string]any) {
				checks := m["checks"].([]any)
				asMap(checks[0])["primitive"] = "no_such_primitive"
			})
		}},
		{"MINHITS_collect声明写错", "min_hits", func(t *testing.T, f map[string][]byte) {
			// S7 的 collect 指到不存在的路径 ⇒ 命中 0 < min_hits 1 ⇒ 必须报错
			// （V1.2 教训：声明写错不许静默通过）。
			mutateJSON(t, f, "spec/checks.json", func(m map[string]any) {
				for _, c := range m["checks"].([]any) {
					cm := asMap(c)
					if cm["id"] == "S7" {
						asMap(cm["args"])["collect"] = "thresholds.purchase.bands_no_such"
					}
				}
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

// ---------------------------------------------------------------------------
// 引擎能力自测（N-021 split · N-022 set_covers）——
// 清单尚无 S6c/S13/S14（你先我后顺序：Go 先落、门禁仍绿）；本组用内存追加判据自测。
// ---------------------------------------------------------------------------

// appendCheck 向内存 checks.json 追加一条判据（不落盘）。
func appendCheck(t *testing.T, files map[string][]byte, c map[string]any) {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(files["spec/checks.json"], &doc); err != nil {
		t.Fatal(err)
	}
	doc["checks"] = append(doc["checks"].([]any), c)
	out, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	files["spec/checks.json"] = out
}

func TestRefExistsSplitEngine(t *testing.T) { // N-021
	base := loadReal(t).ProblemsRaw

	t.Run("管道串逐段校验_合法通过", func(t *testing.T) {
		files := cloneFiles(base)
		appendCheck(t, files, map[string]any{
			"id": "S6c-probe", "primitive": "ref_exists",
			"args": map[string]any{
				"file": "spec/chain.json", "collect": "**.route_by_condition",
				"target_file": "spec/chain.json", "target_dict": "routes",
				"extra_allowed": []string{"contract_two_level"},
				"split":         "|", "min_hits": 1,
			},
		})
		if _, err := loadFiles(files); err != nil {
			t.Fatalf("合法管道串不应报错: %v", err)
		}
	})

	t.Run("坏段被抓出", func(t *testing.T) {
		files := cloneFiles(base)
		appendCheck(t, files, map[string]any{
			"id": "S6c-bad", "primitive": "ref_exists",
			"args": map[string]any{
				"file": "spec/chain.json", "collect": "**.route_by_condition",
				"target_file": "spec/chain.json", "target_dict": "routes",
				"extra_allowed": []string{"contract_two_level"},
				"split":         "|",
			},
		})
		// 破坏 SA.route_by_condition 注入坏段
		mutateJSON(t, files, "spec/chain.json", func(m map[string]any) {
			asMap(asMap(m["doc_chains"])["SA"])["route_by_condition"] = "expense_sales | no_such_route"
		})
		_, err := loadFiles(files)
		if err == nil || !strings.Contains(err.Error(), "no_such_route") {
			t.Fatalf("应报出坏段 no_such_route: %v", err)
		}
	})

	t.Run("未设split_行为不变", func(t *testing.T) {
		files := cloneFiles(base)
		// 整串当单个引用（含空格管道）⇒ 必然不存在 → 报错（证明拆分确实生效于有 split 时）
		appendCheck(t, files, map[string]any{
			"id": "S6c-nosplit", "primitive": "ref_exists",
			"args": map[string]any{
				"file": "spec/chain.json", "collect": "**.route_by_condition",
				"target_file": "spec/chain.json", "target_dict": "routes",
			},
		})
		if _, err := loadFiles(files); err == nil || !strings.Contains(err.Error(), "expense_sales | expense_mgmt_advance") {
			t.Fatalf("未 split 时应整串比对失败: %v", err)
		}
	})
}

// declarePrimitive 向内存 checks.json 的 primitives 补声明（模拟 WorkBuddy 侧将
// 随 S13/S14 提交的原语声明 —— META 自检要求「引用的原语必须已声明」，探针不许绕过它）。
func declarePrimitive(t *testing.T, files map[string][]byte, id string) {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(files["spec/checks.json"], &doc); err != nil {
		t.Fatal(err)
	}
	prims, _ := doc["primitives"].(map[string]any)
	if prims == nil {
		prims = map[string]any{}
		doc["primitives"] = prims
	}
	if _, ok := prims[id]; !ok {
		prims[id] = map[string]any{"desc": "engine self-test stub", "args": map[string]any{}}
	}
	out, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	files["spec/checks.json"] = out
}

func TestSetCoversEngine(t *testing.T) { // N-022
	base := loadReal(t).ProblemsRaw

	t.Run("CT八组必备条款_合法通过", func(t *testing.T) {
		files := cloneFiles(base)
		declarePrimitive(t, files, "set_covers")
		appendCheck(t, files, map[string]any{
			"id": "S13-probe", "primitive": "set_covers",
			"args": map[string]any{
				"file": "spec/forms/CT.json", "collect": "sections[*].fields[*].clause_group",
				"required": []any{1, 2, 3, 4, 5, 6, 7, 8}, "min_hits": 8,
			},
		})
		if _, err := loadFiles(files); err != nil {
			t.Fatalf("CT 八组应覆盖: %v", err)
		}
	})

	t.Run("缺项逐条报出", func(t *testing.T) {
		files := cloneFiles(base)
		declarePrimitive(t, files, "set_covers")
		appendCheck(t, files, map[string]any{
			"id": "S13-missing", "primitive": "set_covers",
			"args": map[string]any{
				"file": "spec/forms/CT.json", "collect": "sections[*].fields[*].clause_group",
				"required": []any{1, 2, 3, 4, 5, 6, 7, 8, 99},
			},
		})
		_, err := loadFiles(files)
		if err == nil || !strings.Contains(err.Error(), "99") {
			t.Fatalf("应报出缺失项 99: %v", err)
		}
	})

	t.Run("min_hits不足报错", func(t *testing.T) {
		files := cloneFiles(base)
		declarePrimitive(t, files, "set_covers")
		appendCheck(t, files, map[string]any{
			"id": "S13-minhits", "primitive": "set_covers",
			"args": map[string]any{
				"file": "spec/forms/CT.json", "collect": "sections[*].fields[*].no_such_attr",
				"required": []any{1}, "min_hits": 1,
			},
		})
		_, err := loadFiles(files)
		if err == nil || !strings.Contains(err.Error(), "min_hits") {
			t.Fatalf("collect 命中 0 应触发 min_hits 报错: %v", err)
		}
	})
}
