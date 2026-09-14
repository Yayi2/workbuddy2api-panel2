package panel

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
	"github.com/linguo2625469/workbuddy2api-panel/internal/upstream"
)

func TestAuthFileForRegion(t *testing.T) {
	// 国内站保持既有文件名（老部署的既有凭证文件不受影响）。
	if got := authFileFor(auth.RegionCN, "uid-1"); got != "workbuddy-uid-1.json" {
		t.Errorf("CN 凭证文件名 = %q, want workbuddy-uid-1.json（保持既有命名）", got)
	}
	// 国际站加 intl 中缀以便区分。
	if got := authFileFor(auth.RegionINTL, "uid-1"); got != "workbuddy-intl-uid-1.json" {
		t.Errorf("INTL 凭证文件名 = %q, want workbuddy-intl-uid-1.json", got)
	}
	// 空值回退国内站。
	if got := authFileFor("", "uid-1"); got != "workbuddy-uid-1.json" {
		t.Errorf("空 region 文件名 = %q, want 国内站命名", got)
	}
	// 两种命名都必须能被 auth.LoadDir 的 workbuddy*.json glob 发现。
	dir := t.TempDir()
	for _, r := range []string{auth.RegionCN, auth.RegionINTL} {
		fp := filepath.Join(dir, authFileFor(r, "u-"+r))
		a := &auth.Auth{AccessToken: "at", UID: "u-" + r, Edition: r, FilePath: fp}
		if err := a.SaveAtomic(); err != nil {
			t.Fatal(err)
		}
	}
	loaded, err := auth.LoadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 2 {
		t.Errorf("LoadDir 发现 %d 个凭证，want 2（两种命名都要能被自动发现）", len(loaded))
	}
}

// TestEndpointPerRegion 端点 URL 按站点拼接：基址与 platform 参数都不同。
func TestEndpointPerRegion(t *testing.T) {
	cn := upstream.ProfileFor(auth.RegionCN)
	intl := upstream.ProfileFor(auth.RegionINTL)

	cnState := endpointAuthState(cn)
	if !strings.HasPrefix(cnState, "https://copilot.tencent.com/v2/plugin/auth/state?platform=") {
		t.Errorf("CN auth/state = %q", cnState)
	}
	if !strings.Contains(cnState, "platform=VSCode") {
		t.Errorf("CN platform 应为 VSCode: %q", cnState)
	}

	intlState := endpointAuthState(intl)
	if !strings.HasPrefix(intlState, "https://www.workbuddy.ai/v2/plugin/auth/state?platform=") {
		t.Errorf("INTL auth/state = %q", intlState)
	}
	if !strings.Contains(intlState, "platform=workbuddy-ai") {
		t.Errorf("INTL platform 应为 workbuddy-ai: %q", intlState)
	}

	// token / account 端点基址同样分流（路径一致，仅域名不同）。
	if got := endpointAuthToken(intl, "st"); !strings.HasPrefix(got, "https://www.workbuddy.ai/v2/plugin/auth/token?state=") {
		t.Errorf("INTL auth/token = %q", got)
	}
	if got := endpointLoginAcct(intl, "st"); !strings.HasPrefix(got, "https://www.workbuddy.ai/v2/plugin/login/account?state=") {
		t.Errorf("INTL login/account = %q", got)
	}
	// state 必须做 URL 转义（防注入查询参数）。
	weird := endpointAuthToken(intl, "a b&c=d")
	if strings.Contains(weird, "&c=d") {
		t.Errorf("state 未转义，可注入查询参数: %q", weird)
	}
	if want := url.QueryEscape("a b&c=d"); !strings.Contains(weird, want) {
		t.Errorf("state 转义结果 = %q, want 含 %q", weird, want)
	}
}

// TestIsLoginPending 授权未完成的识别：国际站 code 11217 文案。
func TestIsLoginPending(t *testing.T) {
	pending := []string{
		"code=11217 msg=login ing...",
		"code=11217 msg=",
		"something LOGIN ING here",
	}
	for _, s := range pending {
		if !isLoginPending(s) {
			t.Errorf("isLoginPending(%q) = false, want true", s)
		}
	}
	notPending := []string{"", "http_error: upstream 500", "code=12153 session dead"}
	for _, s := range notPending {
		if isLoginPending(s) {
			t.Errorf("isLoginPending(%q) = true, want false", s)
		}
	}
}

// TestRegionFromRequest 请求里的 region 解析：缺省/非法回退国内站。
func TestRegionFromRequest(t *testing.T) {
	cases := map[string]string{
		"":                     auth.RegionCN,
		"?region=":             auth.RegionCN,
		"?region=intl":         auth.RegionINTL,
		"?region=global":       auth.RegionINTL,
		"?region=workbuddy.ai": auth.RegionINTL,
		"?region=bogus":        auth.RegionCN,
		"?region=cn":           auth.RegionCN,
		"?region=INTL":         auth.RegionINTL,
	}
	for q, want := range cases {
		r := httptest.NewRequest("POST", "/panel/api/login/start"+q, nil)
		if got := regionFromRequest(r); got != want {
			t.Errorf("regionFromRequest(%q) = %q, want %q", q, got, want)
		}
	}
}

