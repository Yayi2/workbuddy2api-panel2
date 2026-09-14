package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
	"github.com/linguo2625469/workbuddy2api-panel/internal/upstream"
)

// newFakeUpstreamCaptureBody 与 newFakeUpstream 同构，但把请求体也交给行为函数——
// 用于断言"网关发往上游的 model 字段"（如 @intl 后缀是否被正确剥离）。
func newFakeUpstreamCaptureBody(t *testing.T, behavior func(authz, reqBody string) (status int, resp string, isStream bool)) *upstream.Client {
	t.Helper()
	return &upstream.Client{
		HTTP: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			raw, _ := io.ReadAll(r.Body)
			status, respBody, isStream := behavior(r.Header.Get("Authorization"), string(raw))
			ct := "application/json"
			if isStream {
				ct = "text/event-stream"
			}
			return &http.Response{
				StatusCode: status,
				Header:     http.Header{"Content-Type": []string{ct}},
				Body:       io.NopCloser(strings.NewReader(respBody)),
			}, nil
		})},
		ChatBaseCN:    "https://fake.example",
		BillingBaseCN: "https://fake.example",
	}
}

// TestRequestRegionParsing 区域意图解析：请求头、model 后缀、优先级、容错。
func TestRequestRegionParsing(t *testing.T) {
	cases := []struct {
		header   string
		model    string
		wantReg  string
		wantBare string
	}{
		// 无任何区域意图：不限（region 为空），模型名原样。
		{"", "glm-5.2", "", "glm-5.2"},
		{"", "", "", ""},

		// 请求头指定。
		{"intl", "glm-5.2", auth.RegionINTL, "glm-5.2"},
		{"cn", "glm-5.2", auth.RegionCN, "glm-5.2"},
		{"INTL", "glm-5.2", auth.RegionINTL, "glm-5.2"},
		{" intl ", "glm-5.2", auth.RegionINTL, "glm-5.2"},
		{"global", "glm-5.2", auth.RegionINTL, "glm-5.2"},
		{"workbuddy.ai", "glm-5.2", auth.RegionINTL, "glm-5.2"},

		// model 后缀。后缀必须被剥离（上游不认识 @intl）。
		{"", "glm-5.2@intl", auth.RegionINTL, "glm-5.2"},
		{"", "glm-5.2@cn", auth.RegionCN, "glm-5.2"},
		{"", "deepseek-v4-pro@global", auth.RegionINTL, "deepseek-v4-pro"},
		// 非法后缀：整体保留为模型名（不得误剥，否则模型名被截断）。
		{"", "glm-5.2@bogus", "", "glm-5.2@bogus"},
		{"", "glm-5.2@", "", "glm-5.2@"},
		{"", "@intl", "", "@intl"},

		// 请求头优先于后缀（显式意图强于约定）。
		{"cn", "glm-5.2@intl", auth.RegionCN, "glm-5.2"},

		// 显式"不限"取值。
		{"any", "glm-5.2", "", "glm-5.2"},
		{"all", "glm-5.2", "", "glm-5.2"},
		{"auto", "glm-5.2", "", "glm-5.2"},
		{"none", "glm-5.2", "", "glm-5.2"},
		{"*", "glm-5.2", "", "glm-5.2"},

		// 未知 token：视为不限（绝不静默收窄到 cn，否则国际站账号被无声排除）。
		{"amtl", "glm-5.2", "", "glm-5.2"},
		{"bogus", "glm-5.2", "", "glm-5.2"},
		{"tencent", "glm-5.2", "", "glm-5.2"},
	}
	for _, c := range cases {
		reg, bare := requestRegion(c.header, c.model)
		if reg != c.wantReg {
			t.Errorf("requestRegion(%q, %q) region = %q, want %q", c.header, c.model, reg, c.wantReg)
		}
		if bare != c.wantBare {
			t.Errorf("requestRegion(%q, %q) bareModel = %q, want %q", c.header, c.model, bare, c.wantBare)
		}
	}
}

