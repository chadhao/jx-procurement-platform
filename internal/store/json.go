package store

import (
	"encoding/json"
	"strings"
)

// marshalStrings 将字符串切片序列化为 JSON 数组文本；nil 返回空串（落库为 NULL）。
func marshalStrings(in []string) string {
	if in == nil {
		return ""
	}
	b, err := json.Marshal(in)
	if err != nil {
		return ""
	}
	return string(b)
}

// unmarshalStrings 反序列化 JSON 数组文本；空串返回 nil。
func unmarshalStrings(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	var out []string
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return nil
	}
	return out
}
