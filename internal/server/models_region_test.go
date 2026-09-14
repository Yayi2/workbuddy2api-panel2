package server

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
	"github.com/linguo2625469/workbuddy2api-panel/internal/upstream"
)

// modelIDs 提取 /v1/models 返回的模型 id 集合。
func modelIDs(t *testing.T, h *Handler) map[string]bool {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/v1/models", nil))
	if rec.Code != 200 {
		t.Fatalf("code = %d", rec.Code)
	}
	var body struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	for _, m := range body.Data {
		out[m.ID] = true
	}
	return out
}

// TestStaticModelsPerStation 纯国际站部署时，/v1/models 静态回退必须用**国际站**清单。
//
// 为什么重要：两站的 deepseek 模型**命名不同**——
//
//	国际站：deepseek-v4.1-flash（带小版本号）、deepseek-v3
//	国内站：deepseek-v4-pro、deepseek-v4-flash
//
// 若把国内站清单发给国际站客户端，用户照着列表选 deepseek-v4-pro 会得到
// code=11102 `model [...] service info not found`，全部调用失败。
//
// （这一条曾判错：早期照搬国内站命名探测国际站，见 deepseek-v4-pro/flash 全部 11102，
// 据此误以为"国际站无 deepseek 系"；实为模型名不同，用户实际在用的正是
// deepseek-v4.1-flash。测试固化正确命名以防再次回归。）
func TestStaticModelsPerStation(t *testing.T) {
	// 场景一：纯国际站部署。
	intlOnly := testPoolWith(
		&auth.Auth{UID: "intl-1", AccessToken: "at-i", ExpiresAt: 9999999999, Edition: auth.RegionINTL},
	)
	hIntl := NewHandler(Config{Pool: intlOnly, Upstream: upstream.New()})
	ids := modelIDs(t, hIntl)

	// 国际站真实可用的 deepseek 命名必须出现。
	for _, want := range []string{"deepseek-v4.1-flash", "deepseek-v3", "glm-5.2", "kimi-k2.7", "hy4-preview"} {
		if !ids[want] {
			t.Errorf("国际站清单应含 %s", want)
		}
	}
	// 国内站的 deepseek 命名不得出现在国际站清单（会 11102）。
	for _, bad := range []string{"deepseek-v4-pro", "deepseek-v4-flash"} {
		if ids[bad] {
			t.Errorf("国际站清单不应含 %s（该站在国际站返回 code=11102）", bad)
		}
	}

	// 场景二：纯国内站部署 → 国内站清单（含国内站命名的 deepseek）。
	cnOnly := testPoolWith(
		&auth.Auth{UID: "cn-1", AccessToken: "at-c", ExpiresAt: 9999999999, Edition: auth.RegionCN},
	)
	hCN := NewHandler(Config{Pool: cnOnly, Upstream: upstream.New()})
	cnIDs := modelIDs(t, hCN)
	if !cnIDs["deepseek-v4-pro"] {
		t.Error("国内站清单应含 deepseek-v4-pro")
	}
	if cnIDs["deepseek-v4.1-flash"] {
		t.Error("国内站清单不应含 deepseek-v4.1-flash（那是国际站命名）")
	}

	// 场景三：混挂 → 用国内站清单（超集；国内站清单里的模型在国内站均可用）。
	mixed := testPoolWith(
		&auth.Auth{UID: "cn-1", AccessToken: "at-c", ExpiresAt: 9999999999, Edition: auth.RegionCN},
		&auth.Auth{UID: "intl-1", AccessToken: "at-i", ExpiresAt: 9999999999, Edition: auth.RegionINTL},
	)
	hMixed := NewHandler(Config{Pool: mixed, Upstream: upstream.New()})
	mixedIDs := modelIDs(t, hMixed)
	if !mixedIDs["deepseek-v4-pro"] {
		t.Error("混挂部署应回退国内站清单（含 deepseek）")
	}

	// 场景四：空池 → 国内站清单（保持既有行为，不回归）。
	empty := NewHandler(Config{Pool: testPoolWith(), Upstream: upstream.New()})
	if !modelIDs(t, empty)["deepseek-v4-pro"] {
		t.Error("空池应回退国内站清单（既有行为）")
	}
}

// TestStaticModelsNamingDivergence 固化两站 deepseek 命名分歧这一实测事实，
// 防止有人"顺手"把两个静态表合并或把国际站命名改成国内站命名。
func TestStaticModelsNamingDivergence(t *testing.T) {
	intlSet := map[string]bool{}
	for _, m := range intlStaticModels {
		intlSet[m.ID] = true
	}
	cnSet := map[string]bool{}
	for _, m := range staticModels {
		cnSet[m.ID] = true
	}

	// 国际站用带小版本号的命名；国内站的命名在国际站是 11102。
	if !intlSet["deepseek-v4.1-flash"] {
		t.Error("国际站静态表应含 deepseek-v4.1-flash（用户实际在用的模型）")
	}
	if intlSet["deepseek-v4-pro"] || intlSet["deepseek-v4-flash"] {
		t.Error("国际站静态表不得含国内站的 deepseek 命名（国际站返回 11102）")
	}
	// 国内站对照。
	if !cnSet["deepseek-v4-pro"] {
		t.Error("国内站静态表应含 deepseek-v4-pro")
	}
	if cnSet["deepseek-v4.1-flash"] {
		t.Error("国内站静态表不应含 deepseek-v4.1-flash（那是国际站命名）")
	}
	// 两表确实不同——若被合并，本测试的上一组断言会失败，此处再显式兜底。
	if len(intlSet) == len(cnSet) {
		for id := range intlSet {
			if !cnSet[id] {
				return // 集合不同，符合预期
			}
		}
		t.Error("两个站点的静态表不应完全相同（命名与可用性确有差异）")
	}
}