// TestRewriteModelField 模型名改写：只改 model 键，其余字段原样保留。
func TestRewriteModelField(t *testing.T) {
	body := []byte(`{"model":"glm-5.2@intl","messages":[{"role":"user","content":"hi"}],"stream":true,"temperature":0.7}`)
	out, err := rewriteModelField(body, "glm-5.2")
	if err != nil {
		t.Fatalf("rewriteModelField: %v", err)
	}
	var obj map[string]any
	if err := json.Unmarshal(out, &obj); err != nil {
		t.Fatalf("解析改写结果失败: %v", err)
	}
	if obj["model"] != "glm-5.2" {
		t.Errorf("model = %v, want glm-5.2（后缀必须剥离）", obj["model"])
	}
	// 其余字段必须完整保留。
	if obj["stream"] != true {
		t.Error("stream 字段丢失")
	}
	if obj["temperature"] != 0.7 {
		t.Errorf("temperature = %v, want 0.7", obj["temperature"])
	}
	msgs, ok := obj["messages"].([]any)
	if !ok || len(msgs) != 1 {
		t.Errorf("messages 字段损坏: %v", obj["messages"])
	}

	// 无 model 字段 → 报错（调用方回退原始 body）。
	if _, err := rewriteModelField([]byte(`{"stream":true}`), "glm-5.2"); err == nil {
		t.Error("无 model 字段应返回错误")
	}
	// 非法 JSON → 报错。
	if _, err := rewriteModelField([]byte(`{not json`), "glm-5.2"); err == nil {
		t.Error("非法 JSON 应返回错误")
	}
}

// TestRegionUnavailableResponse 区域无可用账号 → 503 + 明确区分于"网关全挂"。
func TestRegionUnavailableResponse(t *testing.T) {
	rec := httptest.NewRecorder()
	writeRegionUnavailable(rec, auth.RegionINTL)

	if rec.Code != 503 {
		t.Errorf("code = %d, want 503", rec.Code)
	}
	if got := rec.Header().Get(HeaderRegionApplied); got != auth.RegionINTL {
		t.Errorf("%s = %q, want intl", HeaderRegionApplied, got)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "no_available_account_in_region") {
		t.Errorf("错误码应可区分于全池不可用: %s", body)
	}
	if !strings.Contains(body, "国际站") {
		t.Errorf("错误文案应点明站点: %s", body)
	}
	if !strings.Contains(body, HeaderRegion) {
		t.Errorf("错误文案应提示如何放宽（去掉头部）: %s", body)
	}
}

// TestChatRoutesToRegion 端到端：X-WB-Region: intl 必须只走国际站账号。
//
// 判定方式：假上游按 Authorization 头区分账号（at-cn / at-intl），据此断言
// 被选中的账号确实属于所要求的站点。
func TestChatRoutesToRegion(t *testing.T) {
	var seen []string
	up := newFakeUpstream(t, func(authz string) (int, string, bool) {
		seen = append(seen, authz)
		return 200, sseOK, true
	})
	p := testPoolWith(
		&auth.Auth{UID: "cn-1", AccessToken: "at-cn", ExpiresAt: 9999999999, Edition: auth.RegionCN},
		&auth.Auth{UID: "intl-1", AccessToken: "at-intl", ExpiresAt: 9999999999, Edition: auth.RegionINTL},
	)
	h := NewHandler(Config{Pool: p, Upstream: up})

	// 国际站收窄：多次请求，命中账号必须恒为 intl-1。
	for i := 0; i < 5; i++ {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/v1/chat/completions",
			strings.NewReader(`{"model":"glm-5.2","messages":[{"role":"user","content":"hi"}]}`))
		req.Header.Set(HeaderRegion, "intl")
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("第 %d 次请求 code = %d, body=%s", i, rec.Code, rec.Body.String())
		}
		if got := rec.Header().Get(HeaderRegionApplied); got != auth.RegionINTL {
			t.Errorf("%s = %q, want intl", HeaderRegionApplied, got)
		}
	}
	for _, a := range seen {
		if a != "Bearer at-intl" {
			t.Fatalf("国际站收窄却打到 %q（应为 Bearer at-intl）", a)
		}
	}

	// 国内站收窄：应只打到 cn-1。
	seen = nil
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"glm-5.2","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set(HeaderRegion, "cn")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("国内站收窄 code = %d, body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get(HeaderRegionApplied); got != auth.RegionCN {
		t.Errorf("%s = %q, want cn", HeaderRegionApplied, got)
	}
	for _, a := range seen {
		if a != "Bearer at-cn" {
			t.Fatalf("国内站收窄却打到 %q（应为 Bearer at-cn）", a)
		}
	}
}

