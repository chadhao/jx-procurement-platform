package seed

// N-061 T3：机检扩面 —— spec 侧**全可写台账** ⊇（导入源登记集 ∪ 具名豁免集）。
// ★ 与 H3.4 的区别：对照面由运行时 t_ledger_field_def（空库无行）换成
//   spec/ledger-mapping.json 的 writable 面（其正源）＋ docs 样例登记集；
//   ★ 不必连库（判据④）；★ 断言形态＝⊇ 集合（禁止计数 —— N-061 ②：数会漂、名可核）。
// ★ 豁免面唯一依据 ＝ spec/ledger-mapping.json#ledgers.L08.known_gaps（指针引用，
//   不复制其理由文本 —— 避免第二份真相）；豁免名恰 6 个（不多不少）。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	specfs "github.com/chadhao/jx-procurement-platform"
)

// l08ExemptNames L08 具名豁免清单（★ 唯一依据＝ ledger-mapping#ledgers.L08.known_gaps[id=L08-1]）。
var l08ExemptNames = []string{
	"供应商名称", "统一社会信用代码", "账户信息", "准入日期", "评级", "关联关系申报",
}

// writableCoverage 求「spec 全量 writable label 中，不在 登记集∪豁免集 的条目」。
// 空库可跑（只读两份文件字节）；返回按 「台账/列名」 排序的缺口清单。
func writableCoverage(specJSON, sampleJSON []byte) []string {
	var lm struct {
		Ledgers map[string]struct {
			Fields []struct {
				Label    string `json:"label"`
				Writable bool   `json:"writable"`
			} `json:"fields"`
		} `json:"ledgers"`
	}
	if err := json.Unmarshal(specJSON, &lm); err != nil {
		return []string{"<spec 解析失败: " + err.Error() + ">"}
	}
	var payload struct {
		LedgerField []struct {
			LedgerType string `json:"ledger_type"`
			FieldKey   string `json:"field_key"`
		} `json:"ledger_field"`
	}
	if err := json.Unmarshal(sampleJSON, &payload); err != nil {
		return []string{"<sample 解析失败: " + err.Error() + ">"}
	}
	registered := map[string]bool{}
	for _, f := range payload.LedgerField {
		registered[f.LedgerType+"\x00"+f.FieldKey] = true
	}
	exempt := map[string]bool{}
	for _, n := range l08ExemptNames {
		exempt["L08\x00"+n] = true
	}
	missing := []string{}
	// 稳定遍历（map 序不定 ⇒ 先收台账名排序）
	lts := make([]string, 0, len(lm.Ledgers))
	for lt := range lm.Ledgers {
		lts = append(lts, lt)
	}
	for i := 0; i < len(lts); i++ {
		for j := i + 1; j < len(lts); j++ {
			if lts[j] < lts[i] {
				lts[i], lts[j] = lts[j], lts[i]
			}
		}
	}
	for _, lt := range lts {
		for _, f := range lm.Ledgers[lt].Fields {
			if !f.Writable {
				continue
			}
			key := lt + "\x00" + f.Label
			if registered[key] || exempt[key] {
				continue
			}
			missing = append(missing, "台账 "+lt+" 的可写列 `"+f.Label+"`")
		}
	}
	return missing
}

// repoRoot 从包目录向上找仓库根（同 cmd/jxapproval 先例）。
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 8; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Fatal("未找到仓库根")
	return ""
}

// TestSeedWritableCoversSpecAllLedgers ★ N-061 T3 扩面机检（先红后绿 —— 顺序不可颠倒）：
// spec 全量 writable ⊆ 样例登记 ∪ L08 豁免；豁免面恰 6 个名。
func TestSeedWritableCoversSpecAllLedgers(t *testing.T) {
	specJSON, err := specfs.FS.ReadFile("spec/ledger-mapping.json")
	if err != nil {
		t.Fatal(err)
	}
	sampleJSON, err := os.ReadFile(filepath.Join(repoRoot(t), "docs", "reference", "config-mapping.sample.json"))
	if err != nil {
		t.Fatalf("读样例配置失败: %v", err)
	}
	// 豁免面恰 6 名（任务包判据③）
	if len(l08ExemptNames) != 6 {
		t.Fatalf("L08 豁免清单应恰 6 名，实为 %d：%v", len(l08ExemptNames), l08ExemptNames)
	}
	missing := writableCoverage(specJSON, sampleJSON)
	if len(missing) > 0 {
		t.Fatalf("spec 全量可写列未被 登记∪豁免 覆盖（N-061 T3 扩面 —— 先红后绿）: 缺 %v", missing)
	}
}

// TestSeedWritableCoverageExemptionScope 反向鉴别力（判据③的反面）：合成 spec（非真文件）
// 在**豁免面之外**注入一条未登记可写列 ⇒ 必须报出（不得被 L08 豁免误吞）。
func TestSeedWritableCoverageExemptionScope(t *testing.T) {
	specJSON := []byte(`{"ledgers":{"L09":{"fields":[
		{"label":"幽灵列","writable":true},
		{"label":"正常列","writable":false}
	]}}}`)
	sampleJSON := []byte(`{"ledger_field":[]}`)
	missing := writableCoverage(specJSON, sampleJSON)
	if len(missing) != 1 || missing[0] != "台账 L09 的可写列 `幽灵列`" {
		t.Fatalf("豁免面之外的未登记列必须报出，实为 %v", missing)
	}
	// 对照：注入到 L08 且名在豁免清单 ⇒ 不报
	specL08 := []byte(`{"ledgers":{"L08":{"fields":[
		{"label":"供应商名称","writable":true}
	]}}}`)
	if got := writableCoverage(specL08, sampleJSON); len(got) != 0 {
		t.Fatalf("豁免面内不应报，实为 %v", got)
	}
}
