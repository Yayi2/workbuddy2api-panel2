// Package headers 构造四类上游请求头（common / chat / billing / refresh）。
// 规则来自 docs/api-reference.md §0/§4/§6。
//
// 站点相关头（Origin/Referer/UA）自引入国际站后**按账号 region 取 Profile**：
// 国内站伪装来源 www.codebuddy.cn，国际站 www.workbuddy.ai。发到错误站点的
// Origin 会被上游判为跨站调用而拒绝，故这里必须与目标域名同站。
//
// 本文件是「官方 v1.6.3 客户端指纹体系」与「本地站点感知」的合并结果：
//   - 官方的 ClientVersion/CliVersion/ClientName/DeviceToken/PassthroughIP 全部保留；
//   - 本地把 UA 与 Origin 改为按站点取值（siteUA / originRefererFor）。
package upstream

import (
	"net/http"
	"strings"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
)

const (
	// defaultClientVersion 出站 WorkBuddy 客户端版本段（UA 的 `WorkBuddy/<ver>` 与
	// 白名单头组的 X-IDE-Version）。对齐官方 WorkBuddy Desktop 分发包版本（5.5.4）。
	// config upstream.client_version 可覆盖（空 = 内置默认）。
	defaultClientVersion = "5.5.4"
	// defaultCliVersion 出站 UA 中 `CLI/<ver>` 段版本。对齐官方内置 CLI（2.137.1）。
	// config upstream.cli_version 可覆盖（空 = 内置默认）。
	defaultCliVersion = "2.137.1"

	// clientUALegacy 站点未提供 UA 时的兜底（站点 UA 由 Profile.ClientUA 决定）。
	clientUALegacy = "CLI/2.63.2 CodeBuddy/2.63.2"

	// originRefererCN 国内站伪装来源（保留常量：既有测试与语义以它为基准）。
	originRefererCN = "https://www.codebuddy.cn"
)

// originRefererFor 返回账号所属站点的 Origin/Referer 伪装来源。
// 国内站 → www.codebuddy.cn；国际站 → www.workbuddy.ai（与目标域同站）。
func originRefererFor(a *auth.Auth) string {
	return ProfileForAuth(a).Origin
}

// clientVersion 生效的 WorkBuddy 客户端版本：Client.ClientVersion 非空则取之，
// 否则内置默认 defaultClientVersion。
func (c *Client) clientVersion() string {
	if c != nil && c.ClientVersion != "" {
		return c.ClientVersion
	}
	return defaultClientVersion
}

// cliVersion 生效的 CLI 版本：Client.CliVersion 非空则取之，否则内置默认 defaultCliVersion。
func (c *Client) cliVersion() string {
	if c != nil && c.CliVersion != "" {
		return c.CliVersion
	}
	return defaultCliVersion
}

// defaultWorkBuddyUA 组装默认客户端出站 UA（官方桌面端 RestOperations 层形状）：
// `WorkBuddy/<clientVersion> WorkBuddy/<clientVersion> CLI/<cliVersion>`。
func (c *Client) defaultWorkBuddyUA() string {
	return "WorkBuddy/" + c.clientVersion() + " WorkBuddy/" + c.clientVersion() + " CLI/" + c.cliVersion()
}

// userAgent 返回当前出站 UA（客户端出站路径）：
// Client.UserAgent（config user_agent）显式覆盖 > 官方默认 WorkBuddy 三段式。
//
// 关于站点 UA：站点 Profile.ClientUA 为空表示「用官方默认」——合并官方 v1.6.3 后，
// 默认 UA 已升级为真实桌面端形状 `WorkBuddy/5.5.4 WorkBuddy/5.5.4 CLI/2.137.1`
//（官方逆向所得，比旧的 `CLI/2.63.2 CodeBuddy/2.63.2` 更贴近真实指纹）。
// 因此**默认路径不再让站点 UA 抢先**，站点仅在显式配置覆盖时才生效——这样既保留
// 本地的"按站点可配 UA"能力，又不会把官方的正确默认值顶掉。
func (c *Client) userAgent(a *auth.Auth) string {
	if c != nil && c.UserAgent != "" {
		return c.UserAgent
	}
	// 仅当站点**显式配置**了 UA 才用站点值（默认 Profile.ClientUA 为空 → 走官方默认）。
	if c != nil {
		if ov, ok := c.regionOverrideFor(a); ok && ov.UserAgent != "" {
			return ov.UserAgent
		}
	}
	return c.defaultWorkBuddyUA()
}

