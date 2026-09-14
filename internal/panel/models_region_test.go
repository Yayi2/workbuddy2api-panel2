package panel

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
	"github.com/linguo2625469/workbuddy2api-panel/internal/pool"
	"github.com/linguo2625469/workbuddy2api-panel/internal/upstream"
)

// modelsTestPanel 构造带池与假上游的面板（不鉴权，便于直接调用）。
//
// 用 upstream.New() 而非 &upstream.Client{}：后者 HTTP 为 nil，真实请求会 panic。
// 仅覆盖 ChatBaseCN 指向 httptest 假服务器（非空优先于 Profile），
// 与生产路径的差异仅在于域名。
func modelsTestPanel(t *testing.T, base string, accounts ...*auth.Auth) *Panel {
	t.Helper()
	p := pool.New("")
	for _, a := range accounts {
		p.Add(a)
		if a.Region() != auth.RegionINTL {
			p.SetCredits(a.UID, 1000, 0)
		}
	}
	up := upstream.New()
	if base != "" {
		up.ChatBaseCN = base
	}
	return New(Config{Pool: p, Upstream: up, Version: "test"})
}

// modelsJSON 调 /panel/api/models 并解析响应。
func modelsJSON(t *testing.T, p *Panel, query string) (int, map[string]any) {
	t.Helper()
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, httptest.NewRequest("GET", "/panel/api/models"+query, nil))
	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	return rec.Code, body
}

// TestModelsPicksCNEvenWithIntlInPool 混挂池下「模型与档位」必须只走国内站账号。
//
// 这是本 bug 的回归测试：原实现用 Pool.Pick() 全池随机选号，池中一旦有国际站
// 账号，约一半概率选到它 → 打到国际站未挂载的 /console/enterprises/personal/models
// → 上游返回裸 HTML `500 Internal Server Error` → 整个模型页报错。
func TestModelsPicksCNEvenWithIntlInPool(t *testing.T) {
	var hits []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits = append(hits, r.Header.Get("Authorization"))
		w.Write([]byte(`{"code":0,"data":{"models":[{"id":"glm-5.2","name":"GLM-5.2"}],
		  "agents":[{"name":"cli","models":["glm-5.2"]}]}}`))
	}))
	defer srv.Close()

	p := modelsTestPanel(t, srv.URL,
		&auth.Auth{UID: "cn-1", AccessToken: "at-cn", Edition: auth.RegionCN},
		&auth.Auth{UID: "intl-1", AccessToken: "at-intl", Edition: auth.RegionINTL},
	)

	// 多次调用：每次都必须是国内站账号（原实现会随机命中 at-intl）。
	for i := 0; i < 20; i++ {
		code, body := modelsJSON(t, p, "")
		if code != http.StatusOK {
			t.Fatalf("第 %d 次 code = %d, body=%v", i, code, body)
		}
	}
	if len(hits) == 0 {
		t.Fatal("应至少发起一次上游请求")
	}
	for _, h := range hits {
		if h != "Bearer at-cn" {
			t.Fatalf("模型页打到 %q，应固定使用国内站账号（Bearer at-cn）", h)
		}
	}
}

// TestModelsNoCNAccountGivesActionableMessage 池中只有国际站账号时，
// 必须给出「模型清单仅国内站提供」的可操作提示，而不是裸 HTML 500。
func TestModelsNoCNAccountGivesActionableMessage(t *testing.T) {
	p := modelsTestPanel(t, "",
		&auth.Auth{UID: "intl-1", AccessToken: "at-intl", Edition: auth.RegionINTL},
	)
	code, body := modelsJSON(t, p, "")
	if code != http.StatusServiceUnavailable {
		t.Fatalf("code = %d, want 503", code)
	}
	msg, _ := body["error"].(string)
	if !strings.Contains(msg, "国内站") {
		t.Errorf("错误应点明需要国内站账号: %q", msg)
	}
	// 不能退化成笼统的"没有可用账号"——那会让用户反复添加国际站账号。
	if strings.Contains(msg, "请先在面板添加账号再查询") {
		t.Errorf("池中有账号却提示'没有账号'，会误导用户: %q", msg)
	}
}

// TestModelsEmptyPoolKeepsOriginalMessage 空池仍走原有提示（不回归）。
func TestModelsEmptyPoolKeepsOriginalMessage(t *testing.T) {
	p := modelsTestPanel(t, "")
	code, body := modelsJSON(t, p, "")
	if code != http.StatusServiceUnavailable {
		t.Fatalf("code = %d, want 503", code)
	}
	msg, _ := body["error"].(string)
	if !strings.Contains(msg, "先在面板添加账号") {
		t.Errorf("空池应提示先添加账号: %q", msg)
	}
}

