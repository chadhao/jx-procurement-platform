package feishu

import (
	"encoding/json"
	"strings"
	"testing"
)

// external_test.go —— externalApprovalBody 组装单测（2026-09-27 实测教训回归）。
//
// 背景：`POST /open-apis/approval/v4/external_approvals` 是 **upsert**，平台会把
// **未传**的开关字段重置为默认值 false ⇒ body 必须**显式传全**全部开关，
// 否则执行一次定义装载就会把飞书侧已配置好的 enable_quick_operate=true
// （「同意/拒绝」两键）重置打没。本文件锁死这一语义：**键必须存在**，值按入参。

// wantExternalSwitches 目标开关集（对齐 2026-09-27 真机读回基线）。
var wantExternalSwitches = map[string]bool{
	"enable_quick_operate": true,
	"allow_batch_operate":  true,
	"support_batch_read":   true,
	"support_pc":           true,
	"support_mobile":       true,
	"enable_mark_readed":   false, // 显式 false：键必须在，值不得省略
}

// TestExternalApprovalBodyExplicitSwitches 全量开关定义：external.* 开关必须逐一
// 显式出现在 JSON 中（键存在 ＝ 显式传值；漏键 ⇒ 平台重置为默认 false），
// 且回调 URL/token 原样下发。
func TestExternalApprovalBodyExplicitSwitches(t *testing.T) {
	def := ExternalApprovalDef{
		ApprovalCode:       "JXQA-TEST-0001",
		Name:               "①采购报备单",
		GroupName:          "采购审批",
		CreateLinkPC:       "http://office.hunanyichu.com:5500/",
		CreateLinkMobile:   "http://office.hunanyichu.com:5500/",
		SupportPC:          true,
		SupportMobile:      true,
		EnableQuickOperate: true,
		AllowBatchOperate:  true,
		SupportBatchRead:   true,
		EnableMarkReaded:   false,
		CallbackURL:        "http://office.hunanyichu.com:5500/approval/external/callback",
		CallbackToken:      "QA-TOKEN-PROBE-1",
	}
	raw, err := externalApprovalBody(def)
	if err != nil {
		t.Fatalf("组装失败: %v", err)
	}
	var body struct {
		ApprovalCode string         `json:"approval_code"`
		Name         string         `json:"approval_name"`
		External     map[string]any `json:"external"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("解析 body 失败: %v", err)
	}

	// ① 顶层字段。
	if body.ApprovalCode != "JXQA-TEST-0001" {
		t.Fatalf("approval_code = %q, 期望 JXQA-TEST-0001", body.ApprovalCode)
	}
	if body.Name != "①采购报备单" {
		t.Fatalf("approval_name = %q, 期望 ①采购报备单", body.Name)
	}

	// ② external.* 开关：键必须逐一存在（显式传值），值与入参一致。
	for _, key := range []string{
		"enable_quick_operate", "allow_batch_operate", "support_batch_read",
		"support_pc", "support_mobile", "enable_mark_readed",
	} {
		v, ok := body.External[key]
		if !ok {
			t.Fatalf("external.%s 缺失：upsert 未传字段会被平台重置为默认 false", key)
		}
		b, isBool := v.(bool)
		if !isBool {
			t.Fatalf("external.%s 不是 bool: %v (%T)", key, v, v)
		}
		if want, ok := wantExternalSwitches[key]; !ok || b != want {
			t.Fatalf("external.%s = %v, 期望 %v", key, b, want)
		}
	}

	// ③ 回调 URL / token 原样下发（action_callback_url 由装载通道拼好传入）。
	if got, _ := body.External["action_callback_url"].(string); got != def.CallbackURL {
		t.Fatalf("action_callback_url = %q, 期望 %q", got, def.CallbackURL)
	}
	if got, _ := body.External["action_callback_token"].(string); got != def.CallbackToken {
		t.Fatalf("action_callback_token = %q, 期望 %q", got, def.CallbackToken)
	}

	// ④ 原始 JSON 键存在性复核（防解析层吞键）。
	for _, key := range []string{
		"enable_quick_operate", "support_pc", "support_mobile",
		"allow_batch_operate", "support_batch_read", "enable_mark_readed",
	} {
		if !strings.Contains(string(raw), `"`+key+`":`) {
			t.Fatalf("原始 JSON 缺 external.%s", key)
		}
	}
}

// TestExternalApprovalBodyAllFalseStillExplicit 全 false 定义：即便开关全为 false，
// 键也必须出现在 JSON 中（省略 ≠ false；省略会被平台重置——本测锁死"显式传全"）。
func TestExternalApprovalBodyAllFalseStillExplicit(t *testing.T) {
	raw, err := externalApprovalBody(ExternalApprovalDef{
		Name: "min", // 开关全零值
	})
	if err != nil {
		t.Fatalf("组装失败: %v", err)
	}
	var body struct {
		External map[string]any `json:"external"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("解析 body 失败: %v", err)
	}
	for _, key := range []string{
		"enable_quick_operate", "allow_batch_operate",
		"support_batch_read", "enable_mark_readed",
	} {
		if _, ok := body.External[key]; !ok {
			t.Fatalf("零值定义仍须显式下发 external.%s（漏键 ⇒ 平台重置）", key)
		}
	}
}

// TestExternalApprovalBodyNewCreateOmitsCode 新建形态：approval_code 为空 ⇒ 不下发
// 该键（由平台新建），其余字段照常。
func TestExternalApprovalBodyNewCreateOmitsCode(t *testing.T) {
	raw, err := externalApprovalBody(ExternalApprovalDef{
		Name:               "新建定义",
		EnableQuickOperate: true,
		SupportPC:          true,
		SupportMobile:      true,
		AllowBatchOperate:  true,
		SupportBatchRead:   true,
	})
	if err != nil {
		t.Fatalf("组装失败: %v", err)
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("解析 body 失败: %v", err)
	}
	if _, ok := body["approval_code"]; ok {
		t.Fatalf("新建形态不得下发 approval_code（由平台生成）")
	}
	ext, _ := body["external"].(map[string]any)
	if v, ok := ext["enable_quick_operate"].(bool); !ok || !v {
		t.Fatalf("新建形态 enable_quick_operate = %v, 期望显式 true", ext["enable_quick_operate"])
	}
}
