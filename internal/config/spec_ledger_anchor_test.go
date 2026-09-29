package config

// M8 · R-02 spec 锚定（跨层一致性）—— 把「单据 → 台账」的三方口径钉在一起：
//   spec/ledger-mapping.json（WorkBuddy 权威） ↔ config 权威名单 ↔ 样例配置（导入通道）。
// ★ spec 一改、样例一改，本测试立刻见差异（N-008② 精神：同一份 embed 字节，不复制快照）。
// ★ R-02 执行守卫的 Go 侧复核：PR 必须同时落 L02 与 L03（漏 L03 ⇒ 看板 16「需求提出人
//   任经办人的笔数」恒 0 而 0 恰是期望值 ⇒ 违规永远发现不了）。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	specfs "github.com/chadhao/jx-procurement-platform"
	"github.com/chadhao/jx-procurement-platform/internal/specload"
)

func loadSpec(t *testing.T) *specload.Bundle {
	t.Helper()
	b, err := specload.Load(specfs.FS)
	if err != nil {
		t.Fatalf("spec 加载失败: %v", err)
	}
	return b
}

// findRepoRoot 从包目录向上找仓库根（测试 CWD ＝ 包目录）。
func findRepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 8; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Fatal("未找到仓库根（go.mod）")
	return ""
}

// specDocToLedger 直读 embed 的 ledger-mapping.json#doc_to_ledger。
func specDocToLedger(t *testing.T) map[string][]string {
	t.Helper()
	raw, err := specfs.FS.ReadFile("spec/ledger-mapping.json")
	if err != nil {
		t.Fatal(err)
	}
	var lm struct {
		DocToLedger map[string]struct {
			Ledger []string `json:"ledger"`
		} `json:"doc_to_ledger"`
	}
	if err := json.Unmarshal(raw, &lm); err != nil {
		t.Fatal(err)
	}
	out := map[string][]string{}
	for doc, spec := range lm.DocToLedger {
		out[doc] = spec.Ledger
	}
	return out
}

// TestSpecLedgerTargetsWithinConfigAllowlist spec 落账目标必须 ⊆ config 权威名单：
// 全体 ⊆ LedgerTypes（12 张）且 ⊆ PerInstanceLedgerTypes（禁 L08/L10/L11/L12 —— README #20）。
func TestSpecLedgerTargetsWithinConfigAllowlist(t *testing.T) {
	for doc, ledgers := range specDocToLedger(t) {
		for _, lt := range ledgers {
			if !inList(lt, LedgerTypes) {
				t.Errorf("doc_to_ledger[%s] 的 %s 不在 config.LedgerTypes（12 张）中 —— spec/config 漂移", doc, lt)
			}
			if !inList(lt, PerInstanceLedgerTypes) {
				t.Errorf("doc_to_ledger[%s] 把 %s 当落账目标 —— 禁 L08/L10/L11/L12（README #20 / config.PerInstanceLedgerTypes）", doc, lt)
			}
		}
	}
}

// TestSpecDocLedgerMatchesSample 样例配置（导入通道）必须覆盖 spec 的全部非空落账目标，
// 且不得多出 spec 未授权的 (doc, ledger) 对。
func TestSpecDocLedgerMatchesSample(t *testing.T) {
	samplePath := filepath.Join(findRepoRoot(t), "docs", "reference", "config-mapping.sample.json")
	raw, err := os.ReadFile(samplePath)
	if err != nil {
		t.Fatalf("读取样例失败: %v", err)
	}
	var sample struct {
		LedgerType []struct {
			Key   string `json:"key"`
			Value string `json:"value"`
		} `json:"ledger_type"`
	}
	if err := json.Unmarshal(raw, &sample); err != nil {
		t.Fatal(err)
	}
	samplePairs := map[string]map[string]bool{}
	for _, e := range sample.LedgerType {
		if samplePairs[e.Key] == nil {
			samplePairs[e.Key] = map[string]bool{}
		}
		samplePairs[e.Key][e.Value] = true
	}

	spec := specDocToLedger(t)
	for doc, ledgers := range spec {
		for _, lt := range ledgers {
			if !samplePairs[doc][lt] {
				t.Errorf("样例配置缺 (doc_type=%s, ledger=%s) —— 导入通道与 spec/ledger-mapping 不一致", doc, lt)
			}
		}
	}
	for doc, m := range samplePairs {
		for lt := range m {
			want := map[string]bool{}
			for _, x := range spec[doc] {
				want[x] = true
			}
			if !want[lt] {
				t.Errorf("样例配置多出 (doc_type=%s, ledger=%s) —— spec/ledger-mapping 未授权", doc, lt)
			}
		}
	}
}

// TestSpecR02DualLedger R-02 执行守卫：PR 必须同时落 L02 与 L03。
func TestSpecR02DualLedger(t *testing.T) {
	pr := specDocToLedger(t)["PR"]
	if !inList("L02", pr) || !inList("L03", pr) {
		t.Errorf("PR 落账 = %v，必须同时含 L02 与 L03（R-02；漏 L03 ⇒ 看板 16 恒 0 不可发现）", pr)
	}
}

// TestFormsLedgerAnchored forms/*.json 的 ledger 与 doc_to_ledger 完全一致
// （与 specload S12 同判据、不同层 —— Python 门禁 / Go 加载器 / 本锚定三方互锁）。
func TestFormsLedgerAnchored(t *testing.T) {
	b := loadSpec(t)
	spec := specDocToLedger(t)
	for dt, form := range b.Forms {
		want := map[string]bool{}
		for _, x := range spec[dt] {
			want[x] = true
		}
		if len(want) == 0 && len(form.Ledger) == 0 {
			continue // RFQ/BJ/QC 等不落账单据
		}
		for _, lt := range form.Ledger {
			if !want[lt] {
				t.Errorf("forms/%s.ledger 含 %s，但 doc_to_ledger[%s] 未授权 —— spec 内部漂移", dt, lt, dt)
			}
		}
		if len(form.Ledger) != len(spec[dt]) {
			t.Errorf("forms/%s.ledger=%v 与 doc_to_ledger=%v 长度不一致", dt, form.Ledger, spec[dt])
		}
	}
}
