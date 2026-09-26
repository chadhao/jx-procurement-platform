// Package inbox 实现事件收件箱（同步极短路径，M3）。
//
// ★ 硬约束（写在代码里）：
//  1. 幂等键 = 事件级唯一 ID：2.0 版取 header.event_id，1.0 版取顶层 uuid；
//     绝不用 instance_code + status（会吞掉驳回重提的第二次 PENDING）。
//  2. 取不到事件 ID 时拒绝入库并告警，不得退化为时间戳兜底。
//  3. 同步路径只做：解析幂等键 → INSERT OR IGNORE 写收件箱 + 落一条待处理作业 → 立即返回。
package inbox

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// ErrMissingEventID 缺少事件级唯一 ID（拒绝入库并告警，不退化为时间戳）。
var ErrMissingEventID = errors.New("inbox: 事件缺少幂等键（2.0 版 header.event_id / 1.0 版 uuid）")

// Event 归一化后的事件（版本无关）。
type Event struct {
	IdemKey       string // 幂等键 = 事件级唯一 ID
	SchemaVersion string // "2.0" | "1.0"
	EventType     string // approval_instance / approval_task 等
	InstanceCode  string
	ApprovalCode  string
	Status        string
	Raw           []byte
}

// Extract 提取幂等键与事件元信息。
// 判定顺序：2.0 版（含 header.event_id）优先，其次 1.0 版（顶层 uuid）；两者皆无则拒绝。
func Extract(payload []byte) (*Event, error) {
	if len(payload) == 0 {
		return nil, fmt.Errorf("%w: 报文为空", ErrMissingEventID)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(payload, &top); err != nil {
		return nil, fmt.Errorf("%w: 报文非法 JSON: %v", ErrMissingEventID, err)
	}

	ev := &Event{Raw: append([]byte(nil), payload...)}

	// ---- 2.0 版：schema/header，幂等键取 header.event_id ----
	if headerRaw, ok := top["header"]; ok {
		var header map[string]json.RawMessage
		if err := json.Unmarshal(headerRaw, &header); err == nil {
			if id := rawString(header, "event_id"); id != "" {
				ev.IdemKey = id
				ev.SchemaVersion = "2.0"
				ev.EventType = rawString(header, "event_type")
			}
		}
	}

	// ---- 1.0 版：顶层 uuid ----
	if ev.IdemKey == "" {
		if id := rawString(top, "uuid"); id != "" {
			ev.IdemKey = id
			ev.SchemaVersion = "1.0"
			ev.EventType = rawString(top, "type")
		}
	}

	if ev.IdemKey == "" {
		return nil, ErrMissingEventID
	}

	// 事件类型兜底
	if ev.EventType == "" {
		ev.EventType = rawString(top, "type")
	}

	// 事件体：instance_code / status / approval_code
	if bodyRaw, ok := top["event"]; ok {
		fillFromBody(ev, bodyRaw)
	}
	return ev, nil
}

// fillFromBody 从 event 事件体中提取实例信息（键名做兼容处理）。
func fillFromBody(ev *Event, bodyRaw json.RawMessage) {
	var body map[string]json.RawMessage
	if err := json.Unmarshal(bodyRaw, &body); err != nil {
		return
	}
	ev.InstanceCode = firstNonEmpty(
		rawString(body, "instance_code"),
		rawString(body, "instance_id"),
		rawString(body, "instanceId"),
	)
	ev.Status = firstNonEmpty(rawString(body, "status"), rawString(body, "instance_status"))
	ev.ApprovalCode = rawString(body, "approval_code")
}

// rawString 读取 RawMessage 中的字符串值（非字符串返回空）。
func rawString(m map[string]json.RawMessage, key string) string {
	raw, ok := m[key]
	if !ok {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return ""
	}
	return strings.TrimSpace(s)
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
