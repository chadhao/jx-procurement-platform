// Package normalize 提供「文本归一」口径（PRD Q20 定案 2026-09-27）。
//
// ★ 为什么需要它：制度第二十一条第 7 项用「**同供应商当月累计 ≥1,000 元**」触发转档预警
// （防拆分的核心抓手）。若按**原样字符串**分组，同一家供应商换个写法即可绕过：
//
//	"岳阳某某化工有限公司" / "岳阳某某化工有限公司 " / "岳阳某某化工有限公司　"（全角空格）
//	"ABC 物资" / "ABC物资" / "abc物资"
//
// 于是「累计」永远达不到阈值 —— 防拆分指标被**静默规避**（本项目反复出现的"静默"缺陷形态）。
//
// ★ 口径边界（本包只做**归一**，不做「异写合并」）：
// 本包解决的是**写法差异**（空白 / 全角半角 / 大小写），这三类是纯机械差异，无需业务判断。
// 「简称 ↔ 全称」「曾用名」这类**语义等价**必须由业务给**对照表**，不在本包能力范围内 ——
// 未经对照表就强行模糊匹配（如包含关系）会把不同公司并成一家，反而制造误报。
package normalize

import (
	"strings"
	"unicode"
)

// Supplier 供应商名称归一：① 去全部空白（含全角空格 · 不间断空格）；
// ② 全角 ASCII → 半角；③ 统一为大写（仅影响拉丁字母，汉字不受影响）。
//
// 该结果用于**分组 / 比较**（`supplier_norm` 列）；**展示一律保留原名**。
func Supplier(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r == '\u3000' || r == '\u00a0': // 全角空格 / 不间断空格
			continue
		case unicode.IsSpace(r):
			continue
		case r >= '\uff01' && r <= '\uff5e': // 全角 ASCII
			b.WriteRune(r - 0xfee0)
		default:
			b.WriteRune(r)
		}
	}
	return strings.ToUpper(b.String())
}
