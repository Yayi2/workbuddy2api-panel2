// profile.go 上游站点 Profile：国内站（copilot.tencent.com）与国际站（www.workbuddy.ai）。
//
// 站点判据来源：CangShui/workbuddy-gateway 的 upstreamProfile 实现与实测注释——
// 国际站与国内站是**同一套 /v2/plugin/* 协议的两套部署**（实测：auth/state、
// auth/token、login/account、auth/token/refresh、chat/completions 的路径与响应
// 包络完全一致），差异仅在：
//
//  1. 上游域名与 Web Origin（国际站位于腾讯 EdgeOne 国际 CDN）
//  2. 登录 platform 参数（workbuddy-ai 而非 VSCode），登录在浏览器内完成（邮箱/验证码/SSO）
//  3. 等待授权期间 auth/token 轮询返回 code 11217（login ing...）
//  4. 等待窗口更长（浏览器登录慢于扫码：5m → 15m）
//
// 因此本包不为国际站新建一套端点体系，而是把原先硬编码的三处 base（chat/billing/web）
// 与 Origin/UA 收敛成「按账号 region 选 Profile」，所有既有调用点自动跟随。
//
// 兼容性设计（重要）：Client 上的 ChatBaseCN / BillingBaseCN / WebBaseCN 三个历史
// 字段**全部保留**，并在非空时**优先于** Profile 生效。理由是测试广泛用
// `&Client{ChatBaseCN: srv.URL}` 注入 httptest 服务器；保留该优先级可让全部既有
// 测试零改动继续有效，生产路径（三字段为空）才走 Profile。
package upstream

import (
	"strings"
	"time"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
)

// Profile 单个上游站点的全部站点相关参数。
type Profile struct {
	Key   string // 站点标识（与凭据文件 edition 字段同值）："cn" / "intl"
	Label string // 中文展示名（面板/日志）："国内站" / "国际站"

	// ChatBase 聊天 + growth 域基址（copilot.tencent.com / www.workbuddy.ai）。
	ChatBase string
	// BillingBase 计费 + 部分活动域基址（www.codebuddy.cn / www.workbuddy.ai）。
	BillingBase string
	// WebBase Web 成长中心域（任务领奖类接口只在此域提供）。
	WebBase string

	// Origin 请求伪装来源（Origin/Referer 头），指向各站 Web 控制台。
	Origin string
	// Platform auth/state 的 platform 查询参数。
	Platform string
	// ClientUA 出站 User-Agent（可被 Config.Upstream.UserAgent 覆盖）。
	ClientUA string

	// LoginTTL 设备授权等待窗口：国际站需在浏览器内完成邮箱/验证码/SSO 登录，
	// 比扫码慢，故放宽到 15 分钟。
	LoginTTL time.Duration

	// Growth 报告该站点是否提供**成长中心积分任务体系**（本项目的 17 个可自动任务
	// 所依赖的那套 `/v2/activity/growth/*` 语义、连登、猫猫旅行、开学季等）。
	//
	// 国内站为 true；国际站为 false —— 面板据此把国际站账号的「任务/旅行/签到」
	// 按钮置灰并标注 N/A，避免对着不存在的端点盲发请求。
	//
	// 重要辨析：这不是"国际站完全没有 growth 相关的 HTTP 端点"，而是"没有**本网关
	// 实现的那套任务体系**"。实测国际站：
	//
	//	GET  /v2/activity/growth/tasks  → 200，但返回的是**另一套 schema**：
	//	     {task_id, code, title, status}（5 个任务、无 progress/reward 字段），
	//	     而国内站是 {task_code, progress{current,target}, reward_credit, accept_status}。
	//	     本项目 ListTasks 按国内站 schema 解析，对国际站会得到 task_code 为空的条目。
	//	GET  /activity/growth/streak    → 500 internal server error（连登不可用）
	//	GET  /activity/growth/buddy/info→ 200（buddy=null）
	//	POST /activity/growth/redeem    → 400 invalid redemption request
	//	POST /v2/report                 → 400 code=10001（上报通道同样不认这套判据）
	//
	// 故 Growth=false 是准确的：本网关的任务自动化（一键完成/领奖/连登/旅行）对国际站
	// 不适用。**但它不门控余额**——余额由 BalanceAPI 单独表示（国际站可用）。
	Growth bool

	// ModelsAPI 报告 `/console/enterprises/personal/models` 模型清单接口在本站点是否可用。
	//
	// 国内站为 true。
	//
	// 国际站为 false —— 依据（2026-09 用**两个真实有效 token** 实测，非无效 token 推断）：
	//
	//	GET www.workbuddy.ai/console/enterprises/personal/models  → 500 裸 HTML（路由未挂载）
	//	GET www.workbuddy.ai/console/enterprise/personal/models   → 403 {"error":"access_denied",
	//	                                                                 "error_description":"not_authorized"}
	//
	// 注意 403 那条**不是**判断依据：整个 `/console/enterprise/` 前缀（含我刻意编造的
	// 不存在路径）在无有效鉴权时都返回 401/403，属网关级前缀兜底，无法区分"路由存在"。
	// 真正的判据是**复数**路径（与国内站代码里用的完全同一 URL）返回 500 裸 HTML，
	// 而同一 token 打 `/v2/plugin/login/account` 返回 200、打 `/v2/chat/completions`
	// 返回业务包络 code=11128 —— 证明 token 有效、协议族可用，仅此控制台路由缺失。
	// 另经 JWT claims 核对：该 token 为 Keycloak `azp: console`、scope 仅
	// `openid profile offline_access email`，本就不含 models 控制台 API 的角色。
	//
	// 结论：国际站账号无法通过该接口取模型清单，**这不影响对话/余额/保活**。
	// 处理：调用方须按此位跳过国际站账号，否则混挂池下随机选号会让模型页整体报错。
	ModelsAPI bool

	// BalanceAPI 报告计费余额接口（`/v2/billing/meter/get-user-resource`）在本站点是否可用。
	//
	// 两站**均为 true** —— 国际站同域部署了计费域，端点路径与国内站完全一致。
	// 实测（2026-09，真实 token）：返回 code=0 且完整套餐数据
	// （Bonus Pack 250 + Free Plan 100 → 按本项目的 CycleCapacity 口径合计 349）。
	//
	// 为什么单独一个位而不是复用 Growth：**签到与余额在国际站的能力不同**——
	// 签到（daily-checkin）在国际站返回 code=10001「签到活动未开启或已过期」，
	// 而余额完全正常。若用一个 Growth 位同时门控二者，会误伤国际站的余额刷新
	// （面板「刷新」按钮与后台余额轮询都会对着国际站失效）。
	BalanceAPI bool
}