// regionOverrideFor 返回账号所属站点的**用户配置覆盖**（未配置时为 ok=false）。
func (c *Client) regionOverrideFor(a *auth.Auth) (RegionOverrides, bool) {
	if c == nil {
		return RegionOverrides{}, false
	}
	c.regionMu.RLock()
	defer c.regionMu.RUnlock()
	ov, ok := c.overrides[ProfileForAuth(a).Key]
	return ov, ok
}

// billingUA 白名单类（billing/checkin/banner）出站 UA：单段 `WorkBuddy/<clientVersion>`
// （官方 banner 显式覆写形态，不带 CLI 段）。仅当 client_name 配置（非空）才生效。
func (c *Client) billingUA() string {
	if c == nil || c.ClientName == "" {
		return ""
	}
	return "WorkBuddy/" + c.clientVersion()
}

// resolveDeviceToken 解析本次请求的 X-Device-Token 取值。
// 优先级：auth.Auth.DeviceToken（每号）> Client.DeviceToken（config 全局）> 文件兜底。
// 三者皆空/读失败则返回空串（调用方不注入该头，优雅降级）。
func (c *Client) resolveDeviceToken(a *auth.Auth) string {
	if a != nil && a.DeviceToken != "" {
		return a.DeviceToken
	}
	if c != nil && c.DeviceToken != "" {
		return c.DeviceToken
	}
	if c != nil && c.DeviceTokenFile != "" {
		return readDeviceTokenFile(c.DeviceTokenFile)
	}
	return ""
}

// injectDeviceToken 在 req 注入 X-Device-Token 头（仅当取到非空 token）。
func (c *Client) injectDeviceToken(req *http.Request, a *auth.Auth) {
	if tok := c.resolveDeviceToken(a); tok != "" {
		req.Header.Set("X-Device-Token", tok)
	}
}

// CommonHeaders 设置所有 API 共享的请求头。
func (c *Client) CommonHeaders(req *http.Request, a *auth.Auth) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	origin := originRefererFor(a)
	req.Header.Set("Origin", origin)
	req.Header.Set("Referer", origin+"/")
	req.Header.Set("User-Agent", c.userAgent(a))
}

// ChatHeaders 在 common 之上加 chat 专属的账号头。
// 缺省字段用 X-No-* 约定（与 CodeBuddy 官方 CLI 一致）。
// clientIP 为本次请求的客户端 IP（按参数传递，不读共享字段——避免并发串扰）；
// PassthroughIP=false 或 clientIP 为空时不注入 IP 头。
func (c *Client) ChatHeaders(req *http.Request, a *auth.Auth, clientIP string) {
	c.CommonHeaders(req, a)
	if a.AccessToken != "" {
		req.Header.Set("Authorization", "Bearer "+a.AccessToken)
	} else {
		req.Header.Set("X-No-Authorization", "1")
	}
	if a.UID != "" {
		req.Header.Set("X-User-Id", a.UID)
	} else {
		req.Header.Set("X-No-User-Id", "1")
	}
	if a.EnterpriseID != "" {
		req.Header.Set("X-Enterprise-Id", a.EnterpriseID)
	} else {
		req.Header.Set("X-No-Enterprise-Id", "1")
	}
	// 安全红线：绝不在 chat 请求里携带 X-Refresh-Token。
	if a.Domain != "" {
		req.Header.Set("X-Domain", a.Domain)
	} else {
		req.Header.Set("X-No-Department-Info", "1")
	}
	// 用量归属头：ClientName 非空则四头跟随（对齐官方桌面端），空则保持 X-Product="SaaS"。
	c.injectAttribution(req)
	// 客户端 IP 透传（仅 PassthroughIP=true 且本次请求带 IP）。
	c.injectClientIP(req, clientIP)
	// 设备风控头：auth 每号 > config 全局 > 文件兜底；空则不注入。
	c.injectDeviceToken(req, a)
}

