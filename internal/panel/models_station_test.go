package panel

import (
	"net/http"
	"strings"
	"testing"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
)

// TestModelsStationSwitch 面板模型页的站点切换契约。
//
// 两个站点的数据来源本质不同：
//   - 国内站：实时查询上游（source=live，带积分倍率/思考档位等元数据）；
//   - 国际站：无该接口，返回**静态清单**（source=static），不打上游。
//
// 国际站静态返回的关键性质：**不需要池中有国内站账号**。若不这样做，
// 纯国际站部署的用户在模型页会只看到"请添加国内站账号"，而看不到国际站
// 实际可用的模型（deepseek-v4.1-flash 等）。
func TestModelsStationSwitch(t *testing.T) {
	// 纯国际站池。
	p := modelsTestPanel(t, "",
		&auth.Auth{UID: "intl-1", AccessToken: "at-i", Edition: auth.RegionINTL},
	)

	// 国际站：200 + static + 含国际站命名。
	code, body := modelsJSON(t, p, "?region=intl")
	if code != http.StatusOK {
		t.Fatalf("国际站 code = %d, want 200", code)
	}
	if src, _ := body["source"].(string); src != "static" {
		t.Errorf("国际站 source = %q, want static", src)
	}
	if rg, _ := body["region"].(string); rg != auth.RegionINTL {
		t.Errorf("region = %q, want intl", rg)
	}
	ids := modelIDsOf(t, body)
	if !ids["deepseek-v4.1-flash"] {
		t.Error("国际站应含 deepseek-v4.1-flash")
	}
	if ids["deepseek-v4-pro"] {
		t.Error("国际站不应含 deepseek-v4-pro（该站 11102）")
	}

	// 国内站：无国内站账号 → 503（而非静态兜底，因国内站有实时接口，值得明确提示）。
	code2, body2 := modelsJSON(t, p, "?region=cn")
	if code2 != http.StatusServiceUnavailable {
		t.Fatalf("无国内站账号时 code = %d, want 503", code2)
	}
	if msg, _ := body2["error"].(string); !strings.Contains(msg, "国内站") {
		t.Errorf("应提示需要国内站账号: %q", msg)
	}
}

// TestModelsRegionEcho 响应必须回显 region，供前端确认当前展示的是哪个站点。
func TestModelsRegionEcho(t *testing.T) {
	p := modelsTestPanel(t, "",
		&auth.Auth{UID: "intl-1", AccessToken: "at-i", Edition: auth.RegionINTL},
	)
	_, body := modelsJSON(t, p, "?region=intl")
	if got, _ := body["region"].(string); got != auth.RegionINTL {
		t.Errorf("region = %q, want intl", got)
	}
	// 非法 region 回退不限 → 缺省国内站。
	p2 := modelsTestPanel(t, "")
	_, body2 := modelsJSON(t, p2, "?region=bogus")
	if got, _ := body2["region"].(string); got != auth.RegionCN {
		t.Errorf("非法 region 应回退 cn（缺省国内站），got %q", got)
	}
}

// TestModelsIntlStaticHasNoLiveMetadata 国际站静态清单不得伪造元数据。
//
// 静态清单没有积分倍率与思考档位；若编造（或误填成"不支持思考"），
// 用户会照着一个错误的档位表配客户端。故这里断言这些字段为空，
// 前端据此显示「—」而非误导性文案。
func TestModelsIntlStaticHasNoLiveMetadata(t *testing.T) {
	p := modelsTestPanel(t, "",
		&auth.Auth{UID: "intl-1", AccessToken: "at-i", Edition: auth.RegionINTL},
	)
	_, body := modelsJSON(t, p, "?region=intl")
	list, _ := body["models"].([]any)
	if len(list) == 0 {
		t.Fatal("静态清单为空")
	}
	for _, raw := range list {
		m, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if m["credits"] != nil && m["credits"] != "" {
			t.Errorf("%v 静态清单不应带积分倍率: %v", m["id"], m["credits"])
		}
		if m["default_effort"] != nil && m["default_effort"] != "" {
			t.Errorf("%v 静态清单不应带默认档: %v", m["id"], m["default_effort"])
		}
		if eff, ok := m["supported_efforts"].([]any); ok && len(eff) > 0 {
			t.Errorf("%v 静态清单不应带思考档位: %v", m["id"], eff)
		}
	}
}

// modelIDsOf 从响应体提取模型 id 集合。
func modelIDsOf(t *testing.T, body map[string]any) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	list, _ := body["models"].([]any)
	for _, raw := range list {
		if m, ok := raw.(map[string]any); ok {
			if id, ok := m["id"].(string); ok {
				out[id] = true
			}
		}
	}
	return out
}
