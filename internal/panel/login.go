// login.go 面板内嵌的设备授权登录流程（cmd/login 的进程内移植），
// 支持国内站与国际站两个站点。
//
//	POST /panel/api/login/start?region=cn|intl → 拿 state+authUrl，state 存进程内
//	  （不再落 /tmp，原方案在 Windows 上不可用），返回授权 URL；
//	GET  /panel/api/login/poll?state=...       → 面板前端每 3s 轮询本接口；未完成返回
//	  done=false，完成后取 uid/nickname、凭证落盘 auths/workbuddy-<uid>.json
//	  （国际站为 workbuddy-intl-<uid>.json）、热加载进池（pool.Add + Revive），
//	  并顺带签到 + 余额刷新 —— 免重启加载新账号。
//
// 站点差异（依据 CangShui/workbuddy-gateway 实测）：
//   - 域名：国内站 copilot.tencent.com；国际站 www.workbuddy.ai
//   - 登录 platform：VSCode（扫码） vs workbuddy-ai（浏览器内邮箱/验证码/SSO）
//   - Origin/Referer 与 UA 随站点变化
//   - 等待授权期间国际站 auth/token 轮询返回 code 11217（login ing...）
//   - 国际站等待窗口更长（浏览器登录慢于扫码）
//
// 无 PKCE（workbuddy 设备流由服务端签发 state），请求头形状与上游站点 Profile 对齐。
package panel

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
	"github.com/linguo2625469/workbuddy2api-panel/internal/upstream"
)

// loginHTTP 设备授权专用 client：短超时、无 cookie（每请求携带 state，无会话态）。
var loginHTTP = &http.Client{Timeout: 30 * time.Second}

// loginIntent 一次设备授权会话的上下文。
//
// region 必须与 state 一起记录：轮询时只带 state，若依赖调用方再传 region，
// 恶意/错误的前端可以把国际站 state 按国内站轮询，导致凭证被标错站点
// （表现：登录成功但账号随后全部 401）。故 region 在 start 时固化为服务端事实。
type loginIntent struct {
	created time.Time
	region  string
}

// loginDefaultUA 登录流程的默认 UA（官方桌面端三段式）。
//
// 登录走本文件自有的 HTTP 路径（不复用 upstream.Client），故需要自己的默认值。
// 与 upstream 的默认保持一致，避免"登录时一个指纹、登录后另一个指纹"。
// Profile.ClientUA 非空时按站点覆盖（保留本地按站点可配能力）。
const loginDefaultUA = "WorkBuddy/5.5.4 WorkBuddy/5.5.4 CLI/2.137.1"

// commonHeadersFor 按站点设置通用伪装头。
func commonHeadersFor(req *http.Request, p *upstream.Profile) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	req.Header.Set("Origin", p.Origin)
	req.Header.Set("Referer", p.Origin+"/")
	ua := p.ClientUA
	if ua == "" {
		ua = loginDefaultUA
	}
	req.Header.Set("User-Agent", ua)
}

// endpointAuthState / endpointAuthToken / endpointLoginAcct 按站点拼端点 URL。
// 三站路径与响应包络完全一致，只有基址与 platform 参数不同。
func endpointAuthState(p *upstream.Profile) string {
	return p.ChatBase + "/v2/plugin/auth/state?platform=" + url.QueryEscape(p.Platform)
}

func endpointAuthToken(p *upstream.Profile, state string) string {
	return p.ChatBase + "/v2/plugin/auth/token?state=" + url.QueryEscape(state)
}

func endpointLoginAcct(p *upstream.Profile, state string) string {
	return p.ChatBase + "/v2/plugin/login/account?state=" + url.QueryEscape(state)
}

// validUID 校验上游返回的 uid 是否可安全用于拼文件名。
// 只放行字母、数字、下划线、连字符（腾讯侧 uid 实测为 UUID 形态），
// 长度上限 64 兜底异常超长串；拒绝 . / \ 等路径字符与空串。
func validUID(uid string) bool {
	if uid == "" || len(uid) > 64 {
		return false
	}
	for _, c := range uid {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_':
		default:
			return false
		}
	}
	return true
}

// authFileFor 按站点与 uid 生成凭证文件名。
//
// 国内站保持既有 workbuddy-<uid>.json（老部署的既有文件不受改名影响）；
// 国际站加 intl 中缀以便人工核对与区分。两者都匹配 auth.LoadDir 的
// workbuddy*.json glob，故均能被自动发现。
func authFileFor(region, uid string) string {
	if auth.NormalizeRegion(region) == auth.RegionINTL {
		return fmt.Sprintf("workbuddy-intl-%s.json", uid)
	}
	return fmt.Sprintf("workbuddy-%s.json", uid)
}

