// Package jsonutil 集中处理「JSON 文本 → Go 值」的解析。
//
// ★ 存在的理由（2026-09-27 静默审计 C6）：本项目多次栽在
// 「**坏数据被静默当成空数据**」上 —— `_ = json.Unmarshal(raw, &m)` 在 raw 非法时
// 只把 m 留空、**不报错**，于是上游拿到一个"看起来正常"的空对象：
//
//   - 运营表写路径：读到空 → 以空为基础 merge → **把既有运营字段整体覆盖丢失**
//   - 幂等键：载荷指纹读成空 → 不同的载荷被判成"相同"→ **复用首次结果**
//   - 展示路径：ext 读成空 → 台账行少了字段，用户以为"本来就没填"
//
// 所以本包的核心是**把「没有值」与「值坏了」分开**：
//   - 空白串 → 空对象 + nil（真是"没有"）
//   - 非法 JSON → nil + error（"坏了"，调用方**必须**显式处置）
package jsonutil

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Object 把 JSON 文本解析为对象。
//
// 空白串返回空对象且 err == nil；非法 JSON 或非对象返回 nil + error。
func Object(raw string) (map[string]any, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return map[string]any{}, nil
	}
	out := map[string]any{}
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return nil, fmt.Errorf("jsonutil: 解析 JSON 对象失败: %w", err)
	}
	return out, nil
}

// ObjectOrEmpty 与 Object 相同，但把错误也降级为空对象，**并如实报告是否发生降级**。
//
// ★ 给「展示路径」用：那里不方便把整页请求打断（一个坏行不该让列表 500），
// 但**必须**让调用方知道"这一行是坏的"，以便打日志或给出可见标记。
// 没有这个 bool 就退化成原来的静默行为了。
func ObjectOrEmpty(raw string) (map[string]any, bool) {
	out, err := Object(raw)
	if err != nil {
		return map[string]any{}, true
	}
	return out, false
}