// profileCN 国内站（既有全部行为的原样固化，改动即为回归）。
var profileCN = Profile{
	Key:         auth.RegionCN,
	Label:       "国内站",
	ChatBase:    "https://copilot.tencent.com",
	BillingBase: "https://www.codebuddy.cn",
	WebBase:     "https://www.workbuddy.cn",
	Origin:      "https://www.codebuddy.cn",
	Platform:    "VSCode",
	ClientUA:    "", // 空 = 用官方默认 UA（headers.go 的 defaultWorkBuddyUA）
	LoginTTL:    5 * time.Minute,
	Growth:      true,
	ModelsAPI:   true,
	BalanceAPI:  true,
}

// profileINTL 国际站（www.workbuddy.ai）。
//
// BillingBase/WebBase 同样指向国际站主域：国际站是同域部署，没有独立的
// codebuddy.cn / workbuddy.cn 域，计费与活动端点都在 www.workbuddy.ai 之下。
var profileINTL = Profile{
	Key:         auth.RegionINTL,
	Label:       "国际站",
	ChatBase:    "https://www.workbuddy.ai",
	BillingBase: "https://www.workbuddy.ai",
	WebBase:     "https://www.workbuddy.ai",
	Origin:      "https://www.workbuddy.ai",
	Platform:    "workbuddy-ai",
	ClientUA:    "", // 空 = 用官方默认 UA（headers.go 的 defaultWorkBuddyUA）
	LoginTTL:    15 * time.Minute,
	Growth:      false,
	ModelsAPI:   false, // 该站未挂载 /console/enterprises/personal/models（实测返回裸 HTML 500）
	BalanceAPI:  true,  // 计费域同域部署、路径一致，实测返回 code=0 与完整套餐数据
}

// ProfileFor 按站点标识返回 Profile；空值/未知值回退国内站。
// 口径与 auth.NormalizeRegion 一致，二者共用同一张别名表。
func ProfileFor(region string) *Profile {
	if auth.NormalizeRegion(region) == auth.RegionINTL {
		return &profileINTL
	}
	return &profileCN
}

// ProfileForAuth 按账号所属站点返回 Profile（nil 账号回退国内站）。
func ProfileForAuth(a *auth.Auth) *Profile { return ProfileFor(a.Region()) }

