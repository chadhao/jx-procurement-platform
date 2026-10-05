package specload

// `N-049` ② 第二半 · 落地段③ —— **派生白名单 `target_filter`** 的探针（**Go 侧真引擎**）。
//
// ★ 输入 ＝ 与生产**同一份 embed 字节**（`specfs.FS`，`N-008` 修正②）；
//   ★ 变异**只在内存里**做，**不落盘第二份 spec** ⇒ 真实文件零改动。
//
// 它证明四件事（★ 两侧真引擎的反证 —— Python 侧见 `scripts/_probe_n049.py` 第 7 段）：
//
//	P1 **无变异 ＋ 新白名单 ⇒ `S19` 0 处** —— 切换**无红窗**（与批 23 的数据面结论一致）。
//	P2 **同一变异 ＋ 新白名单 ⇒ 恰 1 处** —— 缺口存在性。
//	P3 ★★ **同一变异 ＋ 旧白名单（摘掉 `target_filter`）⇒ 0 处** ——
//	   即**旧白名单对该缺口静默放行** ⇒ 「本参数确实关掉了这个洞」的反证。
//	P4 **无变异 ＋ 旧白名单 ⇒ 0 处**（对照：P3 的 0 不是「摘掉参数导致报错被吞」）。
//
// ★ 本探针**不新增判据、不改 `spec/**`**；断言只依赖「`S19` 报错条数 + 该报错是否点名被变异的取值」。

import (
	"encoding/json"
	"sort"
	"strings"
	"testing"

	specfs "github.com/chadhao/jx-procurement-platform"
)

// n049CloneFiles 深拷贝 files 映射（避免变异的切片别名污染 `Bundle.ProblemsRaw`）。
func n049CloneFiles(src map[string][]byte) map[string][]byte {
	out := make(map[string][]byte, len(src))
	for k, v := range src {
		cp := make([]byte, len(v))
		copy(cp, v)
		out[k] = cp
	}
	return out
}

// n049S19Problems 只取 `[S19]` 前缀的问题行。
func n049S19Problems(files map[string][]byte) []string {
	out := []string{}
	for _, p := range runChecklist(files) {
		if strings.HasPrefix(p, "[S19]") {
			out = append(out, p)
		}
	}
	return out
}

// n049SetFirstRouteActor 把 `routes` 下**第一处字符串 `actor`** 改成 `to`（确定性：按字典序遍历）。
// ★ 该位点在 `routes.**.actor` 收集面内（即 `S19` 的判据面）。
func n049SetFirstRouteActor(t *testing.T, chainRaw []byte, to string) []byte {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(chainRaw, &doc); err != nil {
		t.Fatalf("chain.json 解析失败：%v", err)
	}
	routes, _ := doc["routes"].(map[string]any)
	if len(routes) == 0 {
		t.Fatal("chain.json#routes 为空 —— 探针前提不成立")
	}
	names := make([]string, 0, len(routes))
	for k := range routes {
		names = append(names, k)
	}
	sort.Strings(names)
	var old string
	if !n049SetActor(routes[names[0]], to, &old) {
		t.Fatalf("routes.%s 下未找到字符串 `actor` —— 探针前提不成立", names[0])
	}
	out, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("chain.json 序列化失败：%v", err)
	}
	t.Logf("变异：routes.%s 中首个 `actor` %q → %q", names[0], old, to)
	return out
}

func n049SetActor(node any, to string, old *string) bool {
	switch v := node.(type) {
	case map[string]any:
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if k == "actor" {
				if s, ok := v[k].(string); ok {
					if *old == "" {
						*old = s
					}
					v[k] = to
					return true
				}
			}
			if n049SetActor(v[k], to, old) {
				return true
			}
		}
	case []any:
		for _, e := range v {
			if n049SetActor(e, to, old) {
				return true
			}
		}
	}
	return false
}

