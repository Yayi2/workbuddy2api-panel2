package panel

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
	"github.com/linguo2625469/workbuddy2api-panel/internal/pool"
)

// orderPanel 构造带账号的面板（不鉴权，便于直接调用）。
func orderPanel(t *testing.T, uids ...string) *Panel {
	t.Helper()
	p := pool.New("")
	for _, u := range uids {
		p.Add(&auth.Auth{UID: u, AccessToken: "at-" + u, Edition: auth.RegionCN})
	}
	return New(Config{Pool: p, Version: "test"})
}

// orderGet 调 GET /panel/api/pool/order。
func orderGet(t *testing.T, p *Panel) (int, map[string]any) {
	t.Helper()
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, httptest.NewRequest("GET", "/panel/api/pool/order", nil))
	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	return rec.Code, body
}

// orderPost 调 POST 并返回状态码与响应体。
func orderPost(t *testing.T, p *Panel, path, payload string) (int, map[string]any) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", path, strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	p.ServeHTTP(rec, req)
	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	return rec.Code, body
}

// orderUIDs 从响应里取顺序 uid 列表。
func orderUIDs(t *testing.T, body map[string]any) []string {
	t.Helper()
	raw, _ := body["order"].([]any)
	out := make([]string, 0, len(raw))
	for _, r := range raw {
		// GET 返回 [{uid,...}]，POST 返回 ["uid",...] —— 两种形状都兼容。
		switch v := r.(type) {
		case string:
			out = append(out, v)
		case map[string]any:
			if uid, ok := v["uid"].(string); ok {
				out = append(out, uid)
			}
		}
	}
	return out
}

// TestPoolOrderGetShape GET 返回顺序（含账号元数据）+ 策略 + 可用策略清单。
func TestPoolOrderGetShape(t *testing.T) {
	p := orderPanel(t, "u1", "u2")
	code, body := orderGet(t, p)
	if code != http.StatusOK {
		t.Fatalf("code = %d, want 200", code)
	}
	if got := orderUIDs(t, body); len(got) != 2 {
		t.Errorf("order 长度 = %d, want 2", len(got))
	}
	// 默认策略 weighted，且 effective=false（顺序不参与选号）。
	if s, _ := body["strategy"].(string); s != pool.StrategyWeighted {
		t.Errorf("默认策略 = %q, want weighted", s)
	}
	if eff, _ := body["effective"].(bool); eff {
		t.Error("weighted 下 effective 应为 false（顺序不参与选号）")
	}
	strats, _ := body["strategies"].([]any)
	if len(strats) != 3 {
		t.Errorf("应透出 3 种策略，got %d", len(strats))
	}
}

// TestPoolOrderMove 调整顺序：to / delta 两种用法。
func TestPoolOrderMove(t *testing.T) {
	p := orderPanel(t, "a", "b", "c")

	// 初始按 uid 升序：a b c。把 c 移到最前。
	code, body := orderPost(t, p, "/panel/api/pool/order", `{"uid":"c","to":0}`)
	if code != http.StatusOK {
		t.Fatalf("code = %d, body=%v", code, body)
	}
	if got := orderUIDs(t, body); len(got) != 3 || got[0] != "c" {
		t.Fatalf("移到最前后顺序 = %v, want c 在前", got)
	}

	// 相对下移 c：c 应回到中间。
	_, body = orderPost(t, p, "/panel/api/pool/order", `{"uid":"c","delta":1}`)
	if got := orderUIDs(t, body); got[1] != "c" {
		t.Errorf("下移后顺序 = %v, want c 在中间", got)
	}

	// 上移。
	_, body = orderPost(t, p, "/panel/api/pool/order", `{"uid":"c","delta":-1}`)
	if got := orderUIDs(t, body); got[0] != "c" {
		t.Errorf("上移后顺序 = %v, want c 在最前", got)
	}
}

// TestPoolOrderReset 恢复默认顺序。
func TestPoolOrderReset(t *testing.T) {
	p := orderPanel(t, "d1", "d2")
	orderPost(t, p, "/panel/api/pool/order", `{"uid":"d2","to":0}`)
	code, body := orderPost(t, p, "/panel/api/pool/order", `{"reset":true}`)
	if code != http.StatusOK {
		t.Fatalf("code = %d", code)
	}
	got := orderUIDs(t, body)
	if len(got) != 2 || got[0] != "d1" {
		t.Errorf("重置后顺序 = %v, want d1 在前（uid 升序）", got)
	}
}

// TestPoolOrderUnknownUID 不存在的 uid → 404。
func TestPoolOrderUnknownUID(t *testing.T) {
	p := orderPanel(t, "e1")
	code, _ := orderPost(t, p, "/panel/api/pool/order", `{"uid":"nope","to":0}`)
	if code != http.StatusNotFound {
		t.Errorf("code = %d, want 404", code)
	}
}