// ProfileForRegionKey 按规范站点标识取 Profile（面板/状态展示用）。
func ProfileForRegionKey(region string) *Profile { return ProfileFor(region) }

// profile 返回账号对应的站点参数。
func (c *Client) profile(a *auth.Auth) *Profile { return ProfileForAuth(a) }

// regionOverride 把「配置覆盖」叠加到 Profile 上，返回副本（不污染包级变量）。
//
// 用途：站点域名/平台参数属于外部事实，可能随上游调整而变化。全部留空时
// 使用内置实测值（零配置可用）；仅在用户显式配置时才改写，便于不改代码修正。
// 永远不覆盖 Key/Label/Growth —— 站点身份与能力边界是代码语义，不是配置项。
func (p *Profile) regionOverride(chatBase, billingBase, webBase, origin, platform, ua string) *Profile {
	cp := *p
	if v := strings.TrimSpace(chatBase); v != "" {
		cp.ChatBase = strings.TrimRight(v, "/")
	}
	if v := strings.TrimSpace(billingBase); v != "" {
		cp.BillingBase = strings.TrimRight(v, "/")
	}
	if v := strings.TrimSpace(webBase); v != "" {
		cp.WebBase = strings.TrimRight(v, "/")
	}
	if v := strings.TrimSpace(origin); v != "" {
		cp.Origin = strings.TrimRight(v, "/")
	}
	if v := strings.TrimSpace(platform); v != "" {
		cp.Platform = v
	}
	if v := strings.TrimSpace(ua); v != "" {
		cp.ClientUA = v
	}
	return &cp
}

// RegionOverrides 单站点的配置覆盖（全部空 = 用内置实测值）。
type RegionOverrides struct {
	ChatBase    string
	BillingBase string
	WebBase     string
	Origin      string
	Platform    string
	UserAgent   string
}

// SetRegionOverrides 注入站点配置覆盖（main 从 config 解析后调用）。
// 空结构 = 全部走内置值；未覆盖的站点保持内置值不受影响。
func (c *Client) SetRegionOverrides(cn, intl RegionOverrides) {
	c.regionMu.Lock()
	defer c.regionMu.Unlock()
	c.overrides = map[string]RegionOverrides{
		auth.RegionCN:   cn,
		auth.RegionINTL: intl,
	}
}

// profileFor 是包内统一入口：先按账号取内置 Profile，再叠加配置覆盖。
// 所有 base 取值必须经此函数，以保证覆盖对全部端点一致生效。
func (c *Client) profileFor(a *auth.Auth) *Profile {
	p := ProfileForAuth(a)
	c.regionMu.RLock()
	ov, ok := c.overrides[p.Key]
	c.regionMu.RUnlock()
	if !ok {
		return p
	}
	return p.regionOverride(ov.ChatBase, ov.BillingBase, ov.WebBase, ov.Origin, ov.Platform, ov.UserAgent)
}

// profileCNWithOverrides 取国内站 Profile（含覆盖）；无账号上下文时用（如统一 UA）。
func (c *Client) profileCNWithOverrides() *Profile {
	c.regionMu.RLock()
	ov, ok := c.overrides[auth.RegionCN]
	c.regionMu.RUnlock()
	if !ok {
		return &profileCN
	}
	return profileCN.regionOverride(ov.ChatBase, ov.BillingBase, ov.WebBase, ov.Origin, ov.Platform, ov.UserAgent)
}

// AllProfiles 返回两个站点的内置 Profile（顺序：国内站、国际站），供面板/文档展示。
func AllProfiles() []Profile { return []Profile{profileCN, profileINTL} }

// IsGrowthRegion 报告某站点是否提供成长中心。
func IsGrowthRegion(region string) bool { return ProfileFor(region).Growth }

// SupportsModelsAPI 报告某站点是否提供模型清单接口。
// 调用方（面板「模型与档位」、路由层 /v1/models 动态拉取）据此跳过
// 不支持该接口的站点账号，避免必然失败。
func SupportsModelsAPI(region string) bool { return ProfileFor(region).ModelsAPI }

// SupportsModelsAPIForAuth 同上，按账号所属站点判断（nil 账号回退国内站）。
func SupportsModelsAPIForAuth(a *auth.Auth) bool {
	return ProfileForAuth(a).ModelsAPI
}

// SupportsBalanceAPI 报告某站点是否提供计费余额接口。
func SupportsBalanceAPI(region string) bool { return ProfileFor(region).BalanceAPI }

