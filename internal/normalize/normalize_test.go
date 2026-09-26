package normalize

import "testing"

// 本文件固化 Q20「供应商名称归一」的三条规则（去空白 / 全角半角 / 大小写）。
//
// ★ 这些规则是**防拆分指标不被绕过**的地基：制度第二十一条第 7 项按「同供应商当月累计
// ≥1,000 元」触发转档预警；若按原名分组，同一家换个写法即让累计永远达不到阈值 ——
// 而**不会有任何报错**（本项目反复出现的"静默"形态）。
func TestSupplier(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want string
	}{
		{"全角/半角空白与前后空格均剔除", []string{" 岳阳某某化工 ", "岳阳某某化工　", "岳阳某某化工"}, "岳阳某某化工"},
		{"不间断空格同样剔除", []string{"ABC\u00a0物资", "ABC物资"}, "ABC物资"},
		{"中间空格剔除", []string{"ABC 物资", "ABC物资"}, "ABC物资"},
		{"大小写归一", []string{"abc物资", "ABC物资", "Abc物资"}, "ABC物资"},
		{"全角字母数字归一", []string{"ＡＢＣ１２３", "ABC123"}, "ABC123"},
		{"全角括号归一（组合场景）", []string{"（甲）公司", "(甲)公司"}, "(甲)公司"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			for _, in := range c.in {
				if got := Supplier(in); got != c.want {
					t.Errorf("Supplier(%q) = %q, 期望 %q（该组内所有写法必须归一为同一分组键）", in, got, c.want)
				}
			}
		})
	}
}

// TestSupplierDoesNotMergeDifferentNames 反向：**不同公司绝不能被并成一家**。
//
// ★ 归一 ≠ 模糊匹配。本包只处理"写法差异"；「简称 ↔ 全称」这类语义等价必须由业务给对照表，
// 否则把不同供应商并成一家会制造误报（防拆分预警将指向无辜供应商）。
func TestSupplierDoesNotMergeDifferentNames(t *testing.T) {
	pairs := [][2]string{
		{"岳阳某某化工有限公司", "岳阳某某化工"},
		{"中石化湖南石化", "中石化"},
		{"ABC物资", "ABC物资公司"},
		{"供应商A", "供应商B"},
	}
	for _, p := range pairs {
		if Supplier(p[0]) == Supplier(p[1]) {
			t.Errorf("Supplier(%q) 与 Supplier(%q) 相同 —— 归一不得做包含式模糊合并", p[0], p[1])
		}
	}
}

// TestSupplierEmpty 空值一律归为空串（不产生"空供应商"分组键去参与聚合）。
func TestSupplierEmpty(t *testing.T) {
	for _, in := range []string{"", "   ", "　", "\u00a0", " \u3000 "} {
		if got := Supplier(in); got != "" {
			t.Errorf("Supplier(%q) = %q, 期望空串", in, got)
		}
	}
}