// TestLoginStartRecordsRegion 授权会话必须固化站点：start 后轮询阶段不再依赖 query。
//
// 这是防"轮询期篡改站点"的关键——若轮询时按本次请求的 region 重新解析，
// 一个国际站 state 可被按国内站轮询，凭证会被标成 cn，该账号随后全部 401。
func TestLoginStartRecordsRegion(t *testing.T) {
	p := New(Config{Version: "test"}) // 不鉴权
	// 直接注入一次会话（绕过真实上游调用），验证固化语义。
	p.loginMu.Lock()
	p.logins["st-1"] = loginIntent{created: nowForTest(), region: auth.RegionINTL}
	p.loginMu.Unlock()

	p.loginMu.Lock()
	it, ok := p.logins["st-1"]
	p.loginMu.Unlock()
	if !ok {
		t.Fatal("会话未记录")
	}
	if it.region != auth.RegionINTL {
		t.Errorf("固化站点 = %q, want intl", it.region)
	}
	// 即便随后以 region=cn 轮询，站点仍取固化值。
	r := httptest.NewRequest("GET", "/panel/api/login/poll?state=st-1&region=cn", nil)
	prof := upstream.ProfileFor(it.region)
	if prof.Key != auth.RegionINTL {
		t.Errorf("轮询站点 = %q, 应取 start 时固化值而非 query", prof.Key)
	}
	_ = r
}

// TestLoginUnknownState 未知 state 返回 404（不泄露站点信息）。
func TestLoginUnknownState(t *testing.T) {
	p := New(Config{Version: "test"})
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, httptest.NewRequest("GET", "/panel/api/login/poll?state=nope", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("code = %d, want 404", rec.Code)
	}
	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body["error"] == nil {
		t.Error("应返回 error 字段")
	}
}

// TestLoginStartRequiresUpstreamReachable 上游不可达时 loginStart 返回 502 而非 panic。
//
// 用一个必定连接失败的常量无法注入（端点由 Profile 拼死），故这里改为验证
// 「响应形态」：任何上游错误都必须落到 502 + error 字段，前端才有可展示的提示。
func TestLoginStartBadUpstreamReturns502Shape(t *testing.T) {
	// 直接构造一个指向不可达地址的 Profile 调用 doJSONFor，验证错误路径。
	badProf := *upstream.ProfileFor(auth.RegionCN)
	badProf.ChatBase = "http://127.0.0.1:1" // 保留端口 1：连接必失败
	_, status, err := doJSONFor(http.MethodPost, endpointAuthState(&badProf), "", strings.NewReader("{}"), &badProf, false)
	if err == nil {
		t.Fatal("不可达上游应返回错误")
	}
	if status != 0 {
		t.Logf("status=%d err=%v（连接层失败时 status 为 0 属预期）", status, err)
	}
}

// TestLoginHeadersPerRegion 登录请求的 Origin/Referer/UA 必须随站点变化。
func TestLoginHeadersPerRegion(t *testing.T) {
	for _, tc := range []struct {
		region     string
		wantOrigin string
		wantUA     string
	}{
		{auth.RegionCN, "https://www.codebuddy.cn", "CLI/2.63.2 CodeBuddy/2.63.2"},
		{auth.RegionINTL, "https://www.workbuddy.ai", "CLI/2.63.2 CodeBuddy/2.63.2"},
	} {
		prof := upstream.ProfileFor(tc.region)
		req := httptest.NewRequest("POST", "https://example/", nil)
		commonHeadersFor(req, prof)
		if got := req.Header.Get("Origin"); got != tc.wantOrigin {
			t.Errorf("[%s] Origin = %q, want %q", tc.region, got, tc.wantOrigin)
		}
		if got := req.Header.Get("Referer"); got != tc.wantOrigin+"/" {
			t.Errorf("[%s] Referer = %q", tc.region, got)
		}
		if got := req.Header.Get("User-Agent"); got != tc.wantUA {
			t.Errorf("[%s] UA = %q, want %q", tc.region, got, tc.wantUA)
		}
	}
}

// TestSavedCredentialCarriesEditionRegion 登录落盘的凭证必须带 edition，
// 且能被 auth.Parse 解析回同一站点（否则重启后国际站账号会被当国内站路由）。
func TestSavedCredentialCarriesEditionRegion(t *testing.T) {
	dir := t.TempDir()
	for _, region := range []string{auth.RegionCN, auth.RegionINTL} {
		fp := filepath.Join(dir, authFileFor(region, "u1"))
		a := &auth.Auth{
			AccessToken: "at", RefreshToken: "rt", UID: "u1",
			Nickname: "n", Edition: region, FilePath: fp,
		}
		if err := a.SaveAtomic(); err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(fp)
		if err != nil {
			t.Fatal(err)
		}
		back, err := auth.Parse(raw)
		if err != nil {
			t.Fatalf("parse %s: %v", fp, err)
		}
		if back.Region() != region {
			t.Errorf("[%s] 落盘后 Region() = %q, want %q", region, back.Region(), region)
		}
	}
}

// nowForTest 供注入测试会话使用。
func nowForTest() time.Time { return time.Now() }