// TestPoolOrderBadRequest 参数缺失/非法 JSON → 400（而不是 500 或静默成功）。
func TestPoolOrderBadRequest(t *testing.T) {
	p := orderPanel(t, "f1")
	if code, _ := orderPost(t, p, "/panel/api/pool/order", `{bad json`); code != http.StatusBadRequest {
		t.Errorf("非法 JSON: code = %d, want 400", code)
	}
	// 只有 uid、没有 to/delta/reset。
	if code, _ := orderPost(t, p, "/panel/api/pool/order", `{"uid":"f1"}`); code != http.StatusBadRequest {
		t.Errorf("缺参数: code = %d, want 400", code)
	}
	if code, _ := orderPost(t, p, "/panel/api/pool/order", `{"to":0}`); code != http.StatusBadRequest {
		t.Errorf("缺 uid: code = %d, want 400", code)
	}
}

// TestPoolOrderMessageWhenWeighted weighted 下调整顺序必须提示"顺序不参与选号"。
//
// 这是防困惑的关键：用户排了序却看不到效果，若不给提示会以为功能坏了。
func TestPoolOrderMessageWhenWeighted(t *testing.T) {
	p := orderPanel(t, "g1", "g2")
	_, body := orderPost(t, p, "/panel/api/pool/order", `{"uid":"g2","to":0}`)
	msg, _ := body["message"].(string)
	if !strings.Contains(msg, "加权随机") {
		t.Errorf("weighted 下应提示顺序暂不参与选号，got %q", msg)
	}
	// 切到 priority 后不应再有该提示。
	orderPost(t, p, "/panel/api/pool/strategy", `{"strategy":"priority"}`)
	_, body2 := orderPost(t, p, "/panel/api/pool/order", `{"uid":"g2","to":0}`)
	if msg2, ok := body2["message"].(string); ok && strings.Contains(msg2, "加权随机") {
		t.Errorf("priority 下不应再提示顺序无效: %q", msg2)
	}
	if eff, _ := body2["effective"].(bool); !eff {
		t.Error("priority 下 effective 应为 true")
	}
}

// TestPoolStrategySwitch 策略切换：归一化、生效、非法值回退默认。
func TestPoolStrategySwitch(t *testing.T) {
	p := orderPanel(t, "h1", "h2")

	for _, tc := range []struct{ in, want string }{
		{"priority", pool.StrategyPriority},
		{"round_robin", pool.StrategyRoundRobin},
		{"prio", pool.StrategyPriority},
		{"rotate", pool.StrategyRoundRobin},
		{"weighted", pool.StrategyWeighted},
		{"bogus", pool.StrategyWeighted}, // 未知回退默认
		{"", pool.StrategyWeighted},
	} {
		code, body := orderPost(t, p, "/panel/api/pool/strategy", `{"strategy":"`+tc.in+`"}`)
		if code != http.StatusOK {
			t.Fatalf("strategy=%q code = %d", tc.in, code)
		}
		if got, _ := body["strategy"].(string); got != tc.want {
			t.Errorf("strategy=%q 归一为 %q, want %q", tc.in, got, tc.want)
		}
		// 池内实际策略必须同步。
		if got := p.cfg.Pool.Strategy(); got != tc.want {
			t.Errorf("策略 %q 未同步到池，got %q", tc.in, got)
		}
	}
}

// TestPoolStrategyBadJSON 非法 JSON → 400。
func TestPoolStrategyBadJSON(t *testing.T) {
	p := orderPanel(t, "i1")
	if code, _ := orderPost(t, p, "/panel/api/pool/strategy", `{oops`); code != http.StatusBadRequest {
		t.Errorf("code = %d, want 400", code)
	}
}

// TestPoolOrderReflectsPickBehavior 端到端：面板排序后，pool 的选号行为随之改变。
//
// 这是本功能的核心价值断言——面板排的序必须真正影响选号，而不只是存下来。
func TestPoolOrderReflectsPickBehavior(t *testing.T) {
	p := orderPanel(t, "j1", "j2", "j3")
	// 切到 priority 并把 j3 置顶。
	orderPost(t, p, "/panel/api/pool/strategy", `{"strategy":"priority"}`)
	orderPost(t, p, "/panel/api/pool/order", `{"uid":"j3","to":0}`)

	a := p.cfg.Pool.Pick()
	if a == nil {
		t.Fatal("应有可用账号")
	}
	if a.UID != "j3" {
		t.Errorf("置顶后应优先选 j3，got %q", a.UID)
	}

	// 再置顶 j2 → 应改用 j2。
	orderPost(t, p, "/panel/api/pool/order", `{"uid":"j2","to":0}`)
	if a := p.cfg.Pool.Pick(); a == nil || a.UID != "j2" {
		t.Errorf("重新置顶后应优先选 j2，got %v", a)
	}
}

// TestPoolOrderAuthRequired 顺序接口必须走鉴权（与其他 /panel/api/* 同口径）。
func TestPoolOrderAuthRequired(t *testing.T) {
	p := New(Config{Version: "test", APIKey: "sk-test"})
	for _, c := range []struct{ method, path, body string }{
		{"GET", "/panel/api/pool/order", ""},
		{"POST", "/panel/api/pool/order", `{"uid":"x","to":0}`},
		{"POST", "/panel/api/pool/strategy", `{"strategy":"priority"}`},
	} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(c.method, c.path, strings.NewReader(c.body))
		p.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s 无密钥 code = %d, want 401", c.method, c.path, rec.Code)
		}
	}
}
