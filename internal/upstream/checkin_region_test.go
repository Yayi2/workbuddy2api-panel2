package upstream

import (
	"testing"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
)

// TestCheckinBalanceCapabilitySplit 签到与余额的站点能力**必须分开**。
//
// 事实依据（2026-09 真实 token 实测国际站）：
//
//	POST /v2/billing/meter/daily-checkin        → code=10001「签到活动未开启或已过期」
//	POST /v2/billing/meter/get-user-resource    → code=0 + 完整套餐数据（可正常合计余额）
//
// 即国际站**余额可用、签到不可用**。早期实现用一个 Growth 位同时门控二者，
// 结果是国际站的余额刷新（面板「刷新」按钮、后台余额轮询、登录后首次查询）
// 被一并跳过——账号积分永远显示为旧值/未知。
//
// 这个测试把这个分歧钉死，防止有人把两个能力位又合并回去。
func TestCheckinBalanceCapabilitySplit(t *testing.T) {
	// 国内站：两者都可用。
	if !SupportsCheckin(auth.RegionCN) {
		t.Error("国内站应支持签到")
	}
	if !SupportsBalanceAPI(auth.RegionCN) {
		t.Error("国内站应支持余额查询")
	}

	// 国际站：签到不可用（活动未开），但**余额必须可用**。
	if SupportsCheckin(auth.RegionINTL) {
		t.Error("国际站不应报告支持签到（daily-checkin 返回 code=10001）")
	}
	if !SupportsBalanceAPI(auth.RegionINTL) {
		t.Error("国际站必须支持余额查询（实测 code=0 + 完整套餐数据）；" +
			"若误判为不支持，面板刷新与后台轮询会对国际站账号全部失效")
	}

	// 空/未知回退国内站口径。
	if !SupportsCheckin("") || !SupportsBalanceAPI("") {
		t.Error("空 region 回退国内站，两者都应支持")
	}
}

// TestBalanceAPINotGatedByGrowth 显式断言"余额不看 Growth 位"。
// 这是本组能力位存在的核心原因，单独一条测试防止语义漂移。
func TestBalanceAPINotGatedByGrowth(t *testing.T) {
	for _, region := range []string{auth.RegionCN, auth.RegionINTL, "", "unknown"} {
		if ProfileFor(region).Growth != SupportsCheckin(region) {
			t.Errorf("[%s] SupportsCheckin 应与 Growth 同口径", region)
		}
		// 关键：国际站 Growth=false 但 BalanceAPI=true，二者不得绑定。
		if region == auth.RegionINTL {
			p := ProfileFor(region)
			if p.Growth {
				t.Fatal("前提失效：国际站 Growth 应为 false")
			}
			if !p.BalanceAPI {
				t.Fatal("国际站 BalanceAPI 必须为 true——余额不被 Growth 门控")
			}
		}
	}
}
