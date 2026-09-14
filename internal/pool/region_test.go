package pool

import (
	"testing"
	"time"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
)

// addRegion 把一个带站点标识的账号放入池中。
func addRegion(p *Pool, uid, region string) {
	p.Add(&auth.Auth{UID: uid, AccessToken: "at-" + uid, Edition: region})
}

// TestNormalizeRegionFilter 筛选参数归一化：未知值必须回退"不限"而非国内站。
//
// 这是与 auth.NormalizeRegion 的**有意分歧**：账号解析时未知值回退 cn 是
// "历史账号"的安全默认；但筛选时若也回退 cn，一个拼错的 ?region=amtl 会
// 静默把国际站账号全部排除，且没有任何提示——用户只会看到"没有可用账号"。
func TestNormalizeRegionFilter(t *testing.T) {
	intl := []string{"intl", "INTL", " intl ", "international", "global", "workbuddy.ai"}
	for _, s := range intl {
		if got := NormalizeRegionFilter(s); got != auth.RegionINTL {
			t.Errorf("NormalizeRegionFilter(%q) = %q, want %q", s, got, auth.RegionINTL)
		}
	}
	cn := []string{"cn", "CN", " cn "}
	for _, s := range cn {
		if got := NormalizeRegionFilter(s); got != auth.RegionCN {
			t.Errorf("NormalizeRegionFilter(%q) = %q, want %q", s, got, auth.RegionCN)
		}
	}
	// 空/未知 → 不限（RegionFilterAny，即空串）。
	for _, s := range []string{"", "  ", "amtl", "bogus", "tencent", "xyz"} {
		if got := NormalizeRegionFilter(s); got != RegionFilterAny {
			t.Errorf("NormalizeRegionFilter(%q) = %q, want %q（未知应回退为不限）", s, got, RegionFilterAny)
		}
	}
}

// TestPickInRegionFiltersCandidates 区域限定选号：只在本区域账号中选。
func TestPickInRegionFiltersCandidates(t *testing.T) {
	// 关闭防撞号窗口：minPickGap 会刻意压制"紧循环内反复命中同一账号"，
	// 而本测试关心的是**区域过滤**是否正确，不是分布均匀性。若不关闭，
	// "不限区域"分支在紧循环下会被抗撞号逻辑长期压到同一账号上，
	// 看起来像区域过滤失效，实则是两个正交机制在互相干扰。
	oldGap := minPickGap
	minPickGap = 0
	defer func() { minPickGap = oldGap }()

	p := New("")
	addRegion(p, "cn-1", auth.RegionCN)
	addRegion(p, "cn-2", auth.RegionCN)
	addRegion(p, "intl-1", auth.RegionINTL)

	// 反复抽取：国际站筛选必须恒中 intl-1。
	for i := 0; i < 30; i++ {
		a := p.PickInRegion(auth.RegionINTL, nil, "")
		if a == nil {
			t.Fatal("国际站应有可用账号")
		}
		if a.Region() != auth.RegionINTL {
			t.Fatalf("第 %d 次抽到 %q（站点 %q），应为国际站", i, a.UID, a.Region())
		}
	}
	// 国内站筛选只应命中 cn-*。
	for i := 0; i < 30; i++ {
		a := p.PickInRegion(auth.RegionCN, nil, "")
		if a == nil {
			t.Fatal("国内站应有可用账号")
		}
		if a.Region() != auth.RegionCN {
			t.Fatalf("第 %d 次抽到 %q（站点 %q），应为国内站", i, a.UID, a.Region())
		}
	}
	// 不限（空串）：两站都应有机会被选中（混挂轮询语义）。
	counts := map[string]int{}
	for i := 0; i < 400; i++ {
		if a := p.PickInRegion("", nil, ""); a != nil {
			counts[a.Region()]++
		}
	}
	if counts[auth.RegionINTL] == 0 {
		t.Errorf("不限区域时国际站账号应有机会被选中（混挂轮询），分布=%v", counts)
	}
	if counts[auth.RegionCN] == 0 {
		t.Errorf("不限区域时国内站账号应有机会被选中，分布=%v", counts)
	}
}

// TestPickInRegionNoCandidateReturnsNil 区域内无账号时返回 nil（不跨区域回落）。
//
// 语义要点：区域收窄是调用方的**强约束**（"只走国际站"），跨区域回落会把请求
// 发到用户未预期的计费主体。故无候选即返回 nil，由调用方决定是否放宽并提示。
func TestPickInRegionNoCandidateReturnsNil(t *testing.T) {
	p := New("")
	addRegion(p, "cn-1", auth.RegionCN)

	if a := p.PickInRegion(auth.RegionINTL, nil, ""); a != nil {
		t.Errorf("池中无国际站账号，应返回 nil，got %q", a.UID)
	}
	// 国内站仍正常。
	if a := p.PickInRegion(auth.RegionCN, nil, ""); a == nil {
		t.Error("国内站应有可用账号")
	}
}

