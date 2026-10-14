package permission

// N-079：`ProjectDeep` 递归面的**类型泄漏**回归（按具体类型枚举 ⇒ typed slice 落 `default`
// ⇒ 金额类键对「禁金额」角色静默泄漏）。
//
// ★ 本文件是**反例驱动**的：先构造会落 `default` 的容器载荷，断言其受限键**不得出现在线缆输出**。
//   ⇒ 在「按具体类型枚举」的旧实现下【红】，在「按 reflect.Kind 归一」的新实现下【绿】。

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

// recLeakRow / recLeakOuter 反例载体：含 `sum_cents` / `amount_cents` 金额键的 typed struct。
type recLeakRow struct {
	Supplier string `json:"supplier"`
	SumCents int64  `json:"sum_cents"`
}

type recLeakOuter struct {
	Name    string       `json:"name"`
	Amount  int64        `json:"amount_cents"`
	Entries []recLeakRow `json:"entries"`
}

// wireTree 把投影结果编成 JSON 再解析回 `any`：断言的是**实际线缆输出**，而非内存形态。
func wireTree(t *testing.T, v any) any {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("投影结果无法 JSON 序列化: %v", err)
	}
	var tree any
	if err := json.Unmarshal(b, &tree); err != nil {
		t.Fatalf("JSON 解析失败: %v", err)
	}
	return tree
}

// containsKeyDeep 在任意嵌套的线缆结构里找键（对象键 / 数组元素递归）。
func containsKeyDeep(v any, key string) bool {
	switch t := v.(type) {
	case map[string]any:
		for k, vv := range t {
			if k == key || containsKeyDeep(vv, key) {
				return true
			}
		}
	case []any:
		for _, vv := range t {
			if containsKeyDeep(vv, key) {
				return true
			}
		}
	}
	return false
}

// TestProjectDeepContainerKindNoLeak ★★ 反例驱动：**任意容器类型**（typed slice / []*T / array /
// 裸 struct / 指针 / 非 string-key map）内的金额键都**不得**出现在线缆输出里。
//
// ★ 修复前（按具体类型枚举）：`[]recLeakRow` 落 `default` 原样返回 ⇒ `sum_cents` 泄漏 ⇒ 本用例【红】。
// ★ 修复后（按 reflect.Kind 归一）：全部被裁剪 ⇒ 【绿】。
func TestProjectDeepContainerKindNoLeak(t *testing.T) {
	deny := []string{"amount_cents", "sum_cents", "change_cents"}
	cases := []struct {
		name    string
		payload any
	}{
		{"typed-slice-of-struct", []recLeakRow{{Supplier: "甲", SumCents: 100}}},
		{"slice-of-pointer", []*recLeakRow{{Supplier: "甲", SumCents: 100}}},
		{"array-of-struct", [1]recLeakRow{{Supplier: "甲", SumCents: 100}}},
		{"bare-struct", recLeakOuter{Name: "x", Amount: 100, Entries: []recLeakRow{{SumCents: 7}}}},
		{"pointer-to-struct", &recLeakOuter{Name: "x", Amount: 100}},
		{"typed-string-map", map[string]string{"a": "b"}},
		{"struct-inside-any-slice", []any{recLeakRow{SumCents: 9}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ProjectDeep(map[string]any{"x": tc.payload}, nil, deny, nil)
			tree := wireTree(t, got)
			for _, k := range deny {
				if containsKeyDeep(tree, k) {
					t.Fatalf("容器 %s 内的受限键 %q 泄漏到线缆输出：%v", tc.name, k, tree)
				}
			}
		})
	}
}

// TestProjectDeepScalarAndOpaquePreserved 标量与「自序列化类型」：投影**不得改变线缆输出**。
// ★ 防「为堵容器漏面而误伤 time.Time / []byte」——它们的线缆形态必须与未投影时逐字一致。
func TestProjectDeepScalarAndOpaquePreserved(t *testing.T) {
	ts := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	row := map[string]any{
		"n": 7, "s": "v", "b": true, "f": 1.5,
		"t": ts, "raw": []byte("ab"),
	}
	got := ProjectDeep(row, nil, nil, nil)
	if got["n"] != 7 || got["s"] != "v" || got["b"] != true || got["f"] != 1.5 {
		t.Errorf("标量被改动: %v", got)
	}
	// 线缆等价：整行 JSON 与未投影时一致（无 deny/sensitive ⇒ 无裁剪）。
	want, _ := json.Marshal(row)
	have, _ := json.Marshal(got)
	if string(want) != string(have) {
		t.Errorf("投影改变了线缆输出：\nwant=%s\n got=%s", want, have)
	}
}

// TestProjectDeepContainerKindWhitelist ★ 机检判据（防复发）：`isContainerKind` 的
// reflect.Kind 白名单必须**恰好**覆盖容器类集合 —— 少一类 ⇒ 该类容器会静默漏、多一类 ⇒ 误伤。
//
// ★ 单点变异（从白名单删 `reflect.Struct`）⇒ 本用例【恰红】，且反例用例 `bare-struct`【恰红】；
// 无关的 `typed-slice-of-struct` 保持【绿】。
func TestProjectDeepContainerKindWhitelist(t *testing.T) {
	want := map[reflect.Kind]bool{
		reflect.Map:     true,
		reflect.Slice:   true,
		reflect.Array:   true,
		reflect.Struct:  true,
		reflect.Pointer: true,
	}
	for k := reflect.Invalid; k <= reflect.UnsafePointer; k++ {
		if got := isContainerKind(k); got != want[k] {
			t.Errorf("isContainerKind(%v)=%v，期望 %v（白名单与容器类集合不一致 ⇒ 漏面或误伤）",
				k, got, want[k])
		}
	}
}
