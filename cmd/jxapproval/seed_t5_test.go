package main

// T5 / N-024 验收：可写台账列未登记 ⇒ 提示（label ↔ field_key 口径，非阻断）。

import (
	"strings"
	"testing"

	specfs "github.com/chadhao/jx-procurement-platform"
	"github.com/chadhao/jx-procurement-platform/internal/config"
)

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

	t.Run("真实ledger-mapping_可解析", func(t *testing.T) {
		// 用真实 spec（内嵌字节）跑一遍 —— 结构兼容性冒烟（不设期望条数：随 spec 演进）
		specBytes, err := specfs.FS.ReadFile("spec/ledger-mapping.json")
		if err != nil {
			t.Fatal(err)
		}
		p := &config.ImportPayload{}
		_ = unregisteredWritableLedgerFields(p, specBytes) // 不 panic 即结构兼容
	})
}
