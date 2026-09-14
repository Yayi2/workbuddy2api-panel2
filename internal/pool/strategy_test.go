package pool

import (
	"testing"
	"time"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
)

// TestNormalizeStrategy 策略标识归一化；空/未知必须回退 weighted（老配置行为不变）。
func TestNormalizeStrategy(t *testing.T) {
	cases := map[string]string{
		"":            StrategyWeighted,
		"  ":          StrategyWeighted,
		"weighted":    StrategyWeighted,
		"WEIGHTED":    StrategyWeighted,
		"bogus":       StrategyWeighted,
		"priority":    StrategyPriority,
		" priority ":  StrategyPriority,
		"prio":        StrategyPriority,
		"order":       StrategyPriority,
		"round_robin": StrategyRoundRobin,
		"round-robin": StrategyRoundRobin,
		"rr":          StrategyRoundRobin,
		"rotate":      StrategyRoundRobin,
	}
	for in, want := range cases {
		if got := NormalizeStrategy(in); got != want {
			t.Errorf("NormalizeStrategy(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestDefaultStrategyIsWeighted 默认必须仍是 weighted —— 既有部署行为不变。
func TestDefaultStrategyIsWeighted(t *testing.T) {
	p := New("")
	if got := p.Strategy(); got != StrategyWeighted {
		t.Errorf("默认策略 = %q, want %q", got, StrategyWeighted)
	}
}

// TestPriorityStrategyUsesFirstAvailable priority：只要队首可用就一直用它。
//
// 这是用户选择的语义（"不耗尽不换号"）：优先号冷却/禁用时才自动用下一个。
func TestPriorityStrategyUsesFirstAvailable(t *testing.T) {
	old := minPickGap
	minPickGap = 0
	defer func() { minPickGap = old }()

	p := New("")
	addRegion(p, "a-first", auth.RegionCN)
	addRegion(p, "b-second", auth.RegionCN)
	addRegion(p, "c-third", auth.RegionCN)
	p.SetStrategy(StrategyPriority)
	// 显式排定顺序。
	if _, ok := p.MoveAccount("a-first", 0); !ok {
		t.Fatal("MoveAccount 失败")
	}
	if _, ok := p.MoveAccount("b-second", 1); !ok {
		t.Fatal("MoveAccount 失败")
	}
	if _, ok := p.MoveAccount("c-third", 2); !ok {
		t.Fatal("MoveAccount 失败")
	}

	// 队首可用：连抽多次必须恒为 a-first（不换号）。
	for i := 0; i < 20; i++ {
		a := p.Pick()
		if a == nil {
			t.Fatal("应有可用账号")
		}
		if a.UID != "a-first" {
			t.Fatalf("第 %d 次抽到 %q，priority 下队首可用时应恒用 a-first", i, a.UID)
		}
	}

	// 队首禁用 → 自动用第二个。
	p.Disable("a-first", "test")
	for i := 0; i < 10; i++ {
		if a := p.Pick(); a == nil || a.UID != "b-second" {
			t.Fatalf("队首禁用后应用 b-second，got %v", a)
		}
	}

	// 第二个也禁用 → 用第三个。
	p.Disable("b-second", "test")
	if a := p.Pick(); a == nil || a.UID != "c-third" {
		t.Fatalf("前两个禁用后应用 c-third，got %v", a)
	}

	// 全禁用 → nil（不参与兜底）。
	p.Disable("c-third", "test")
	if a := p.Pick(); a != nil {
		t.Errorf("全部禁用应返回 nil，got %q", a.UID)
	}
}

// TestPriorityStrategyOrderMatters 顺序改变会改变被选中的账号。
func TestPriorityStrategyOrderMatters(t *testing.T) {
	old := minPickGap
	minPickGap = 0
	defer func() { minPickGap = old }()

	p := New("")
	addRegion(p, "x", auth.RegionCN)
	addRegion(p, "y", auth.RegionCN)
	p.SetStrategy(StrategyPriority)

	// 把 y 提到最前。
	if _, ok := p.MoveAccount("y", 0); !ok {
		t.Fatal("MoveAccount 失败")
	}
	if a := p.Pick(); a == nil || a.UID != "y" {
		t.Fatalf("应优先用 y，got %v", a)
	}

	// 再把 x 提到最前。
	if _, ok := p.MoveAccount("x", 0); !ok {
		t.Fatal("MoveAccount 失败")
	}
	if a := p.Pick(); a == nil || a.UID != "x" {
		t.Fatalf("调整顺序后应优先用 x，got %v", a)
	}
}

// TestRoundRobinRotates round_robin：每次请求换下一个，环形。
func TestRoundRobinRotates(t *testing.T) {
	old := minPickGap
	minPickGap = 0
	defer func() { minPickGap = old }()

	p := New("")
	addRegion(p, "r1", auth.RegionCN)
	addRegion(p, "r2", auth.RegionCN)
	addRegion(p, "r3", auth.RegionCN)
	p.SetStrategy(StrategyRoundRobin)
	// 排定 r1 → r2 → r3。
	p.MoveAccount("r1", 0)
	p.MoveAccount("r2", 1)
	p.MoveAccount("r3", 2)

	got := make([]string, 0, 6)
	for i := 0; i < 6; i++ {
		a := p.Pick()
		if a == nil {
			t.Fatal("应有可用账号")
		}
		got = append(got, a.UID)
	}
	// 6 次应为两轮完整循环。
	want := []string{"r1", "r2", "r3", "r1", "r2", "r3"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("round_robin 序列 = %v, want %v", got, want)
		}
	}
}

// TestRoundRobinSkipsUnavailable 轮到的账号不可用时自动跳到下一个可用。
func TestRoundRobinSkipsUnavailable(t *testing.T) {
	old := minPickGap
	minPickGap = 0
	defer func() { minPickGap = old }()

	p := New("")
	addRegion(p, "s1", auth.RegionCN)
	addRegion(p, "s2", auth.RegionCN)
	addRegion(p, "s3", auth.RegionCN)
	p.SetStrategy(StrategyRoundRobin)
	p.MoveAccount("s1", 0)
	p.MoveAccount("s2", 1)
	p.MoveAccount("s3", 2)
	p.Disable("s2", "test")

	seen := map[string]int{}
	for i := 0; i < 6; i++ {
		a := p.Pick()
		if a == nil {
			t.Fatal("应有可用账号")
		}
		if a.UID == "s2" {
			t.Fatal("已禁用账号不应被选中")
		}
		seen[a.UID]++
	}
	// s2 被跳过，其余两个应各 3 次（均摊）。
	if seen["s1"] != 3 || seen["s3"] != 3 {
		t.Errorf("分布异常: %v（s1/s3 应各 3 次）", seen)
	}
}

// TestWeightedStrategyIgnoresOrder weighted（默认）下顺序不影响选号 —— 关键回归保护。
func TestWeightedStrategyIgnoresOrder(t *testing.T) {
	old := minPickGap
	minPickGap = 0
	defer func() { minPickGap = old }()

	p := New("")
	addRegion(p, "w1", auth.RegionCN)
	addRegion(p, "w2", auth.RegionCN)
	addRegion(p, "w3", auth.RegionCN)
	// 不调用 SetStrategy → 默认 weighted。
	p.SetCredits("w1", 1000)
	p.SetCredits("w2", 1000)
	p.SetCredits("w3", 1000)

	// 即使把某个账号排到最后，weighted 仍会（按权重随机地）用到它。
	p.MoveAccount("w1", 2)
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		if a := p.Pick(); a != nil {
			seen[a.UID] = true
		}
	}
	if !seen["w1"] || !seen["w2"] || !seen["w3"] {
		t.Errorf("weighted 策略下三个账号都应有机会被选中，实际: %v", seen)
	}
}

// TestOrderPersistsAcrossReload 顺序必须持久化（重启后不丢）。
func TestOrderPersistsAcrossReload(t *testing.T) {
	dir := t.TempDir()
	fp := dir + "/state.json"

	p := New(fp)
	addRegion(p, "p1", auth.RegionCN)
	addRegion(p, "p2", auth.RegionCN)
	addRegion(p, "p3", auth.RegionCN)
	// 排成 p3, p1, p2。
	if _, ok := p.MoveAccount("p3", 0); !ok {
		t.Fatal("MoveAccount 失败")
	}
	if _, ok := p.MoveAccount("p1", 1); !ok {
		t.Fatal("MoveAccount 失败")
	}
	if _, ok := p.MoveAccount("p2", 2); !ok {
		t.Fatal("MoveAccount 失败")
	}
	p.Flush()

	// 重新加载（模拟重启）。
	p2 := New(fp)
	p2.SyncToDir([]*auth.Auth{
		{UID: "p1", AccessToken: "t"},
		{UID: "p2", AccessToken: "t"},
		{UID: "p3", AccessToken: "t"},
	})
	got := p2.Order()
	want := []string{"p3", "p1", "p2"}
	if len(got) != 3 {
		t.Fatalf("顺序长度 = %d, want 3", len(got))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("重启后顺序 = %v, want %v", got, want)
		}
	}
}

// TestOrderAppendsNewAccountsDeterministically 未排入顺序的新账号按 uid 升序追加到末尾。
//
// 确定性很重要：若用 map 遍历顺序，新账号每次排的位置都不同，
// 用户精心排好的顺序会被莫名打乱，且不同重启间表现不一致。
func TestOrderAppendsNewAccountsDeterministically(t *testing.T) {
	p := New("")
	addRegion(p, "old1", auth.RegionCN)
	addRegion(p, "old2", auth.RegionCN)
	p.MoveAccount("old2", 0) // old2 优先

	// 新增三个账号（uid 故意乱序加入）。
	addRegion(p, "zzz", auth.RegionCN)
	addRegion(p, "aaa", auth.RegionCN)
	addRegion(p, "mmm", auth.RegionCN)

	got := p.Order()
	want := []string{"old2", "old1", "aaa", "mmm", "zzz"}
	if len(got) != len(want) {
		t.Fatalf("顺序 = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("顺序 = %v, want %v（新账号应按 uid 升序追加）", got, want)
		}
	}
}

// TestMoveAccountClamps 越界索引自动钳制（面板拖拽可能传出界值）。
func TestMoveAccountClamps(t *testing.T) {
	p := New("")
	addRegion(p, "m1", auth.RegionCN)
	addRegion(p, "m2", auth.RegionCN)

	// to 超出上界 → 移到最后。
	if order, ok := p.MoveAccount("m1", 999); !ok || order[len(order)-1] != "m1" {
		t.Errorf("to 越界应钳到最后: %v", order)
	}
	// to 为负 → 移到最前。
	if order, ok := p.MoveAccount("m1", -5); !ok || order[0] != "m1" {
		t.Errorf("to 负数应钳到最前: %v", order)
	}
	// 不存在的 uid → false。
	if _, ok := p.MoveAccount("nope", 0); ok {
		t.Error("不存在的 uid 应返回 false")
	}
}

// TestResetOrder 重置顺序回到 uid 升序。
func TestResetOrder(t *testing.T) {
	p := New("")
	addRegion(p, "s1", auth.RegionCN)
	addRegion(p, "s2", auth.RegionCN)
	p.MoveAccount("s2", 0)
	if got := p.Order(); got[0] != "s2" {
		t.Fatalf("前置条件失败: %v", got)
	}
	got := p.ResetOrder()
	if got[0] != "s1" || got[1] != "s2" {
		t.Errorf("重置后应回到 uid 升序，got %v", got)
	}
}

// TestOrderStrategyWithRegionFilter 策略选号与区域收窄叠加生效。
func TestOrderStrategyWithRegionFilter(t *testing.T) {
	old := minPickGap
	minPickGap = 0
	defer func() { minPickGap = old }()

	p := New("")
	addRegion(p, "cn-a", auth.RegionCN)
	addRegion(p, "intl-a", auth.RegionINTL)
	p.SetStrategy(StrategyPriority)
	// 国内站优先在前。
	p.MoveAccount("cn-a", 0)
	p.MoveAccount("intl-a", 1)

	if a := p.PickInRegion(auth.RegionINTL, nil, ""); a == nil || a.UID != "intl-a" {
		t.Fatalf("国际站收窄应选中 intl-a，got %v", a)
	}
	if a := p.PickInRegion(auth.RegionCN, nil, ""); a == nil || a.UID != "cn-a" {
		t.Fatalf("国内站收窄应选中 cn-a，got %v", a)
	}
}

// TestPriorityFallsBackWhenAllCooling 优先号全冷却时走全冷却兜底（不返回 nil，除非全禁用）。
func TestPriorityFallsBackWhenAllCooling(t *testing.T) {
	p := New("")
	addRegion(p, "c1", auth.RegionCN)
	addRegion(p, "c2", auth.RegionCN)
	p.SetStrategy(StrategyPriority)
	p.MoveAccount("c1", 0)

	// 两个都软冷却：仍应能兜底选出（软冷却允许参与兜底）。
	p.Cooldown("c1", CoolSoft, time.Hour, "429")
	p.Cooldown("c2", CoolSoft, time.Hour, "429")
	a := p.Pick()
	if a == nil {
		t.Fatal("软冷却账号应可参与兜底")
	}
}

// TestPickByUIDBypassesStrategy PickByUID（会话粘性走的就是这条路）**不经过**选号策略。
//
// 这是运行时最容易困惑的一点："我把 A 置顶了，实际却在用 B"——因为该会话此前
// 已绑定到 B，而粘性绑定优先于策略（多轮上下文必须留在同一账号）。
// 此测试把该契约钉死：无论策略如何，PickByUID 取的都是指定 uid。
func TestPickByUIDBypassesStrategy(t *testing.T) {
	old := minPickGap
	minPickGap = 0
	defer func() { minPickGap = old }()

	p := New("")
	addRegion(p, "top", auth.RegionCN)
	addRegion(p, "other", auth.RegionCN)
	p.SetStrategy(StrategyPriority)
	p.MoveAccount("top", 0)

	// 普通选号走策略 → top。
	if a := p.Pick(); a == nil || a.UID != "top" {
		t.Fatalf("策略选号应选 top，got %v", a)
	}
	// PickByUID 直取 other —— 绕过策略，粘性绑定即此语义。
	if a := p.PickByUID("other"); a == nil || a.UID != "other" {
		t.Fatalf("PickByUID 应直取 other（绕过策略），got %v", a)
	}
	if a := p.PickByUID("top"); a == nil || a.UID != "top" {
		t.Fatalf("PickByUID(top) 应返回 top，got %v", a)
	}
}

// TestStrategySwitchIsLive 策略可在线切换且立即影响选号（面板切换走的就是这条）。
func TestStrategySwitchIsLive(t *testing.T) {
	old := minPickGap
	minPickGap = 0
	defer func() { minPickGap = old }()

	p := New("")
	addRegion(p, "k1", auth.RegionCN)
	addRegion(p, "k2", auth.RegionCN)
	p.MoveAccount("k1", 0)

	// priority：恒 k1。
	p.SetStrategy(StrategyPriority)
	for i := 0; i < 5; i++ {
		if a := p.Pick(); a == nil || a.UID != "k1" {
			t.Fatalf("priority 下应恒选 k1，got %v", a)
		}
	}
	// 切到 round_robin：应开始轮换（4 次内必然出现 k2）。
	p.SetStrategy(StrategyRoundRobin)
	seen := map[string]bool{}
	for i := 0; i < 4; i++ {
		if a := p.Pick(); a != nil {
			seen[a.UID] = true
		}
	}
	if !seen["k2"] {
		t.Errorf("切换到 round_robin 后应轮到 k2，seen=%v", seen)
	}
}

// TestOrderSavedEvenInWeighted weighted 下顺序不参与选号，但**仍可保存**（不报错）。
// 语义：允许用户先把顺序排好、再切策略，不要在 weighted 下拒绝排序操作。
func TestOrderSavedEvenInWeighted(t *testing.T) {
	p := New("")
	addRegion(p, "w-a", auth.RegionCN)
	addRegion(p, "w-b", auth.RegionCN)
	if _, ok := p.MoveAccount("w-b", 0); !ok {
		t.Fatal("weighted 下也应允许调整顺序（先排好，切策略后生效）")
	}
	if got := p.Order(); got[0] != "w-b" {
		t.Errorf("顺序应被保存，got %v", got)
	}
	if p.Strategy() != StrategyWeighted {
		t.Errorf("策略不应因排序而改变，got %q", p.Strategy())
	}
}