// isLoginPending 判定"授权尚未完成"。
//
// 国际站等待期间 auth/token 返回 code 11217（login ing...）；国内站用其它
// 业务码表达同一语义。二者都表现为 doJSON 返回业务错误，故这里只做识别以便
// 给出更准确的提示文案与日志，不改变控制流（都是继续轮询）。
func isLoginPending(msg string) bool {
	return strings.Contains(msg, "11217") || strings.Contains(strings.ToLower(msg), "login ing")
}

// apiEnvelope 与 upstream 同形：{code,msg,data}，code!=0 视为业务错误。
type apiEnvelope struct {
	Code int             `json:"code"`
	Msg  string          `json:"msg"`
	Data json.RawMessage `json:"data"`
}

// doJSONFor 发一次 JSON 请求并解信封（按站点 Profile 设置伪装头）。
// noAuth=true 时显式声明 X-No-Authorization（与官方客户端轮询行为一致）。
func doJSONFor(method, fullURL, bearer string, body io.Reader, p *upstream.Profile, noAuth bool) (json.RawMessage, int, error) {
	req, err := http.NewRequest(method, fullURL, body)
	if err != nil {
		return nil, 0, err
	}
	commonHeadersFor(req, p)
	if noAuth {
		req.Header.Set("X-No-Authorization", "1")
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := loginHTTP.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 300 {
		return nil, resp.StatusCode, fmt.Errorf("http_error: upstream %d", resp.StatusCode)
	}
	var env apiEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, resp.StatusCode, fmt.Errorf("parse failed: %w", err)
	}
	if env.Code != 0 {
		return nil, resp.StatusCode, fmt.Errorf("code=%d msg=%s", env.Code, env.Msg)
	}
	return env.Data, resp.StatusCode, nil
}

// regionFromRequest 解析请求里的站点选择；缺省/非法一律国内站（历史默认）。
func regionFromRequest(r *http.Request) string {
	raw := strings.TrimSpace(r.URL.Query().Get("region"))
	if raw == "" {
		// 兼容表单/JSON 提交（前端当前用 query，此处为稳健性兜底）。
		raw = strings.TrimSpace(r.FormValue("region"))
	}
	return auth.NormalizeRegion(raw)
}

// loginStart 发起设备授权：POST auth/state 拿授权 URL。
func (p *Panel) loginStart(w http.ResponseWriter, r *http.Request) {
	region := regionFromRequest(r)
	prof := upstream.ProfileFor(region)

	data, status, err := doJSONFor(http.MethodPost, endpointAuthState(prof), "", bytes.NewReader([]byte("{}")), prof, false)
	if err != nil {
		writeErr(w, http.StatusBadGateway, fmt.Sprintf("auth state (upstream %d): %v", status, err))
		return
	}
	var st struct {
		State   string `json:"state"`
		AuthURL string `json:"authUrl"`
	}
	if err := json.Unmarshal(data, &st); err != nil || st.State == "" || st.AuthURL == "" {
		writeErr(w, http.StatusBadGateway, "auth state: missing state or authUrl")
		return
	}
	p.loginMu.Lock()
	// 顺手回收过期会话，防"开弹窗走开"的 state 滞留。
	for s, it := range p.logins {
		if time.Since(it.created) > loginTTL {
			delete(p.logins, s)
		}
	}
	p.logins[st.State] = loginIntent{created: time.Now(), region: prof.Key}
	p.loginMu.Unlock()
	log.Printf("panel: 发起 OAuth 添加账号（站点=%s state=%s...）", prof.Label, st.State[:min(8, len(st.State))])
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "url": st.AuthURL, "state": st.State,
		"region": prof.Key, "region_label": prof.Label,
		// 等待窗口交给前端展示（国际站 15 分钟内需在浏览器完成登录）。
		"login_ttl_sec": int(prof.LoginTTL.Seconds()),
	})
}

