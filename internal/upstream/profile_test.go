package upstream

import (
	"net/http"
	"strings"
	"testing"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
)

// TestProfileForRegionMapping 站点标识 → Profile 映射表。
// 别名表必须与 auth.NormalizeRegion 同口径，否则会出现"账号被判为国际站、
// 但 Profile 仍返回国内站域名"的分叉（表现为国际站账号打到 tencent.com 全部 401）。
func TestProfileForRegionMapping(t *testing.T) {
	cases := []struct {
		region     string
		wantKey    string
		wantBase   string
		wantPlat   string
		wantGrowth bool
	}{
		{"cn", auth.RegionCN, "https://copilot.tencent.com", "VSCode", true},
		{"", auth.RegionCN, "https://copilot.tencent.com", "VSCode", true},
		{"unknown", auth.RegionCN, "https://copilot.tencent.com", "VSCode", true},
		{"intl", auth.RegionINTL, "https://www.workbuddy.ai", "workbuddy-ai", false},
		{"international", auth.RegionINTL, "https://www.workbuddy.ai", "workbuddy-ai", false},
		{"global", auth.RegionINTL, "https://www.workbuddy.ai", "workbuddy-ai", false},
		{"workbuddy.ai", auth.RegionINTL, "https://www.workbuddy.ai", "workbuddy-ai", false},
	}
	for _, c := range cases {
		p := ProfileFor(c.region)
		if p.Key != c.wantKey {
			t.Errorf("ProfileFor(%q).Key = %q, want %q", c.region, p.Key, c.wantKey)
		}
		if p.ChatBase != c.wantBase {
			t.Errorf("ProfileFor(%q).ChatBase = %q, want %q", c.region, p.ChatBase, c.wantBase)
		}
		if p.Platform != c.wantPlat {
			t.Errorf("ProfileFor(%q).Platform = %q, want %q", c.region, p.Platform, c.wantPlat)
		}
		if p.Growth != c.wantGrowth {
			t.Errorf("ProfileFor(%q).Growth = %v, want %v", c.region, p.Growth, c.wantGrowth)
		}
	}
}

// TestProfileINTLTruth 国际站实测参数固化（对齐 workbuddy-gateway）。
// 这些值来自参考项目的 upstreamProfile 实测结论，一旦被"顺手改回"就是回归。
func TestProfileINTLTruth(t *testing.T) {
	p := ProfileFor(auth.RegionINTL)
	if p.ChatBase != "https://www.workbuddy.ai" {
		t.Errorf("国际站 ChatBase = %q, want https://www.workbuddy.ai", p.ChatBase)
	}
	if p.Origin != "https://www.workbuddy.ai" {
		t.Errorf("国际站 Origin = %q, want https://www.workbuddy.ai", p.Origin)
	}
	if p.Platform != "workbuddy-ai" {
		t.Errorf("国际站登录 platform = %q, want workbuddy-ai", p.Platform)
	}
	// 国际站是同域部署：计费/活动/Web 都在主域，没有 codebuddy.cn / workbuddy.cn。
	if p.BillingBase != "https://www.workbuddy.ai" {
		t.Errorf("国际站 BillingBase = %q, want www.workbuddy.ai（同域部署）", p.BillingBase)
	}
	if p.WebBase != "https://www.workbuddy.ai" {
		t.Errorf("国际站 WebBase = %q, want www.workbuddy.ai（同域部署）", p.WebBase)
	}
	// 浏览器内登录（邮箱/验证码/SSO）比扫码慢，等待窗口放宽到 15 分钟。
	if p.LoginTTL.String() != "15m0s" {
		t.Errorf("国际站 LoginTTL = %s, want 15m", p.LoginTTL)
	}
	if ProfileFor(auth.RegionCN).LoginTTL.String() != "5m0s" {
		t.Errorf("国内站 LoginTTL = %s, want 5m", ProfileFor(auth.RegionCN).LoginTTL)
	}
}

