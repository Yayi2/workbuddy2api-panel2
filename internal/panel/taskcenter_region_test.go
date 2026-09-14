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

// taskCenterPanel 构造一个带「国内站 + 国际站」混挂池的面板。
// 假上游记录每次被请求的账号，据此断言国际站账号是否被任务中心访问过。
func taskCenterPanel(t *testing.T, hits *[]string) (*Panel, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits != nil {
			*hits = append(*hits, r.Header.Get("Authorization"))
		}
		// 返回一份"国内站 schema"的任务列表：国际站若被访问，会解析出空 task_code。
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"code":0,"msg":"OK","data":{"tasks":[
		  {"task_code":"chat_5","title":"t","progress":{"current":0,"target":5},"accept_status":"not_accepted","reward_credit":100}
		],"in_period":false}}`))
	}))
	t.Cleanup(srv.Close)

	p := pool.New("")
	p.Add(&auth.Auth{UID: "cn-1", AccessToken: "at-cn", Edition: auth.RegionCN})
	p.Add(&auth.Auth{UID: "intl-1", AccessToken: "at-intl", Edition: auth.RegionINTL})

	up := upstream.New()
	up.ChatBaseCN = srv.URL
	up.BillingBaseCN = srv.URL
	up.WebBaseCN = srv.URL
	return New(Config{Pool: p, Upstream: up, Version: "test"}), srv
}

// TestTaskScanExcludesIntl 任务中心扫描必须**跳过国际站账号**。
//
// 依据：两站 growth tasks 是不同 schema——
//
//	国内站 {task_code, progress{current,target}, reward_credit, accept_status}
//	国际站 {task_id, code, title, status}（无 progress / 无 reward）
//
// 本项目按国内站 schema 解析，国际站条目会变成 task_code 为空、进度恒 0 的幽灵待办：
// 面板列出点不动的任务，队列还会拿空 task_code 去接受/领奖（必然失败）。
func TestTaskScanExcludesIntl(t *testing.T) {
	var hits []string
	p, _ := taskCenterPanel(t, &hits)

	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, httptest.NewRequest("POST", "/panel/api/tasks/scan_all", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, body=%s", rec.Code, rec.Body.String())
	}

	// 关键的**直接证据**：上游只应被国内站账号访问过。
	for _, h := range hits {
		if h == "Bearer at-intl" {
			t.Fatalf("国际站账号被任务中心访问了（hits=%v）——应为仅国内站", hits)
		}
	}
	if len(hits) == 0 {
		t.Fatal("国内站账号应被扫描到")
	}

	// 返回的账号清单里不得出现国际站账号。
	var body struct {
		Accounts []struct {
			UID string `json:"uid"`
		} `json:"accounts"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	for _, a := range body.Accounts {
		if a.UID == "intl-1" {
			t.Error("扫描结果不应包含国际站账号")
		}
	}
	if len(body.Accounts) != 1 || body.Accounts[0].UID != "cn-1" {
		t.Errorf("应只返回国内站账号，got %+v", body.Accounts)
	}
}

// TestSchoolStatusExcludesIntl 开学季状态视图必须跳过国际站账号。
func TestSchoolStatusExcludesIntl(t *testing.T) {
	var hits []string
	p, _ := taskCenterPanel(t, &hits)

	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, httptest.NewRequest("GET", "/panel/api/school/status", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, body=%s", rec.Code, rec.Body.String())
	}
	for _, h := range hits {
		if h == "Bearer at-intl" {
			t.Fatalf("国际站账号被开学季视图访问了（hits=%v）", hits)
		}
	}
	var body struct {
		Accounts []struct {
			UID string `json:"uid"`
		} `json:"accounts"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	for _, a := range body.Accounts {
		if a.UID == "intl-1" {
			t.Error("开学季视图不应包含国际站账号")
		}
	}
}

// TestQueueRunExcludesIntl 执行队列必须跳过国际站账号（否则会入队空 task_code）。
func TestQueueRunExcludesIntl(t *testing.T) {
	var hits []string
	p, _ := taskCenterPanel(t, &hits)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/panel/api/tasks/run_queue",
		strings.NewReader(`{"growth":true,"school":false,"concurrency":1}`))
	req.Header.Set("Content-Type", "application/json")
	p.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, body=%s", rec.Code, rec.Body.String())
	}
	for _, h := range hits {
		if h == "Bearer at-intl" {
			t.Fatalf("国际站账号被队列访问了（hits=%v）", hits)
		}
	}

	// 队列内容里不得出现国际站 uid。
	qrec := httptest.NewRecorder()
	p.ServeHTTP(qrec, httptest.NewRequest("GET", "/panel/api/tasks/queue", nil))
	if strings.Contains(qrec.Body.String(), "intl-1") {
		t.Errorf("队列不应包含国际站账号: %s", qrec.Body.String())
	}
}

// TestRegionSupportsGrowthTasks 站点适用性判定的口径（本功能的唯一依据）。
func TestRegionSupportsGrowthTasks(t *testing.T) {
	if !regionSupportsGrowthTasks(&auth.Auth{UID: "c", Edition: auth.RegionCN}) {
		t.Error("国内站应支持成长任务")
	}
	if regionSupportsGrowthTasks(&auth.Auth{UID: "i", Edition: auth.RegionINTL}) {
		t.Error("国际站**不应**支持成长任务（schema 不同，会产出幽灵待办）")
	}
	// 老账号（无 edition）按国内站处理 —— 向后兼容。
	if !regionSupportsGrowthTasks(&auth.Auth{UID: "legacy"}) {
		t.Error("无 edition 的老账号应按国内站处理")
	}
	// nil 安全。
	if regionSupportsGrowthTasks(nil) {
		t.Error("nil 账号应返回 false")
	}
}

// TestIntlStillUsableForNonTaskFeatures 排除的是**任务**，不是整个账号。
//
// 国际站账号仍应能：对话（走选号）、查余额、保活。
// 这个测试防止有人把"任务中心排除"误做成"国际站账号全局禁用"。
func TestIntlStillUsableForNonTaskFeatures(t *testing.T) {
	var hits []string
	p, _ := taskCenterPanel(t, &hits)

	// 单号余额刷新应覆盖国际站账号 —— 国际站计费域可用（Profile.BalanceAPI）。
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, httptest.NewRequest("POST", "/panel/api/accounts/intl-1/balance", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("国际站余额刷新 code = %d, body=%s（应可用）", rec.Code, rec.Body.String())
	}
	sawIntl := false
	for _, h := range hits {
		if h == "Bearer at-intl" {
			sawIntl = true
		}
	}
	if !sawIntl {
		t.Error("余额刷新应实际请求国际站账号")
	}

	// 国际站账号在池中仍应是可选的（选号不受任务中心影响）。
	if a := p.cfg.Pool.PickInRegion(auth.RegionINTL, nil, ""); a == nil {
		t.Error("国际站账号应仍参与选号")
	}

	// 而**任务**接口对国际站账号仍应明确拒绝（而非静默返回幽灵条目）。
	trec := httptest.NewRecorder()
	p.ServeHTTP(trec, httptest.NewRequest("GET", "/panel/api/accounts/intl-1/tasks", nil))
	if trec.Code != http.StatusBadRequest {
		t.Errorf("国际站任务查询应 400（明确拒绝），got %d", trec.Code)
	}
}