// TestPickInRegionCoolingDoesNotCrossRegion 国际站账号全冷却时，区域收窄不得
// 回落到国内站账号（兜底路径同样受区域约束）。
func TestPickInRegionCoolingDoesNotCrossRegion(t *testing.T) {
	p := New("")
	addRegion(p, "cn-1", auth.RegionCN)
	addRegion(p, "intl-1", auth.RegionINTL)

	p.Cooldown("intl-1", CoolSoft, time.Hour, "429 rate limit")
	// 国际站账号在冷却：区域收窄下"无 healthy 候选"，兜底也只在 intl 内找，
	// 而唯一的 intl 账号处于软冷却 → 兜底可选中它（CoolSoft 允许参与兜底）。
	a := p.PickInRegion(auth.RegionINTL, nil, "")
	if a == nil {
		t.Fatal("国际站软冷却账号应可参与兜底")
	}
	if a.Region() != auth.RegionINTL {
		t.Errorf("区域收窄下兜底抽到 %q（站点 %q），不得跨区域", a.UID, a.Region())
	}

	// 国际站账号被禁用后：兜底也不得选中它，且不得回落到国内站 → nil。
	p.Disable("intl-1", "test disable")
	if a := p.PickInRegion(auth.RegionINTL, nil, ""); a != nil {
		t.Errorf("国际站账号已禁用，区域收窄应返回 nil，got %q（站点 %q）", a.UID, a.Region())
	}
}

// TestPickInRegionExcludesTried 区域收窄与请求级轮换（tried）叠加生效。
func TestPickInRegionExcludesTried(t *testing.T) {
	p := New("")
	addRegion(p, "intl-1", auth.RegionINTL)
	addRegion(p, "intl-2", auth.RegionINTL)

	tried := map[string]bool{"intl-1": true}
	for i := 0; i < 20; i++ {
		a := p.PickInRegion(auth.RegionINTL, tried, "")
		if a == nil {
			t.Fatal("应能抽到未被 tried 的国际站账号")
		}
		if a.UID == "intl-1" {
			t.Fatal("tried 中的账号不应被选中")
		}
	}
	// 两个都被 tried → nil（区域内无候选）。
	tried["intl-2"] = true
	if a := p.PickInRegion(auth.RegionINTL, tried, ""); a != nil {
		t.Errorf("区域候选全被 tried 排除，应返回 nil，got %q", a.UID)
	}
}

// TestCountsByRegion 区域分组计数：顺序固定、空站点也返回零值条目。
func TestCountsByRegion(t *testing.T) {
	p := New("")
	// 全空池：两站条目都在（面板需展示"国际站：0 个账号"以提示该维度存在）。
	rc := p.CountsByRegion()
	if len(rc) != 2 {
		t.Fatalf("CountsByRegion len = %d, want 2", len(rc))
	}
	if rc[0].Region != auth.RegionCN || rc[1].Region != auth.RegionINTL {
		t.Errorf("顺序应为 [cn, intl], got [%s, %s]", rc[0].Region, rc[1].Region)
	}
	if rc[0].Total != 0 || rc[1].Total != 0 {
		t.Error("空池各站计数应为 0")
	}
	if !rc[0].Growth || rc[1].Growth {
		t.Errorf("Growth 边界错误: cn=%v intl=%v", rc[0].Growth, rc[1].Growth)
	}

	// 加入账号并制造各种状态。
	addRegion(p, "cn-1", auth.RegionCN)
	addRegion(p, "cn-2", auth.RegionCN)
	addRegion(p, "intl-1", auth.RegionINTL)
	p.SetCredits("cn-1", 100)
	p.SetCredits("cn-2", 50)
	p.SetCredits("intl-1", 75)
	p.Cooldown("cn-2", CoolSoft, time.Hour, "429")
	p.Disable("intl-1", "manual")

	rc = p.CountsByRegion()
	cn, intl := rc[0], rc[1]

	if cn.Total != 2 || cn.Healthy != 1 || cn.Cooling != 1 || cn.Disabled != 0 {
		t.Errorf("国内站计数 = total:%d healthy:%d cooling:%d disabled:%d, want 2/1/1/0",
			cn.Total, cn.Healthy, cn.Cooling, cn.Disabled)
	}
	if cn.Credits != 150 {
		t.Errorf("国内站积分合计 = %d, want 150", cn.Credits)
	}
	if intl.Total != 1 || intl.Healthy != 0 || intl.Disabled != 1 {
		t.Errorf("国际站计数 = total:%d healthy:%d disabled:%d, want 1/0/1",
			intl.Total, intl.Healthy, intl.Disabled)
	}
	if intl.Credits != 75 {
		t.Errorf("国际站积分合计 = %d, want 75", intl.Credits)
	}
	// 标签与站点能力。
	if cn.Label != "国内站" || intl.Label != "国际站" {
		t.Errorf("标签错误: cn=%q intl=%q", cn.Label, intl.Label)
	}
}