// n049StripTargetFilter 去掉 `S19.args.target_filter` ⇒ 复现「旧白名单＝`roles` 全表键」。
func n049StripTargetFilter(t *testing.T, checksRaw []byte) []byte {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(checksRaw, &doc); err != nil {
		t.Fatalf("checks.json 解析失败：%v", err)
	}
	checks, _ := doc["checks"].([]any)
	found := false
	for _, c := range checks {
		cm, _ := c.(map[string]any)
		if cm["id"] != "S19" {
			continue
		}
		args, _ := cm["args"].(map[string]any)
		if _, ok := args["target_filter"]; !ok {
			t.Fatal("S19.args 无 target_filter —— 规格未按预期落地（本探针前提不成立）")
		}
		delete(args, "target_filter")
		found = true
	}
	if !found {
		t.Fatal("checks.json 里找不到 S19")
	}
	out, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("checks.json 序列化失败：%v", err)
	}
	return out
}

func TestN049TargetFilterProbe(t *testing.T) {
	b, err := Load(specfs.FS)
	if err != nil {
		t.Fatalf("真实 spec 加载失败：%v", err)
	}
	base := n049CloneFiles(b.ProblemsRaw)

	// 前提：现行 S19.args 确实带 target_filter，且收集面为 routes.**.actor
	var checksDoc map[string]any
	if err := json.Unmarshal(base["spec/checks.json"], &checksDoc); err != nil {
		t.Fatal(err)
	}
	{
		ok := false
		for _, c := range checksDoc["checks"].([]any) {
			cm, _ := c.(map[string]any)
			if cm["id"] != "S19" {
				continue
			}
			args := cm["args"].(map[string]any)
			if tf, ok2 := args["target_filter"].(map[string]any); ok2 {
				ok = tf["field"] == "node_actor_kind"
				if !ok {
					t.Errorf("S19.args.target_filter.field 应为 node_actor_kind，实为 %v", tf["field"])
				}
			}
			if args["collect"] != "routes.**.actor" {
				t.Errorf("S19.args.collect 应为 routes.**.actor，实为 %v", args["collect"])
			}
		}
		if !ok {
			t.Fatal("S19.args 未带 target_filter（或 field 不符）—— 落地段③ 未生效")
		}
	}
	t.Log("前提成立：S19.args 带 target_filter{field=node_actor_kind, in=[approver,action]} · collect=routes.**.actor")

	// P1 无变异 ＋ 新白名单 ⇒ 0 处
	p1 := n049S19Problems(base)
	if len(p1) != 0 {
		t.Errorf("P1：无变异下 S19 应 0 处（切换无红窗），实为 %d：%v", len(p1), p1)
	}

	// 变异 & 旧白名单文件
	mutated := n049CloneFiles(base)
	mutated["spec/chain.json"] = n049SetFirstRouteActor(t, base["spec/chain.json"], "group_finance")
	oldWL := n049CloneFiles(mutated)
	oldWL["spec/checks.json"] = n049StripTargetFilter(t, base["spec/checks.json"])

	// P2 同一变异 ＋ 新白名单 ⇒ 恰 1 处，且点名被变异取值
	p2 := n049S19Problems(mutated)
	if len(p2) != 1 {
		t.Fatalf("P2：变异后新白名单应**恰 1 处** S19 报错，实为 %d：%v", len(p2), p2)
	}
	if !strings.Contains(p2[0], "group_finance") {
		t.Errorf("P2：报错应点名被变异的取值 group_finance，实为：%s", p2[0])
	}

	// P3 ★★ 同一变异 ＋ 旧白名单 ⇒ 0 处（静默放行）
	p3 := n049S19Problems(oldWL)
	if len(p3) != 0 {
		t.Fatalf("P3：旧白名单对被变异取值应**静默放行（0 处）**，实为 %d：%v", len(p3), p3)
	}

	// P4 无变异 ＋ 旧白名单 ⇒ 0 处（对照）
	nofilter := n049CloneFiles(base)
	nofilter["spec/checks.json"] = n049StripTargetFilter(t, base["spec/checks.json"])
	p4 := n049S19Problems(nofilter)
	if len(p4) != 0 {
		t.Errorf("P4：无变异下旧白名单应 0 处，实为 %d：%v", len(p4), p4)
	}

	t.Logf("✓ 两侧真引擎反证成立：无变异=0/0 · 同一变异=新白名单 1 处 / 旧白名单 0 处（静默放行）")
}