// SupportsCheckin 报告某站点是否提供每日签到。
//
// 签到与余额在**国际站的能力不同**，故不能用一个 Growth 位同时门控：
// 国际站签到实测返回 code=10001「签到活动未开启或已过期」，而余额完全正常。
// 这里以 Growth 作为签到可用性的口径（签到属成长中心运营活动），
// 与 SupportsBalanceAPI 分开，使调用方能精确地"跳过签到但照常刷新余额"。
func SupportsCheckin(region string) bool { return ProfileFor(region).Growth }

// ---------------------------------------------------------------------------
// 静态模型清单（按站点）
//
// 国内站有模型清单接口，正常路径走动态查询；这里的两份静态表用于：
//   - 路由层 /v1/models 在上游不可用/无国内站账号时兜底；
//   - 面板「模型与档位」在国际站的唯一数据源（该站无此接口）。
//
// 放在 upstream 包而非 server：站点能力（Profile）与站点模型清单是同一类
// 「站点事实」，集中一处可让 server 与 panel 共用一份、避免两处漂移；
// 也避免 panel → server 的反向依赖。
// ---------------------------------------------------------------------------

// StaticModelsCN 国内站静态模型表（api-reference §5，动态接口失败时的回退）。
var StaticModelsCN = []ModelInfo{
	{ID: "glm-5.2", ContextWindow: 131072},
	{ID: "glm-5.1", ContextWindow: 131072},
	{ID: "glm-5v-turbo", ContextWindow: 131072},
	{ID: "kimi-k2.7", ContextWindow: 131072},
	{ID: "minimax-m3", ContextWindow: 131072},
	{ID: "hy3", ContextWindow: 131072},
	{ID: "hy3-preview", ContextWindow: 131072},
	{ID: "hy3-preview-agent", ContextWindow: 131072},
	{ID: "deepseek-v4-pro", ContextWindow: 131072},
	{ID: "deepseek-v4-flash", ContextWindow: 131072},
}

// StaticModelsINTL 国际站实测可用模型。
//
// 来源：2026-09 用**真实国际站 token** 逐个调用国际站 chat 端点探测所得。
// 国际站不提供模型清单接口（该控制台路由仅国内站挂载，实测裸 HTML 500，
// 详见 Profile.ModelsAPI），故无法动态枚举——这份清单只能靠实测维护。
//
// 与国内站的**关键差异**（务必保留，这是两表不能合并的原因）：
//   - 国际站用 `deepseek-v4.1-flash`（带小版本号），**不是**国内站的
//     `deepseek-v4-pro` / `deepseek-v4-flash`；后者在国际站返回
//     code=11102 `model [...] service info not found`。
//     （实测教训：早期版本照搬国内站命名探测国际站、全部 11102，
//     据此误判"国际站无 deepseek 系"——实际用户一直在用 deepseek-v4.1-flash。）
//   - 国际站另有 `deepseek-v3` 可用；
//   - `hy3-preview` / `hy3-preview-agent` 仅国内站；国际站可用的是 `hy4-preview`。
//
// 该清单只影响"模型发现"体验，不影响任何模型的可用性：
// 网关不做 model 白名单校验，客户端可传任意模型名，由上游决定是否受理
// （与 workbuddy-gateway 的「完全透传」策略一致）。
var StaticModelsINTL = []ModelInfo{
	{ID: "deepseek-v4.1-flash", ContextWindow: 131072},
	{ID: "deepseek-v3", ContextWindow: 131072},
	{ID: "glm-5.2", ContextWindow: 131072},
	{ID: "glm-5.1", ContextWindow: 131072},
	{ID: "glm-5v-turbo", ContextWindow: 131072},
	{ID: "kimi-k2.7", ContextWindow: 131072},
	{ID: "minimax-m3", ContextWindow: 131072},
	{ID: "hy3", ContextWindow: 131072},
	{ID: "hy4-preview", ContextWindow: 131072},
}

// StaticModelsFor 返回指定站点的静态模型清单（空/未知回退国内站）。
func StaticModelsFor(region string) []ModelInfo {
	if auth.NormalizeRegion(region) == auth.RegionINTL {
		return StaticModelsINTL
	}
	return StaticModelsCN
}

// StaticModelIDsFor 返回指定站点的模型 id 列表（顺序即展示顺序）。
func StaticModelIDsFor(region string) []string {
	src := StaticModelsFor(region)
	out := make([]string, 0, len(src))
	for _, m := range src {
		out = append(out, m.ID)
	}
	return out
}
