package worker

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/chadhao/jx-procurement-platform/internal/platform/feishu"
)

// AttachmentRef 从表单控件值中解析出的**附件引用**（只含元数据，不含文件本体）。
type AttachmentRef struct {
	FileID string
	Name   string
	Size   *int64
}

// CollectAttachments 从实例表单字段中收集附件引用。
//
// ★ **只解析、不下载**：事件处理有 3 秒窗口，网络 IO 绝不能放在同步路径上。
// 文件本体按需拉取（见 httpapi 的下载端点与凭证包）。
//
// ★ 形态容忍：模板控件形态**尚未定稿**（Q1），且飞书附件控件的取值写法有多种。故：
//   - 逐个字段尝试解析，**任一步失败即跳过该字段**（绝不因一个陌生形态让整条入库失败）；
//   - 只接受**含非空 `file_token` / `file_id`** 的条目 —— 这是"确实是附件"的强信号，
//     从而不必依赖控件类型名（避免模板改名即失效）。
//
// 兼容的取值形态（官方常见写法）：
//
//	[{"file_token":"xxx","name":"a.pdf","size":123}, ...]
//	["xxx", "yyy"]                 // 纯 token 数组
//	{"file_token":"xxx"}           // 单对象
func CollectAttachments(det *feishu.InstanceDetail) []AttachmentRef {
	if det == nil || len(det.Fields) == 0 {
		return nil
	}
	var out []AttachmentRef
	seen := map[string]bool{}
	for _, f := range det.Fields {
		raw := strings.TrimSpace(f.RawJSON)
		if raw == "" || (raw[0] != '[' && raw[0] != '{') {
			continue // 标量文本不可能是附件
		}
		var v any
		if err := json.Unmarshal([]byte(raw), &v); err != nil {
			continue
		}
		for _, ref := range attachmentRefsFrom(v) {
			if ref.FileID == "" || seen[ref.FileID] {
				continue
			}
			seen[ref.FileID] = true
			out = append(out, ref)
		}
	}
	return out
}

// attachmentRefsFrom 从任意 JSON 值中抽取附件引用（数组 / 对象 / 字符串）。
func attachmentRefsFrom(v any) []AttachmentRef {
	switch t := v.(type) {
	case string:
		s := strings.TrimSpace(t)
		if s == "" {
			return nil
		}
		return []AttachmentRef{{FileID: s}}
	case []any:
		var out []AttachmentRef
		for _, e := range t {
			out = append(out, attachmentRefsFrom(e)...)
		}
		return out
	case map[string]any:
		id := firstNonEmptyOf(jsonStrOf(t["file_token"]), jsonStrOf(t["file_id"]), jsonStrOf(t["token"]))
		if id == "" {
			return nil
		}
		ref := AttachmentRef{FileID: id, Name: jsonStrOf(t["name"])}
		if n, ok := jsonIntOf(t["size"]); ok {
			ref.Size = &n
		}
		return []AttachmentRef{ref}
	}
	return nil
}

// jsonStrOf 取字符串值（非字符串返回空串）。
func jsonStrOf(v any) string {
	if s, ok := v.(string); ok {
		return strings.TrimSpace(s)
	}
	return ""
}

// jsonIntOf 取整数值（JSON number / 数字字符串）。
func jsonIntOf(v any) (int64, bool) {
	switch t := v.(type) {
	case float64:
		return int64(t), true
	case string:
		if n, err := strconv.ParseInt(strings.TrimSpace(t), 10, 64); err == nil {
			return n, true
		}
	}
	return 0, false
}

// firstNonEmptyOf 返回首个非空字符串。
func firstNonEmptyOf(vals ...string) string {
	for _, v := range vals {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}