// TestGrowthRegionBoundary 成长中心归属：仅国内站有（国际站无运营活动）。
// 面板据此把国际站账号的签到/旅行/任务按钮置灰，故这个布尔值是 UI 行为的唯一依据。
func TestGrowthRegionBoundary(t *testing.T) {
	if !IsGrowthRegion(auth.RegionCN) {
		t.Error("国内站应有成长中心")
	}
	if IsGrowthRegion(auth.RegionINTL) {
		t.Error("国际站不应有成长中心")
	}
	if !IsGrowthRegion("") {
		t.Error("空 region 回退国内站，应有成长中心")
	}
}

// TestChatBaseRoutesByRegion 聊天 URL 必须按账号站点分流。
func TestChatBaseRoutesByRegion(t *testing.T) {
	c := New()
	cn := &auth.Auth{UID: "u1", Edition: auth.RegionCN}
	intl := &auth.Auth{UID: "u2", Edition: auth.RegionINTL}

	if got := c.chatBase(cn); got != "https://copilot.tencent.com" {
		t.Errorf("CN chatBase = %q", got)
	}
	if got := c.chatBase(intl); got != "https://www.workbuddy.ai" {
		t.Errorf("INTL chatBase = %q", got)
	}
	if got := c.billingBase(intl); got != "https://www.workbuddy.ai" {
		t.Errorf("INTL billingBase = %q", got)
	}
	if got := c.webBase(intl); got != "https://www.workbuddy.ai" {
		t.Errorf("INTL webBase = %q", got)
	}
	// 未标注 edition 的老账号回退国内站（向后兼容硬约束）。
	legacy := &auth.Auth{UID: "u3"}
	if got := c.chatBase(legacy); got != "https://copilot.tencent.com" {
		t.Errorf("legacy chatBase = %q, want 国内站", got)
	}
}

// TestInjectedBaseTakesPrecedence 注入的 *BaseCN 必须优先于 Profile。
//
// 这是让全部既有测试零改动继续有效的关键：测试用 ChatBaseCN=srv.URL 指向
// httptest 假上游。若 Profile 抢在注入值之前，所有既有测试都会打到真实腾讯域名。
func TestInjectedBaseTakesPrecedence(t *testing.T) {
	c := &Client{ChatBaseCN: "http://fake.test", BillingBaseCN: "http://fake.test", WebBaseCN: "http://fake.test"}
	intl := &auth.Auth{UID: "u2", Edition: auth.RegionINTL}
	if got := c.chatBase(intl); got != "http://fake.test" {
		t.Errorf("chatBase = %q, 注入值应优先于 Profile", got)
	}
	if got := c.billingBase(intl); got != "http://fake.test" {
		t.Errorf("billingBase = %q, 注入值应优先", got)
	}
	if got := c.webBase(intl); got != "http://fake.test" {
		t.Errorf("webBase = %q, 注入值应优先", got)
	}
}

// TestHeadersOriginByRegion Origin/Referer 必须与目标站点同站。
// 发到国际站的请求带 codebuddy.cn 的 Origin 会被判跨站调用而拒绝。
func TestHeadersOriginByRegion(t *testing.T) {
	c := New()
	cn := &auth.Auth{UID: "u1", AccessToken: "at", Edition: auth.RegionCN}
	intl := &auth.Auth{UID: "u2", AccessToken: "at", Edition: auth.RegionINTL}

	// CommonHeaders
	reqCN, _ := http.NewRequest(http.MethodPost, "https://x/", nil)
	c.CommonHeaders(reqCN, cn)
	if got := reqCN.Header.Get("Origin"); got != "https://www.codebuddy.cn" {
		t.Errorf("CN Origin = %q", got)
	}
	if got := reqCN.Header.Get("Referer"); got != "https://www.codebuddy.cn/" {
		t.Errorf("CN Referer = %q", got)
	}

	reqIntl, _ := http.NewRequest(http.MethodPost, "https://x/", nil)
	c.CommonHeaders(reqIntl, intl)
	if got := reqIntl.Header.Get("Origin"); got != "https://www.workbuddy.ai" {
		t.Errorf("INTL Origin = %q, want https://www.workbuddy.ai", got)
	}
	if got := reqIntl.Header.Get("Referer"); got != "https://www.workbuddy.ai/" {
		t.Errorf("INTL Referer = %q", got)
	}

	// ChatHeaders 沿用 common 的站点 Origin，且仍不得携带 refresh token。
	reqChat, _ := http.NewRequest(http.MethodPost, "https://x/", nil)
	c.ChatHeaders(reqChat, intl)
	if got := reqChat.Header.Get("Origin"); got != "https://www.workbuddy.ai" {
		t.Errorf("INTL chat Origin = %q", got)
	}
	if got := reqChat.Header.Get("X-Refresh-Token"); got != "" {
		t.Errorf("chat 请求不得携带 X-Refresh-Token, got %q", got)
	}
	if got := reqChat.Header.Get("X-Product"); got != "SaaS" {
		t.Errorf("X-Product = %q, want SaaS", got)
	}
}