// TestAvailableInRegion 区域可用性判定。
func TestAvailableInRegion(t *testing.T) {
	p := New("")
	addRegion(p, "cn-1", auth.RegionCN)

	if !p.AvailableInRegion(auth.RegionCN) {
		t.Error("国内站应有可用账号")
	}
	if p.AvailableInRegion(auth.RegionINTL) {
		t.Error("无国际站账号时不应报告可用")
	}
	// 不限区域：任一站有即可用。
	if !p.AvailableInRegion("") {
		t.Error("不限区域时应报告可用")
	}
	// 冷却后不可用。
	p.Cooldown("cn-1", CoolSoft, time.Hour, "429")
	if p.AvailableInRegion(auth.RegionCN) {
		t.Error("冷却中不应报告可用")
	}
}

// TestAvailableUIDsInRegion 区域可用 uid 列表（供粘性会话过滤）。
func TestAvailableUIDsInRegion(t *testing.T) {
	p := New("")
	addRegion(p, "cn-1", auth.RegionCN)
	addRegion(p, "cn-2", auth.RegionCN)
	addRegion(p, "intl-1", auth.RegionINTL)
	p.Cooldown("cn-2", CoolSoft, time.Hour, "429")

	cn := p.AvailableUIDsInRegion(auth.RegionCN)
	if len(cn) != 1 || cn[0] != "cn-1" {
		t.Errorf("国内站可用 = %v, want [cn-1]", cn)
	}
	intl := p.AvailableUIDsInRegion(auth.RegionINTL)
	if len(intl) != 1 || intl[0] != "intl-1" {
		t.Errorf("国际站可用 = %v, want [intl-1]", intl)
	}
	if all := p.AvailableUIDsInRegion(""); len(all) != 2 {
		t.Errorf("不限区域可用 = %v, want 2 个", all)
	}
}

// TestKnownRegions 只返回实际有账号的站点（面板筛选 chips 用）。
func TestKnownRegions(t *testing.T) {
	p := New("")
	if got := p.KnownRegions(); len(got) != 0 {
		t.Errorf("空池 KnownRegions = %v, want 空", got)
	}
	addRegion(p, "intl-1", auth.RegionINTL)
	if got := p.KnownRegions(); len(got) != 1 || got[0] != auth.RegionINTL {
		t.Errorf("KnownRegions = %v, want [intl]", got)
	}
	addRegion(p, "cn-1", auth.RegionCN)
	got := p.KnownRegions()
	if len(got) != 2 || got[0] != auth.RegionCN || got[1] != auth.RegionINTL {
		t.Errorf("KnownRegions = %v, want [cn intl]（顺序固定）", got)
	}
}

// TestStatusCarriesRegion 账号状态透出站点信息（面板表格区域列的数据源）。
func TestStatusCarriesRegion(t *testing.T) {
	p := New("")
	addRegion(p, "cn-1", auth.RegionCN)
	addRegion(p, "intl-1", auth.RegionINTL)

	st, ok := p.Status("cn-1")
	if !ok {
		t.Fatal("cn-1 应存在")
	}
	if st.Region != auth.RegionCN || st.RegionLabel != "国内站" || !st.RegionGrowth {
		t.Errorf("CN status = region:%q label:%q growth:%v", st.Region, st.RegionLabel, st.RegionGrowth)
	}

	st2, ok := p.Status("intl-1")
	if !ok {
		t.Fatal("intl-1 应存在")
	}
	if st2.Region != auth.RegionINTL || st2.RegionLabel != "国际站" {
		t.Errorf("INTL status = region:%q label:%q", st2.Region, st2.RegionLabel)
	}
	// 国际站无成长中心 → 面板据此把任务/旅行按钮置灰。
	if st2.RegionGrowth {
		t.Error("国际站 RegionGrowth 应为 false")
	}

	// List 同样携带区域。
	for _, s := range p.List() {
		if s.Region == "" {
			t.Errorf("List 中 %s 缺少 region", s.UID)
		}
	}
}

// TestLegacyAccountWithoutEditionIsCN 老账号（无 edition）在池中视为国内站，
// 且不影响区域筛选行为——这是既有部署的向后兼容硬约束。
func TestLegacyAccountWithoutEditionIsCN(t *testing.T) {
	p := New("")
	p.Add(&auth.Auth{UID: "legacy", AccessToken: "at"}) // Edition 留空

	st, ok := p.Status("legacy")
	if !ok {
		t.Fatal("legacy 应存在")
	}
	if st.Region != auth.RegionCN {
		t.Errorf("老账号 region = %q, want cn", st.Region)
	}
	if a := p.PickInRegion(auth.RegionCN, nil, ""); a == nil {
		t.Error("老账号应参与国内站筛选")
	}
	if a := p.PickInRegion(auth.RegionINTL, nil, ""); a != nil {
		t.Errorf("老账号不应参与国际站筛选，got %q", a.UID)
	}
}