// TestChatModelSuffixSelectsRegion 端到端：model 后缀 "glm-5.2@intl" 等价于区域头，
// 且上游收到的模型名必须是剥离后缀后的裸名。
func TestChatModelSuffixSelectsRegion(t *testing.T) {
	var gotModel, gotAuthz string
	up := newFakeUpstreamCaptureBody(t, func(authz, body string) (int, string, bool) {
		gotAuthz = authz
		var obj struct {
			Model string `json:"model"`
		}
		_ = json.Unmarshal([]byte(body), &obj)
		gotModel = obj.Model
		return 200, sseOK, true
	})
	p := testPoolWith(
		&auth.Auth{UID: "intl-1", AccessToken: "at-intl", ExpiresAt: 9999999999, Edition: auth.RegionINTL},
	)
	h := NewHandler(Config{Pool: p, Upstream: up})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"glm-5.2@intl","messages":[{"role":"user","content":"hi"}]}`))
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, body=%s", rec.Code, rec.Body.String())
	}
	if gotModel != "glm-5.2" {
		t.Errorf("上游收到 model = %q, want glm-5.2（@intl 后缀必须剥离）", gotModel)
	}
	if gotAuthz != "Bearer at-intl" {
		t.Errorf("打到账号 = %q, want Bearer at-intl", gotAuthz)
	}
	if got := rec.Header().Get(HeaderRegionApplied); got != auth.RegionINTL {
		t.Errorf("%s = %q, want intl", HeaderRegionApplied, got)
	}
}

// TestChatRegionUnavailableReturns503 区域无账号 → 503 + 可区分错误码，且不跨站点回落。
func TestChatRegionUnavailableReturns503(t *testing.T) {
	var calls int
	up := newFakeUpstream(t, func(authz string) (int, string, bool) {
		calls++
		return 200, sseOK, true
	})
	p := testPoolWith(
		&auth.Auth{UID: "cn-1", AccessToken: "at-cn", ExpiresAt: 9999999999, Edition: auth.RegionCN},
	)
	h := NewHandler(Config{Pool: p, Upstream: up})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"glm-5.2","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set(HeaderRegion, "intl")
	h.ServeHTTP(rec, req)

	if rec.Code != 503 {
		t.Fatalf("code = %d, want 503（国际站无账号，不得回落到国内站）", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "no_available_account_in_region") {
		t.Errorf("错误码应区分于全池不可用: %s", rec.Body.String())
	}
	if calls != 0 {
		t.Errorf("区域无候选时不应打上游，calls=%d", calls)
	}
}

// TestChatNoRegionHintKeepsLegacyBehavior 不带区域意图时行为与从前一致（全池混挂）。
func TestChatNoRegionHintKeepsLegacyBehavior(t *testing.T) {
	up := newFakeUpstream(t, func(authz string) (int, string, bool) {
		return 200, sseOK, true
	})
	p := testPoolWith(
		&auth.Auth{UID: "cn-1", AccessToken: "at-cn", ExpiresAt: 9999999999, Edition: auth.RegionCN},
		&auth.Auth{UID: "intl-1", AccessToken: "at-intl", ExpiresAt: 9999999999, Edition: auth.RegionINTL},
	)
	h := NewHandler(Config{Pool: p, Upstream: up})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"glm-5.2","messages":[{"role":"user","content":"hi"}]}`))
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, body=%s", rec.Code, rec.Body.String())
	}
	// 未指定区域时不得写入该响应头（不误导调用方以为发生了收窄）。
	if got := rec.Header().Get(HeaderRegionApplied); got != "" {
		t.Errorf("%s = %q, want 空（未发生区域收窄）", HeaderRegionApplied, got)
	}
}