// injectAttribution 注入用量归属头（X-Agent-Purpose / X-IDE-* / X-Product）。
// ClientName 非空时全量跟随该值，空则只保留 X-Product="SaaS"（旧行为，向后兼容）。
func (c *Client) injectAttribution(req *http.Request) {
	if c == nil || c.ClientName == "" {
		req.Header.Set("X-Product", "SaaS")
		return
	}
	req.Header.Set("X-Agent-Purpose", "conversation")
	req.Header.Set("X-IDE-Name", c.ClientName)
	req.Header.Set("X-IDE-Type", c.ClientName)
	req.Header.Set("X-IDE-Version", c.clientVersion())
	req.Header.Set("X-Product", c.ClientName)
}

// injectClientIP 在 PassthroughIP 开启时把 clientIP 参数透传给上游（三等价头）。
func (c *Client) injectClientIP(req *http.Request, clientIP string) {
	if c == nil || !c.PassthroughIP || clientIP == "" {
		return
	}
	req.Header.Set("X-Forwarded-For", clientIP)
	req.Header.Set("X-Real-IP", clientIP)
	req.Header.Set("X-Client-IP", clientIP)
}

// ExtractClientIP 从入站请求提取客户端 IP 首段（X-Forwarded-For 首段，回落 X-Real-IP）。
func ExtractClientIP(r *http.Request) string {
	if r == nil {
		return ""
	}
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		for i := 0; i < len(xff); i++ {
			if xff[i] == ',' {
				return strings.TrimSpace(xff[:i])
			}
		}
		return strings.TrimSpace(xff)
	}
	if real := strings.TrimSpace(r.Header.Get("X-Real-IP")); real != "" {
		return real
	}
	return ""
}

// BillingHeaders billing 接口请求头。
// UA 语义：默认**不设置**（保持现状，Go 客户端自带默认 UA）；仅当显式配置
// c.UserAgent 非空才覆盖——避免默认路径给 billing 引入新的 UA 指纹。
func (c *Client) BillingHeaders(req *http.Request, a *auth.Auth) {
	req.Header.Set("Authorization", "Bearer "+a.AccessToken)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	if c != nil && c.UserAgent != "" {
		req.Header.Set("User-Agent", c.UserAgent)
	} else if ua := c.billingUA(); ua != "" {
		req.Header.Set("User-Agent", ua)
	}
	if a.UID != "" {
		req.Header.Set("X-User-Id", a.UID)
	}
	if a.EnterpriseID != "" {
		req.Header.Set("X-Enterprise-Id", a.EnterpriseID)
		req.Header.Set("X-Tenant-Id", a.EnterpriseID)
	}
	if a.Domain != "" {
		req.Header.Set("X-Domain", a.Domain)
	}
	// 设备风控头：billing 域（report/travel/balance/checkin）同样注入。
	c.injectDeviceToken(req, a)
}

// RefreshHeaders refresh 端点专属头（X-Refresh-Token 只允许出现在这里）。
func (c *Client) RefreshHeaders(req *http.Request, a *auth.Auth) {
	c.CommonHeaders(req, a)
	req.Header.Set("X-Refresh-Token", a.RefreshToken)
	if a.EnterpriseID != "" {
		req.Header.Set("X-Enterprise-Id", a.EnterpriseID)
	}
	req.Header.Set("X-Auth-Refresh-Source", "workbuddy")
}
