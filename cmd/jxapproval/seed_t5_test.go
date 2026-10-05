package main

// T5 / N-024 验收：可写台账列未登记 ⇒ 提示（label ↔ field_key 口径，非阻断）。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	specfs "github.com/chadhao/jx-procurement-platform"
	"github.com/chadhao/jx-procurement-platform/internal/config"
)

// findRepoRootInTest 从包目录向上找仓库根（测试 CWD＝包目录）。
func findRepoRootInTest(t *testing.T) string {
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

func TestUnregisteredWritableLedgerFields(t *testing.T) {
	lm := []byte(`{
	  "ledgers": {
	    "L04": {"fields": [
	      {"label": "合同编号", "writable": false},
	      {"label": "履约状态", "writable": true},
	      {"label": "集团付款状态", "writable": true}
	    ]},
	    "L01": {"fields": [
	      {"label": "付款凭据号", "writable": true}
	    ]}
	  }
	}`)

	t.Run("全未登记_逐条报出", func(t *testing.T) {
		p := &config.ImportPayload{}
		got := unregisteredWritableLedgerFields(p, lm)
		if len(got) != 3 {
			t.Fatalf("应报 3 条（L04 两条 + L01 一条），实为 %d：%v", len(got), got)
		}
		joined := strings.Join(got, "\n")
		// ★ 比对口径是 label（不是 name —— N-024 踩过）
		for _, want := range []string{"履约状态", "集团付款状态", "付款凭据号", "台账页将无写入入口"} {
			if !strings.Contains(joined, want) {
				t.Errorf("提示缺 %q：%v", want, got)
			}
		}
		// writable:false 不报
		if strings.Contains(joined, "合同编号") {
			t.Errorf("只读列不应报出：%v", got)
		}
	})

	t.Run("已按label登记_不报", func(t *testing.T) {
		p := &config.ImportPayload{LedgerField: []config.ImportLedgerField{
			{LedgerType: "L04", FieldKey: "履约状态"},
			{LedgerType: "L04", FieldKey: "集团付款状态"},
			{LedgerType: "L01", FieldKey: "付款凭据号"},
		}}
		if got := unregisteredWritableLedgerFields(p, lm); len(got) != 0 {
			t.Errorf("全部按 label 登记后应无提示，实为 %v", got)
		}
	})

	t.Run("按name登记_仍报（口径是label）", func(t *testing.T) {
		// 用 snake_case name 登记 ⇒ 与 label 对不上 ⇒ 必须仍报（防"全部误报"的反向假绿）
		p := &config.ImportPayload{LedgerField: []config.ImportLedgerField{
			{LedgerType: "L04", FieldKey: "performance_status"},
			{LedgerType: "L04", FieldKey: "group_payment_status"},
			{LedgerType: "L01", FieldKey: "payment_receipt_no"},
		}}
		if got := unregisteredWritableLedgerFields(p, lm); len(got) != 3 {
			t.Errorf("用 name 冒充 field_key 应全部报出，实为 %v", got)
		}
	})

	t.Run("真实spec_具名豁免集相等且无只读台账泄漏", func(t *testing.T) {
		// ★ N-026：原「不设期望条数」无鉴别力 —— 真 spec 插入假可写列也测不出。
		//   期望清单由 WorkBuddy 独立算出（2026-09-30，议题 N-026），直接采用；
		//   ★ spec 演进使条数变化时**同步改断言并在议题回执**，不得改回「不设条数」。
		specBytes, err := specfs.FS.ReadFile("spec/ledger-mapping.json")
		if err != nil {
			t.Fatal(err)
		}
		// ★ 载荷＝样例配置（真实导入基线 docs/reference/config-mapping.sample.json）
		//   —— 空载荷会把全部 writable 列报出来，那不是「真实导入」场景；
		//   期望＝样例基线下的缺口（N-026 口径）。
		sampleRaw, err := os.ReadFile(filepath.Join(findRepoRootInTest(t), "docs", "reference", "config-mapping.sample.json"))
		if err != nil {
			t.Fatalf("读取样例配置失败: %v", err)
		}
		var payload config.ImportPayload
		if err := json.Unmarshal(sampleRaw, &payload); err != nil {
			t.Fatalf("样例配置解析失败: %v", err)
		}
		p := &payload
		got := unregisteredWritableLedgerFields(p, specBytes)

		// ★ N-061 T2（N-026 口径：断言=具名集合相等，数会漂、名可核对；不改回「不设条数」）：
		//   ★ 豁免名清单的唯一依据 ＝ spec/ledger-mapping.json#ledgers.L08.known_gaps（指针引用，
		//     不复制其理由文本 ⇒ 避免第二份真相）；L08 六列本期不做补登记（具名豁免、不删声明）。
		//   T1 完成后预期漂移集**只剩** L08 六列（L01 核销后余额已补登记）。
		want := []string{
			"台账 L08 的可写列 `供应商名称` 未登记 ⇒ 台账页将无写入入口",
			"台账 L08 的可写列 `统一社会信用代码` 未登记 ⇒ 台账页将无写入入口",
			"台账 L08 的可写列 `账户信息` 未登记 ⇒ 台账页将无写入入口",
			"台账 L08 的可写列 `准入日期` 未登记 ⇒ 台账页将无写入入口",
			"台账 L08 的可写列 `评级` 未登记 ⇒ 台账页将无写入入口",
			"台账 L08 的可写列 `关联关系申报` 未登记 ⇒ 台账页将无写入入口",
		}
		gotSet := make(map[string]bool, len(got))
		for _, g := range got {
			gotSet[g] = true
		}
		wantSet := make(map[string]bool, len(want))
		for _, w := range want {
			wantSet[w] = true
		}
		// 缺豁免条目 ⇒ 红（spec 侧 writable 消失/改名必须暴露）
		for _, w := range want {
			if !gotSet[w] {
				t.Errorf("缺豁免清单条目 %q；实际：%v", w, got)
			}
		}
		// ★ 豁免清单以外的新漂移 ⇒ 必须失败（不许被吞掉）
		for _, g := range got {
			if !wantSet[g] {
				t.Errorf("出现豁免清单以外的条目 %q（新漂移不许吞掉）；实际：%v", g, got)
			}
		}
		joined := strings.Join(got, "\n")
		// ★ 防噪声回潮（ledger-mapping#_invariants）：只读/派生台账不得出现在提示里
		for _, lt := range []string{"台账 L10", "台账 L11", "台账 L12"} {
			if strings.Contains(joined, lt) {
				t.Errorf("只读台账 %s 泄漏进提示（writable 与 storage 矛盾回潮？）", lt)
			}
		}
	})

	// ★ N-061 T2③ 反向鉴别力：合成 spec（子例内构造的 bytes，非改真文件）——
	//   给**豁免面之外**的台账（L05）注入一条未登记可写列 ⇒ 必须报出，
	//   不得被 L08 豁免误吞（豁免面只含 L08 六列、不多不少）。
	t.Run("合成spec_豁免面外漂移必须报出", func(t *testing.T) {
		specBytes, err := specfs.FS.ReadFile("spec/ledger-mapping.json")
		if err != nil {
			t.Fatal(err)
		}
		var doc map[string]any
		if err := json.Unmarshal(specBytes, &doc); err != nil {
			t.Fatal(err)
		}
		ledgers, _ := doc["ledgers"].(map[string]any)
		l05, _ := ledgers["L05"].(map[string]any)
		fields, _ := l05["fields"].([]any)
		l05["fields"] = append(fields, map[string]any{
			"name": "synthetic_drift", "label": "合成漂移列", "writable": true,
		})
		synth, err := json.Marshal(doc)
		if err != nil {
			t.Fatal(err)
		}
		sampleRaw, err := os.ReadFile(filepath.Join(findRepoRootInTest(t), "docs", "reference", "config-mapping.sample.json"))
		if err != nil {
			t.Fatalf("读取样例配置失败: %v", err)
		}
		var payload config.ImportPayload
		if err := json.Unmarshal(sampleRaw, &payload); err != nil {
			t.Fatalf("样例配置解析失败: %v", err)
		}
		got := unregisteredWritableLedgerFields(&payload, synth)
		found := false
		for _, g := range got {
			if strings.Contains(g, "台账 L05 的可写列 `合成漂移列`") {
				found = true
			}
		}
		if !found {
			t.Errorf("豁免面外的新漂移被吞掉（应报出台账 L05 的可写列 `合成漂移列`）；实际：%v", got)
		}
	})
}