// TestModelsIntlRegionReturnsStaticList 显式 ?region=intl 返回**静态清单**（200）。
//
// 国际站无模型清单接口，但面板要能查看该站可用模型，故不再返回 501，
// 而是给静态表并标注 source="static"（面板据此隐藏积分倍率/思考档等空列）。
// 关键：纯静态返回，**不打上游**——即便池中只有国际站账号也必须可用。
func TestModelsIntlRegionReturnsStaticList(t *testing.T) {
	p := modelsTestPanel(t, "",
		// 故意只放国际站账号：验证"国际站清单不依赖国内站账号"。
		&auth.Auth{UID: "intl-1", AccessToken: "at-i", Edition: auth.RegionINTL},
	)
	code, body := modelsJSON(t, p, "?region=intl")
	if code != http.StatusOK {
		t.Fatalf("code = %d, want 200（国际站给静态清单）", code)
	}
	if src, _ := body["source"].(string); src != "static" {
		t.Errorf("source = %q, want static", src)
	}
	list, _ := body["models"].([]any)
	if len(list) == 0 {
		t.Fatal("国际站静态清单不应为空")
	}
	ids := map[string]bool{}
	for _, m := range list {
		if mm, ok := m.(map[string]any); ok {
			ids[mm["id"].(string)] = true
		}
	}
	if !ids["deepseek-v4.1-flash"] {
		t.Error("国际站清单应含 deepseek-v4.1-flash（用户实际在用的模型）")
	}
	if ids["deepseek-v4-pro"] {
		t.Error("国际站清单不应含 deepseek-v4-pro（该站返回 11102）")
	}
}

// TestModelsDefaultRegionIsCN 不带 region 参数时缺省国内站（既有行为不变）。
func TestModelsDefaultRegionIsCN(t *testing.T) {
	var hit bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit = true
		w.Write([]byte(`{"code":0,"data":{"models":[{"id":"glm-5.2","name":"n"}],
		  "agents":[{"name":"cli","models":["glm-5.2"]}]}}`))
	}))
	defer srv.Close()

	p := modelsTestPanel(t, srv.URL,
		&auth.Auth{UID: "cn-1", AccessToken: "at-cn", Edition: auth.RegionCN},
	)
	code, body := modelsJSON(t, p, "") // 无 region 参数
	if code != http.StatusOK {
		t.Fatalf("code = %d", code)
	}
	if !hit {
		t.Error("缺省应走国内站**实时**查询（打上游）")
	}
	if src, _ := body["source"].(string); src != "live" {
		t.Errorf("source = %q, want live", src)
	}
	if rg, _ := body["region"].(string); rg != auth.RegionCN {
		t.Errorf("region = %q, want cn", rg)
	}
}

// TestModelsExplicitCNRegionStillWorks 显式 ?region=cn 照常工作。
func TestModelsExplicitCNRegionStillWorks(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"code":0,"data":{"models":[{"id":"glm-5.2","name":"n"}],
		  "agents":[{"name":"cli","models":["glm-5.2"]}]}}`))
	}))
	defer srv.Close()

	p := modelsTestPanel(t, srv.URL,
		&auth.Auth{UID: "cn-1", AccessToken: "at-cn", Edition: auth.RegionCN},
	)
	code, body := modelsJSON(t, p, "?region=cn")
	if code != http.StatusOK {
		t.Fatalf("code = %d, body=%v", code, body)
	}
	models, _ := body["models"].([]any)
	if len(models) != 1 {
		t.Errorf("models = %v, want 1 项", models)
	}
}

// TestRegionsEndpointReportsModelsAPI /panel/api/regions 必须透出模型清单能力位，
// 面板配置页据此渲染「模型清单」列。
func TestRegionsEndpointReportsModelsAPI(t *testing.T) {
	p := modelsTestPanel(t, "")
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, httptest.NewRequest("GET", "/panel/api/regions", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d", rec.Code)
	}
	var body struct {
		Regions []struct {
			Region    string `json:"region"`
			Growth    bool   `json:"growth"`
			ModelsAPI bool   `json:"models_api"`
		} `json:"regions"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Regions) != 2 {
		t.Fatalf("regions = %d, want 2", len(body.Regions))
	}
	cn, intl := body.Regions[0], body.Regions[1]
	if !cn.ModelsAPI {
		t.Error("国内站 models_api 应为 true")
	}
	if intl.ModelsAPI {
		t.Error("国际站 models_api 应为 false（未挂载该路由）")
	}
	if cn.Growth != true || intl.Growth != false {
		t.Errorf("growth 边界错误: cn=%v intl=%v", cn.Growth, intl.Growth)
	}
}
