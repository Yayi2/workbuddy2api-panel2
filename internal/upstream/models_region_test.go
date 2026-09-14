package upstream

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
)

// TestSupportsModelsAPIBoundary 模型清单接口的站点能力边界。
//
// 判据（2026-09，用**两个真实有效 token** 实测，而非无效 token 推断）：
//
//	GET www.workbuddy.ai/console/enterprises/personal/models → 500 裸 HTML（与国内站同一 URL）
//	同 token 打 /v2/plugin/login/account  → 200（token 有效，返回账号数据）
//	同 token 打 /v2/chat/completions     → 业务包络 code=11128（后端可达、鉴权已通过）
//
// 故国际站确实取不到模型清单。JWT claims 亦印证：该 token 为 Keycloak
// `azp: console`，scope 仅 `openid profile offline_access email`，不含控制台 API 角色。
//
// 反面教训（写进测试防止再次误判）：**不能**用
// `/console/enterprise/personal/models`（单数）的 401/403 当判据——整个
// `/console/enterprise/` 前缀（含刻意编造的不存在路径）在无有效鉴权时都返回 401/403，
// 属网关级前缀兜底，无法区分"路由存在但无权限"。
func TestSupportsModelsAPIBoundary(t *testing.T) {
	if !SupportsModelsAPI(auth.RegionCN) {
		t.Error("国内站应提供模型清单接口")
	}
	if SupportsModelsAPI(auth.RegionINTL) {
		t.Error("国际站不提供模型清单接口（同 URL 实测裸 HTML 500）")
	}
	// 空/未知回退国内站（与 ProfileFor 同口径）。
	if !SupportsModelsAPI("") {
		t.Error("空 region 回退国内站，应支持")
	}
	if !SupportsModelsAPI("unknown") {
		t.Error("未知 region 回退国内站，应支持")
	}
	// 按账号判断的便捷入口。
	if SupportsModelsAPIForAuth(&auth.Auth{UID: "u", Edition: auth.RegionINTL}) {
		t.Error("国际站账号不应支持模型清单接口")
	}
	if !SupportsModelsAPIForAuth(&auth.Auth{UID: "u", Edition: auth.RegionCN}) {
		t.Error("国内站账号应支持")
	}
	if !SupportsModelsAPIForAuth(nil) {
		t.Error("nil 账号回退国内站，应支持")
	}
}

// TestFetchModelsRejectsIntlRegion 对国际站账号必须**不发请求**直接返回哨兵错误。
//
// 关键断言是 calls==0：若仍然把请求发出去，混挂池下会持续拿到上游裸 HTML 500，
// 既浪费往返又污染错误信息（把网关不认识的一段 HTML 抛给用户）。
func TestFetchModelsRejectsIntlRegion(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(500)
		w.Write([]byte("<html><head><title>500 Internal Server Error</title></head></html>"))
	}))
	defer srv.Close()

	// 注入假上游地址：即便被错误地放行，也会打到这个 500 服务器。
	c := &Client{HTTP: srv.Client(), ChatBaseCN: srv.URL}
	intl := &auth.Auth{UID: "intl-1", AccessToken: "at", Edition: auth.RegionINTL}

	_, err := c.FetchModels(intl)
	if !errors.Is(err, ErrModelsUnsupported) {
		t.Fatalf("err = %v, want ErrModelsUnsupported", err)
	}
	if calls != 0 {
		t.Errorf("对国际站账号不应发起请求，calls=%d", calls)
	}
}

// TestFetchModelsAllowsCN 国内站账号照常请求并解析（回归保护）。
func TestFetchModelsAllowsCN(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"code":0,"data":{
		  "models":[{"id":"glm-5.2","name":"GLM-5.2","maxInputTokens":131072,"maxOutputTokens":32768,
		             "reasoning":{"supportedEfforts":["low","high"],"effort":"high"}}],
		  "agents":[{"name":"cli","models":["glm-5.2"]}]}}`))
	}))
	defer srv.Close()

	c := &Client{HTTP: srv.Client(), ChatBaseCN: srv.URL}
	infos, err := c.FetchModels(&auth.Auth{UID: "cn-1", AccessToken: "at", Edition: auth.RegionCN})
	if err != nil {
		t.Fatalf("FetchModels(CN): %v", err)
	}
	if gotPath != "/console/enterprises/personal/models" {
		t.Errorf("path = %q", gotPath)
	}
	if len(infos) != 1 || infos[0].ID != "glm-5.2" {
		t.Fatalf("infos = %+v", infos)
	}
	if len(infos[0].Efforts) != 2 {
		t.Errorf("efforts = %v, want 2 档", infos[0].Efforts)
	}
}

// TestFetchModelsLegacyAccountUsesCN 老账号（无 edition）按国内站处理，
// 即模型清单接口可用——这是向后兼容的硬约束。
func TestFetchModelsLegacyAccountUsesCN(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Write([]byte(`{"code":0,"data":{"models":[{"id":"glm-5.2","name":"n"}],
		  "agents":[{"name":"cli","models":["glm-5.2"]}]}}`))
	}))
	defer srv.Close()

	c := &Client{HTTP: srv.Client(), ChatBaseCN: srv.URL}
	if _, err := c.FetchModels(&auth.Auth{UID: "legacy", AccessToken: "at"}); err != nil {
		t.Fatalf("老账号应可拉模型: %v", err)
	}
	if calls != 1 {
		t.Errorf("calls = %d, want 1", calls)
	}
}

// TestFetchModelsIntlRaw500IsNotConfusedWithFault 记录真实故障形态：
// 若绕过能力检查直接打到国际站，拿到的是裸 HTML 500（非 JSON 包络）。
// 这里固化该形态，说明"为何必须靠 Profile 能力位拦截而非解析响应"。
func TestFetchModelsIntlRaw500IsNotConfusedWithFault(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		w.Write([]byte("<html>\r\n<head><title>500 Internal Server Error</title></head>\r\n" +
			"<body>\r\n<center><h1>500 Internal Server Error</h1></center>\r\n" +
			"<hr><center>openresty</center>\r\n</body>\r\n</html>\r\n"))
	}))
	defer srv.Close()

	// 强制用一个"声称支持"的 Profile 绕过能力位，模拟未加固的调用路径。
	c := &Client{HTTP: srv.Client(), ChatBaseCN: srv.URL}
	c.SetRegionOverrides(RegionOverrides{}, RegionOverrides{})
	intl := &auth.Auth{UID: "intl-1", AccessToken: "at", Edition: auth.RegionINTL}

	// 正常路径被能力位拦下（不发请求）；这里直接验证错误文案形态，
	// 以便理解"裸 HTML 会被原样透出给用户"这一体验问题。
	if _, err := c.FetchModels(intl); !errors.Is(err, ErrModelsUnsupported) {
		t.Fatalf("err = %v, want ErrModelsUnsupported", err)
	}
	if err := c.rawModelsProbe(srv.URL); err == nil {
		t.Fatal("裸 500 探测应返回错误")
	} else if !strings.Contains(err.Error(), "500") {
		t.Errorf("错误应含状态码: %v", err)
	}
}

// rawModelsProbe 直接打一次模型端点（绕过能力位），仅测试用。
func (c *Client) rawModelsProbe(base string) error {
	req, err := http.NewRequest(http.MethodGet, base+"/console/enterprises/personal/models", nil)
	if err != nil {
		return err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return errors.New("models api status " + resp.Status)
	}
	return nil
}