// loginPoll 轮询登录态。未完成 → {done:false}；完成 → 建凭证、落盘、热加载、签到。
func (p *Panel) loginPoll(w http.ResponseWriter, r *http.Request) {
	state := r.URL.Query().Get("state")
	if state == "" {
		writeErr(w, http.StatusBadRequest, "missing state")
		return
	}
	p.loginMu.Lock()
	intent, known := p.logins[state]
	p.loginMu.Unlock()
	if !known {
		writeErr(w, http.StatusNotFound, "unknown or expired state（请重新发起添加账号）")
		return
	}
	// 站点取 start 时固化的值，而非本次请求的 query（防轮询期站点被篡改）。
	prof := upstream.ProfileFor(intent.region)

	// auth/token 是权威登录状态端点：pending 时业务 code 非 0
	// （国际站为 11217 "login ing..."，国内站为同类业务码）。
	// 轮询时与官方客户端一致，显式声明无 Authorization。
	tokRaw, _, err := doJSONFor(http.MethodGet, endpointAuthToken(prof, state), "", nil, prof, true)
	if err != nil {
		// pending / 未完成：面板前端继续轮询。
		msg := err.Error()
		if isLoginPending(msg) {
			msg = "waiting for login"
		}
		writeJSON(w, http.StatusOK, map[string]any{"done": false, "message": msg})
		return
	}
	var tok struct {
		AccessToken  string `json:"accessToken"`
		RefreshToken string `json:"refreshToken"`
		ExpiresIn    int64  `json:"expiresIn"`
		Domain       string `json:"domain"`
	}
	if err := json.Unmarshal(tokRaw, &tok); err != nil || tok.AccessToken == "" {
		writeJSON(w, http.StatusOK, map[string]any{"done": false, "message": "waiting for login"})
		return
	}

	// 完成：取 uid/nickname（失败不阻塞，仅缺展示名）。
	var acct struct {
		UID          string `json:"uid"`
		EnterpriseID string `json:"enterpriseId"`
		Nickname     string `json:"nickname"`
	}
	if acctRaw, _, err := doJSONFor(http.MethodGet, endpointLoginAcct(prof, state), tok.AccessToken, nil, prof, false); err == nil {
		_ = json.Unmarshal(acctRaw, &acct)
	}
	if acct.UID == "" {
		writeErr(w, http.StatusBadGateway, "login done but no uid（token 已发但账号信息获取失败，请重试）")
		return
	}
	// UID 来自上游响应，未经校验就用于拼文件名会被路径穿越利用
	// （filepath.Join("./auths", "workbuddy-../../evil.json") → auths/evil.json）。
	// UID 是腾讯侧账号标识，实测为 UUID（十六进制与连字符），故只放行 [A-Za-z0-9_-]。
	if !validUID(acct.UID) {
		writeErr(w, http.StatusBadGateway, "上游返回的 uid 含非法字符，拒绝落盘（防路径穿越）")
		return
	}

	// 凭证落盘（嵌套形 + edition 站点标识，与 auths/ 目录既有格式及
	// workbuddy-gateway 的凭据格式互通）→ 热加载进池。
	if err := os.MkdirAll(p.cfg.AuthDir, 0o755); err != nil {
		writeErr(w, http.StatusInternalServerError, "mkdir auth dir: "+err.Error())
		return
	}
	a := &auth.Auth{
		AccessToken:  tok.AccessToken,
		RefreshToken: tok.RefreshToken,
		ExpiresAt:    time.Now().Add(time.Duration(tok.ExpiresIn) * time.Second).Unix(),
		Domain:       tok.Domain,
		UID:          acct.UID,
		EnterpriseID: acct.EnterpriseID,
		Nickname:     acct.Nickname,
		Edition:      prof.Key, // 站点标识：决定后续所有出站请求打到哪个上游
		FilePath:     filepath.Join(p.cfg.AuthDir, authFileFor(prof.Key, acct.UID)),
	}
	if err := a.SaveAtomic(); err != nil {
		writeErr(w, http.StatusInternalServerError, "save auth: "+err.Error())
		return
	}
	p.cfg.Pool.Add(a)
	p.cfg.Pool.Revive(acct.UID) // 全新登录 = 人工恢复口径：清掉旧号遗留的禁用/冷却/熔断

	// 顺带签到 + 余额刷新（幂等；失败不影响登录结果，只体现在返回字段里）。
	//
	// 签到与余额**分开判断**——二者在国际站的能力不同（2026-09 真实 token 实测）：
	//   - 签到：国际站 /v2/billing/meter/daily-checkin 返回 code=10001
	//     「签到活动未开启或已过期」，属常态（该站没开这个活动），故跳过，不打无谓请求；
	//   - 余额：国际站计费域同域部署、路径一致，实测返回完整套餐数据（本项目口径
	//     可正常合计），**照常执行**——否则面板「刷新」与后台余额轮询对国际站全失效。
	checkinMsg := ""
	remain := int64(-1)
	total := int64(0)
	if upstream.SupportsCheckin(prof.Key) {
		if err := p.cfg.Upstream.DailyCheckin(a); err != nil {
			checkinMsg = err.Error()
		}
	} else {
		checkinMsg = "国际站未开启签到活动，跳过首次签到"
	}
	if upstream.SupportsBalanceAPI(prof.Key) {
		if rm, tt, err := p.cfg.Upstream.UserResource(a); err == nil {
			remain, total = rm, tt
			p.cfg.Pool.ReenableIfCredits(acct.UID, rm, tt)
		}
	}

	p.loginMu.Lock()
	delete(p.logins, state)
	p.loginMu.Unlock()
	log.Printf("panel: 新账号已热加载 uid=%s nickname=%q 站点=%s（免重启生效）", acct.UID, acct.Nickname, prof.Label)
	writeJSON(w, http.StatusOK, map[string]any{
		"done":            true,
		"uid":             acct.UID,
		"nickname":        acct.Nickname,
		"credits":         remain,
		"credits_total":   total,
		"region":          prof.Key,
		"region_label":    prof.Label,
		"checkin_message": checkinMsg,
	})
}
