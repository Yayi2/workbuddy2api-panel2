// region.go 池的区域维度：区域筛选常量、按区域计数与区域可用性。
//
// 背景：账号池支持国内站（cn）与国际站（intl）账号**混挂**（与参考实现
// workbuddy-gateway 一致，默认全池轮询）。在此之上，本文件提供可选的区域
// 收窄能力，供调用方通过 X-WB-Region 头显式指定"只走某站点"。
//
// 区域判定一律以账号凭证的 edition 为准（auth.Auth.Region()）——凭证是权威源，
// 池内不缓存冗余区域字段参与决策，避免冗余与实际凭证漂移时选错站点。
package pool

import (
	"strings"
	"time"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
)

// RegionFilterAny 区域筛选的"不限"取值（空串的显式别名，供调用方表意清晰）。
const RegionFilterAny = ""

// NormalizeRegionFilter 归一化区域筛选参数：空/未知 → RegionFilterAny（不限，全池）。
//
// 与 auth.NormalizeRegion 的区别（重要）：auth 把未知值回退为 cn（"未标注即
// 历史 CN 账号"的安全默认），而**筛选**语义下未知值必须回退为"不限"——
// 否则一个拼错的 `?region=amtl` 会静默变成"只走国内站"，把国际站账号全部
// 排除在外却毫无提示，排查成本极高。
func NormalizeRegionFilter(region string) string {
	switch strings.ToLower(strings.TrimSpace(region)) {
	case auth.RegionINTL, "international", "global", "workbuddy.ai":
		return auth.RegionINTL
	case auth.RegionCN:
		return auth.RegionCN
	default:
		return RegionFilterAny
	}
}

// isGrowthRegion 报告站点是否提供成长中心（签到/连登/旅行/积分任务）。
//
// 为什么在 pool 内重复一份而不 import upstream：upstream → auth 已是既有依赖，
// 而 pool 不依赖 upstream（避免包环）。且这是**站点能力边界**这一稳定事实，
// 复制成本仅为一行；upstream.Profile.Growth 与本函数由 upstream 侧测试交叉锁定
// （见 profile_test.go 的 TestGrowthRegionBoundary）。
func isGrowthRegion(region string) bool {
	return auth.NormalizeRegion(region) != auth.RegionINTL
}

// RegionCounts 按站点分组的池计数（供 /status 与面板总览透出）。
type RegionCounts struct {
	Region   string `json:"region"`   // "cn" / "intl"
	Label    string `json:"label"`    // "国内站" / "国际站"
	Total    int    `json:"total"`    // 账号总数
	Healthy  int    `json:"healthy"`  // 可用
	Cooling  int    `json:"cooling"`  // 冷却中
	Disabled int    `json:"disabled"` // 已禁用
	Credits  int64  `json:"credits"`  // 可用积分合计
	Growth   bool   `json:"growth"`   // 是否支持成长中心（国际站 false）
}

// CountsByRegion 返回按站点分组的计数（顺序固定：国内站、国际站）。
//
// 固定顺序而非 map：面板与 /status 的展示顺序稳定，便于人眼比对与测试断言。
// 没有账号的站点同样返回零值条目——面板需要展示"国际站：0 个账号"以提示
// 用户该维度存在（否则用户根本不知道可以添加国际站账号）。
func (p *Pool) CountsByRegion() []RegionCounts {
	p.mu.RLock()
	defer p.mu.RUnlock()
	now := time.Now()

	out := []RegionCounts{
		{Region: auth.RegionCN, Label: auth.RegionLabel(auth.RegionCN), Growth: true},
		{Region: auth.RegionINTL, Label: auth.RegionLabel(auth.RegionINTL), Growth: false},
	}
	idx := map[string]int{auth.RegionCN: 0, auth.RegionINTL: 1}
	for _, e := range p.byUID {
		r := e.a.Region()
		i, ok := idx[r]
		if !ok {
			continue // 理论上不可达（Region 只返回两个值）
		}
		out[i].Total++
		out[i].Credits += e.credits
		switch {
		case e.disabled:
			out[i].Disabled++
		case e.healthy(now):
			out[i].Healthy++
		default:
			out[i].Cooling++
		}
	}
	return out
}

// AvailableInRegion 报告指定区域当前是否有可立即服务的账号
// （存在 healthy 且在途未占满者）。region 为空 = 任意区域。
//
// 供 /healthz 的区域维度探活与调用方在"区域收窄失败"时给出准确提示。
func (p *Pool) AvailableInRegion(region string) bool {
	region = NormalizeRegionFilter(region)
	p.mu.RLock()
	defer p.mu.RUnlock()
	now := time.Now()
	for _, e := range p.byUID {
		if region != RegionFilterAny && e.a.Region() != region {
			continue
		}
		if e.healthy(now) && !p.inFlightFull(e) {
			return true
		}
	}
	return false
}

// AvailableUIDsInRegion 返回指定区域当前可用的 uid 列表（供粘性会话路由过滤）。
// region 为空 = 全池可用账号。顺序不保证。
func (p *Pool) AvailableUIDsInRegion(region string) []string {
	region = NormalizeRegionFilter(region)
	p.mu.RLock()
	defer p.mu.RUnlock()
	now := time.Now()
	var out []string
	for uid, e := range p.byUID {
		if region != RegionFilterAny && e.a.Region() != region {
			continue
		}
		if e.healthy(now) {
			out = append(out, uid)
		}
	}
	return out
}

// KnownRegions 返回池中实际存在账号的站点集合（有序：cn、intl）。
// 供面板筛选 chips 只展示"确实有账号"的站点。
func (p *Pool) KnownRegions() []string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	seen := map[string]bool{}
	for _, e := range p.byUID {
		seen[e.a.Region()] = true
	}
	var out []string
	for _, r := range []string{auth.RegionCN, auth.RegionINTL} {
		if seen[r] {
			out = append(out, r)
		}
	}
	return out
}
