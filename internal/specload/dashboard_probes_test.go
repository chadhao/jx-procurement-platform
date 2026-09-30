package specload

// dashboard.json [D1]–[D4] 负向探针（内存变异，不落盘第二份 spec）。

import "testing"

func TestDashboardProbes(t *testing.T) {
	runProbes(t, []probe{
		{
			name:   "D1-缺看板16",
			expect: "[D1]",
			mutate: func(t *testing.T, files map[string][]byte) {
				mutateJSON(t, files, "spec/dashboard.json", func(m map[string]any) {
					boards, _ := m["dashboards"].([]any)
					kept := make([]any, 0, len(boards))
					for _, b := range boards {
						bm, _ := b.(map[string]any)
						if id, _ := bm["id"].(float64); int(id) == 16 {
							continue
						}
						kept = append(kept, b)
					}
					m["dashboards"] = kept
				})
			},
		},
		{
			name:   "D1-未登记看板id",
			expect: "[D1]",
			mutate: func(t *testing.T, files map[string][]byte) {
				mutateJSON(t, files, "spec/dashboard.json", func(m map[string]any) {
					boards, _ := m["dashboards"].([]any)
					if len(boards) > 0 {
						boards[0].(map[string]any)["id"] = float64(99)
						m["dashboards"] = boards
					}
				})
			},
		},
		{
			name:   "D2-r1纪律为空",
			expect: "[D2]",
			mutate: func(t *testing.T, files map[string][]byte) {
				mutateJSON(t, files, "spec/dashboard.json", func(m map[string]any) {
					gr, _ := m["global_rules"].(map[string]any)
					gr["r1_not_connected_shows_gap"] = ""
				})
			},
		},
		{
			name:   "D3-source_status越域",
			expect: "[D3]",
			mutate: func(t *testing.T, files map[string][]byte) {
				mutateJSON(t, files, "spec/dashboard.json", func(m map[string]any) {
					boards, _ := m["dashboards"].([]any)
					if len(boards) > 0 {
						boards[0].(map[string]any)["source_status"] = "unknown"
					}
				})
			},
		},
		{
			name:   "D4-指标key重复",
			expect: "[D4]",
			mutate: func(t *testing.T, files map[string][]byte) {
				mutateJSON(t, files, "spec/dashboard.json", func(m map[string]any) {
					boards, _ := m["dashboards"].([]any)
					for _, b := range boards {
						bm, _ := b.(map[string]any)
						inds, _ := bm["indicators"].([]any)
						if len(inds) >= 2 {
							first, _ := inds[0].(map[string]any)
							second, _ := inds[1].(map[string]any)
							second["key"] = first["key"]
							break
						}
					}
				})
			},
		},
		{
			name:   "D4-缺formula",
			expect: "[D4]",
			mutate: func(t *testing.T, files map[string][]byte) {
				mutateJSON(t, files, "spec/dashboard.json", func(m map[string]any) {
					boards, _ := m["dashboards"].([]any)
					for _, b := range boards {
						bm, _ := b.(map[string]any)
						inds, _ := bm["indicators"].([]any)
						if len(inds) > 0 {
							inds[0].(map[string]any)["formula"] = ""
							break
						}
					}
				})
			},
		},
	})
}

// TestDashboardMissingFile 缺文件必须拒启（不是静默给空看板）。
func TestDashboardMissingFile(t *testing.T) {
	base := loadReal(t).ProblemsRaw
	files := cloneFiles(base)
	delete(files, "spec/dashboard.json")
	_, err := loadFiles(files)
	if err == nil {
		t.Fatal("缺 spec/dashboard.json 应拒启")
	}
}

// TestDashboardLoaded 真 spec：4 看板齐、r1/r3 可用（正向冒烟）。
func TestDashboardLoaded(t *testing.T) {
	b := loadReal(t)
	if b.Dashboard == nil {
		t.Fatal("Dashboard 未加载")
	}
	if len(b.Dashboard.Dashboards) != 4 {
		t.Fatalf("看板数 = %d, want 4", len(b.Dashboard.Dashboards))
	}
	if b.Dashboard.GlobalRules.R3ZeroNeedsNonemptyGuard == "" {
		t.Fatal("r3 纪律为空 —— 应恒为 0 类指标的前置断言失传")
	}
	if b.Dashboard.Board(16) == nil || b.Dashboard.Board(16).Label == "" {
		t.Fatal("看板 16 缺失或无 label")
	}
	if b.Dashboard.Board(13) == nil || b.Dashboard.Board(13).SourceStatus != "not_enabled" {
		t.Fatal("看板 13 source_status 应为 not_enabled（L12 未启用）")
	}
	// indicator key 在看板内唯一（加载校验已断言；此处再钉 spec 真值形态）
	for _, bd := range b.Dashboard.Dashboards {
		seen := map[string]bool{}
		for _, ind := range bd.Indicators {
			if seen[ind.Key] {
				t.Fatalf("看板 %d 指标 key 重复: %s", bd.ID, ind.Key)
			}
			seen[ind.Key] = true
		}
	}
}

// TestDashboardD6Probes [D6]：connected 看板的每个指标必须声明 availability_guard。
//
//	变异① 16 置 connected + 指标缺守卫 ⇒ 必报 [D6]（把 r6 的口头判据变成拦得住人的东西）；
//	变异② 16 置 connected + 全指标带守卫 ⇒ 必须通过（守卫齐全即可推进，不等数据到齐）。
func TestDashboardD6Probes(t *testing.T) {
	runProbes(t, []probe{
		{
			name:   "D6-connected缺守卫",
			expect: "[D6]",
			mutate: func(t *testing.T, files map[string][]byte) {
				mutateJSON(t, files, "spec/dashboard.json", func(m map[string]any) {
					boards, _ := m["dashboards"].([]any)
					for _, b := range boards {
						bm, _ := b.(map[string]any)
						if id, _ := bm["id"].(float64); int(id) == 16 {
							bm["source_status"] = "connected" // 现 spec 无 connected ⇒ 手工造
							// 指标不加 availability_guard ⇒ 必须报 [D6]
						}
					}
				})
			},
		},
	})
}

// TestDashboardD6GuardedPasses 正向：connected + 每指标带 availability_guard ⇒ 加载通过
// （r6 的语义：判据是「守卫齐全」而不是「数据到齐」—— 带齐守卫就允许推进）。
func TestDashboardD6GuardedPasses(t *testing.T) {
	base := loadReal(t).ProblemsRaw
	files := cloneFiles(base)
	mutateJSON(t, files, "spec/dashboard.json", func(m map[string]any) {
		boards, _ := m["dashboards"].([]any)
		for _, b := range boards {
			bm, _ := b.(map[string]any)
			if id, _ := bm["id"].(float64); int(id) == 16 {
				bm["source_status"] = "connected"
				inds, _ := bm["indicators"].([]any)
				for _, i := range inds {
					im, _ := i.(map[string]any)
					im["availability_guard"] = "源行数 > 0，否则显示「数据未接入」"
				}
			}
		}
	})
	if _, err := loadFiles(files); err != nil {
		t.Fatalf("connected + 守卫齐全应通过（r6 判据＝守卫齐全，不是数据到齐）: %v", err)
	}
}
