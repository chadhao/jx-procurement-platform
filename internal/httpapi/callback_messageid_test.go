package httpapi

import (
	"encoding/json"
	"testing"
)

// callback_messageid_test.go —— 「平台发数字 id」的解析守门（2026-09-28 真机根因回归）。
//
// ★ 背景：官方《三方快捷审批回调》字段表载明 `message_id` 类型为 **int64**，实测平台
// 发的正是**未加引号的数字**（`"message_id":7690275278685244603`）。我方曾用 `string`
// 接收 ⇒ `json: cannot unmarshal number into Go value of type string` ⇒ **整个 Decode
// 失败** ⇒ 回调整体 400（且 `duration_ms: 0`，错误现象离根因很远）。
//
// 本测试把「数字形态」与「字符串形态」都钉死，防止回退到 `string`。

// platformRawNumeric 复刻平台**实发**的报文形态（message_id 为数字）。
const platformRawNumeric = `{"action_type":"REJECT",` +
	`"action_context":"{\"biz_no\":\"PR-TEST-0002\",\"task_id\":\"T-20260927-01\"}",` +
	`"user_id":"18500231907","message_id":7690275278685244603,"token":"QA-TOKEN-PROBE-1"}`

// platformRawString 字符串形态（我方调 message/update 时的形态 / 兼容）。
const platformRawString = `{"action_type":"APPROVE","message_id":"7690275278685244603","token":"t"}`

// TestCallbackMessageIDNumericAccepted 数字形态必须能解析（核心回归断言）。
func TestCallbackMessageIDNumericAccepted(t *testing.T) {
	var body extCallbackBody
	if err := json.Unmarshal([]byte(platformRawNumeric), &body); err != nil {
		t.Fatalf("数字形态 message_id 必须可解析（官方类型 int64），实际报错: %v", err)
	}
	if got := body.MessageID.String(); got != "7690275278685244603" {
		t.Errorf("message_id 解析值错误: got=%q want=%q", got, "7690275278685244603")
	}
	// 同报文其余字段须照常解出（证明不是"解析成功但字段全丢"）。
	if body.ActionType != "REJECT" {
		t.Errorf("action_type 解析错误: %q", body.ActionType)
	}
	if body.UserID != "18500231907" {
		t.Errorf("user_id 解析错误: %q", body.UserID)
	}
}

// TestCallbackMessageIDStringAccepted 字符串形态同样必须可解析（读写不对称下的兼容）。
func TestCallbackMessageIDStringAccepted(t *testing.T) {
	var body extCallbackBody
	if err := json.Unmarshal([]byte(platformRawString), &body); err != nil {
		t.Fatalf("字符串形态 message_id 必须可解析，实际报错: %v", err)
	}
	if got := body.MessageID.String(); got != "7690275278685244603" {
		t.Errorf("message_id 解析值错误: got=%q", got)
	}
}

// TestCallbackMessageIDNullAndAbsent 缺省与 null 不得报错（非卡片操作路径不带该字段）。
func TestCallbackMessageIDNullAndAbsent(t *testing.T) {
	for _, raw := range []string{
		`{"action_type":"APPROVE","token":"t"}`,
		`{"action_type":"APPROVE","message_id":null,"token":"t"}`,
		`{"action_type":"APPROVE","message_id":"","token":"t"}`,
	} {
		var body extCallbackBody
		if err := json.Unmarshal([]byte(raw), &body); err != nil {
			t.Fatalf("缺省/null/空 message_id 不应报错，raw=%s err=%v", raw, err)
		}
		if body.MessageID.String() != "" {
			t.Errorf("缺省/null/空 message_id 应为空串，raw=%s got=%q", raw, body.MessageID.String())
		}
	}
}