// TestStatusIncludesRegionCounts /status 必须透出按站点分组计数。
func TestStatusIncludesRegionCounts(t *testing.T) {
	p := testPoolWith(
		&auth.Auth{UID: "cn-1", AccessToken: "at-cn", ExpiresAt: 9999999999, Edition: auth.RegionCN},
		&auth.Auth{UID: "intl-1", AccessToken: "at-intl", ExpiresAt: 9999999999, Edition: auth.RegionINTL},
	)
	h := NewHandler(Config{Pool: p, Upstream: upstream.New()})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/status", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d", rec.Code)
	}
	var body struct {
		Regions []struct {
			Region  string `json:"region"`
			Label   string `json:"label"`
			Total   int    `json:"total"`
			Healthy int    `json:"healthy"`
			Growth  bool   `json:"growth"`
		} `json:"regions"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("解析 /status 失败: %v", err)
	}
	if len(body.Regions) != 2 {
		t.Fatalf("regions 长度 = %d, want 2", len(body.Regions))
	}
	if body.Regions[0].Region != "cn" || body.Regions[1].Region != "intl" {
		t.Errorf("区域顺序 = [%s %s], want [cn intl]", body.Regions[0].Region, body.Regions[1].Region)
	}
	if body.Regions[0].Total != 1 || body.Regions[1].Total != 1 {
		t.Errorf("各站计数 = %d/%d, want 1/1", body.Regions[0].Total, body.Regions[1].Total)
	}
	if body.Regions[1].Growth {
		t.Error("国际站 Growth 应为 false")
	}
}

// TestHealthzIncludesRegionCounts /healthz 透出区域分组（混挂部署探活用）。
func TestHealthzIncludesRegionCounts(t *testing.T) {
	p := testPoolWith(
		&auth.Auth{UID: "cn-1", AccessToken: "at-cn", ExpiresAt: 9999999999, Edition: auth.RegionCN},
	)
	h := NewHandler(Config{Pool: p, Upstream: upstream.New()})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/healthz", nil))
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("解析 /healthz 失败: %v", err)
	}
	if _, ok := body["regions"]; !ok {
		t.Error("/healthz 应透出 regions 分组")
	}
}

// TestAccountStatusCarriesRegion /status 的每账号条目必须带站点，
// 面板账号表格据此渲染「区域」列。
func TestAccountStatusCarriesRegion(t *testing.T) {
	p := testPoolWith(
		&auth.Auth{UID: "intl-1", AccessToken: "at-intl", ExpiresAt: 9999999999, Edition: auth.RegionINTL},
	)
	h := NewHandler(Config{Pool: p, Upstream: upstream.New()})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/status", nil))
	var body struct {
		Accounts []struct {
			UID          string `json:"uid"`
			Region       string `json:"region"`
			RegionLabel  string `json:"region_label"`
			RegionGrowth bool   `json:"region_growth"`
		} `json:"accounts"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Accounts) != 1 {
		t.Fatalf("accounts 长度 = %d, want 1", len(body.Accounts))
	}
	a := body.Accounts[0]
	if a.Region != "intl" || a.RegionLabel != "国际站" {
		t.Errorf("账号站点 = %q/%q, want intl/国际站", a.Region, a.RegionLabel)
	}
	if a.RegionGrowth {
		t.Error("国际站账号 RegionGrowth 应为 false")
	}
}