// TestRegionOverrides 配置覆盖：显式配置生效（去尾斜杠），未配置项保持内置值，
// 且覆盖不得篡改站点身份（Key/Label/Growth）。
func TestRegionOverrides(t *testing.T) {
	c := New()
	c.SetRegionOverrides(RegionOverrides{}, RegionOverrides{
		ChatBase: "https://intl-mirror.example/",
		Platform: "custom-platform",
	})
	intl := &auth.Auth{UID: "u2", Edition: auth.RegionINTL}

	if got := c.chatBase(intl); got != "https://intl-mirror.example" {
		t.Errorf("覆盖后 chatBase = %q（应去尾斜杠）", got)
	}
	// 未覆盖项保持内置值。
	if got := c.webBase(intl); got != "https://www.workbuddy.ai" {
		t.Errorf("未覆盖的 webBase = %q, want 内置值", got)
	}
	ov := c.profileFor(intl)
	if ov.Platform != "custom-platform" {
		t.Errorf("覆盖 platform = %q", ov.Platform)
	}
	// 站点身份与能力边界不受配置影响。
	if ov.Key != auth.RegionINTL || ov.Label != "国际站" || ov.Growth {
		t.Errorf("覆盖不应改变站点身份/能力: key=%q label=%q growth=%v", ov.Key, ov.Label, ov.Growth)
	}
	// 国内站不受国际站覆盖影响。
	cn := &auth.Auth{UID: "u1", Edition: auth.RegionCN}
	if got := c.chatBase(cn); got != "https://copilot.tencent.com" {
		t.Errorf("国内站被国际站覆盖污染: %q", got)
	}
}

// TestUserAgentOverrideStillWins 全局 UA 覆盖（issue #42）优先级不变。
func TestUserAgentOverrideStillWins(t *testing.T) {
	c := New()
	c.UserAgent = "CustomUA/9.9"
	if got := c.userAgent(&auth.Auth{UID: "u1", Edition: auth.RegionINTL}); got != "CustomUA/9.9" {
		t.Errorf("userAgent = %q, want CustomUA/9.9", got)
	}
	// 未覆盖时按站点 Profile 取（当前两站内置 UA 相同）。
	c2 := New()
	if got := c2.userAgent(&auth.Auth{UID: "u1", Edition: auth.RegionINTL}); got != clientUA {
		t.Errorf("userAgent = %q, want %q", got, clientUA)
	}
}

// TestAllProfiles 两个站点均在展示清单里（面板配置页展示用）。
func TestAllProfiles(t *testing.T) {
	ps := AllProfiles()
	if len(ps) != 2 {
		t.Fatalf("AllProfiles len = %d, want 2", len(ps))
	}
	if ps[0].Key != auth.RegionCN || ps[1].Key != auth.RegionINTL {
		t.Errorf("顺序应为 [cn, intl], got [%s, %s]", ps[0].Key, ps[1].Key)
	}
	for _, p := range ps {
		if !strings.HasPrefix(p.ChatBase, "https://") {
			t.Errorf("%s ChatBase 必须为 https: %q", p.Key, p.ChatBase)
		}
	}
}
