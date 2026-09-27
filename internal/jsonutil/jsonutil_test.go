package jsonutil

import "testing"

// TestObjectDistinguishesAbsentFromCorrupt 本包存在的**唯一理由**：
// 必须能区分「没有值」（空串）与「值坏了」（非法 JSON）。
//
// 反例（旧写法 `_ = json.Unmarshal(raw, &m)`）：两者都得到空 map + 无错误，
// 于是上游把"数据损坏"当成"本来就没填" —— 静默缺陷。
func TestObjectDistinguishesAbsentFromCorrupt(t *testing.T) {
	// ① 空串（含纯空白）→ 空对象 + **无错误**
	for _, raw := range []string{"", "   ", "\n\t"} {
		m, err := Object(raw)
		if err != nil {
			t.Errorf("空串 %q 应视为「没有值」，不该报错，实际: %v", raw, err)
		}
		if len(m) != 0 {
			t.Errorf("空串 %q 应得到空对象，实际 %v", raw, m)
		}
	}

	// ② 非法 JSON / 非对象 → 必须**报错**，且返回值不得假装可用
	bad := []string{
		`{"a":`,            // 截断
		`{a:1}`,            // 键未加引号
		`[1,2]`,            // 数组不是对象
		`"text"`,           // 标量
		`123`,              // 数字
		`{"a":1} trailing`, // 尾部垃圾
	}
	for _, raw := range bad {
		m, err := Object(raw)
		if err == nil {
			t.Errorf("非法 JSON %q 必须报错，实际返回 %v", raw, m)
		}
		if m != nil {
			t.Errorf("非法 JSON %q 应返回 nil（不得给出「看起来可用」的空对象），实际 %v", raw, m)
		}
	}

	// ③ 正常对象
	m, err := Object(`{"a":1,"b":"x"}`)
	if err != nil {
		t.Fatalf("合法 JSON 不该报错: %v", err)
	}
	if m["b"] != "x" {
		t.Errorf("解析结果不对: %v", m)
	}
}

// TestObjectOrEmptyReportsDegrade 降级版必须**如实报告**是否发生降级。
func TestObjectOrEmptyReportsDegrade(t *testing.T) {
	if _, degraded := ObjectOrEmpty(""); degraded {
		t.Error("空串不是降级")
	}
	if _, degraded := ObjectOrEmpty(`{"ok":1}`); degraded {
		t.Error("合法 JSON 不是降级")
	}
	m, degraded := ObjectOrEmpty(`{oops}`)
	if !degraded {
		t.Error("非法 JSON 必须报告为降级（否则调用方无法留痕）")
	}
	if len(m) != 0 {
		t.Errorf("降级时应返回空对象，实际 %v", m)
	}
}
