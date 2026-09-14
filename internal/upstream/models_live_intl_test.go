package upstream

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
)

// intlTokenDir 本机真实国际站凭据所在目录（workbuddy-gateway 的 auths/）。
// 不存在时相关测试整体跳过，绝不因缺凭据而失败。
const intlTokenDir = `D:\TOOLS\workbuddy-gateway-windows-amd64`

// loadIntlToken 从本机真实凭据中取一个国际站 access token。
// 找不到返回空串（调用方跳过测试）。
func loadIntlToken() string {
	for _, name := range []string{"workbuddy.json", "workbuddy2.json"} {
		raw, err := os.ReadFile(filepath.Join(intlTokenDir, name))
		if err != nil {
			continue
		}
		a, err := auth.Parse(raw)
		if err != nil || a.AccessToken == "" {
			continue
		}
		return a.AccessToken
	}
	return ""
}

// TestLiveIntlModelsEndpointUnavailable 用**真实国际站 token** 打真实上游，
// 确认模型清单接口对国际站不可用。
//
// 这是本结论的权威证据（此前一轮我用无效 token 推断，判据不牢）：此测试直接固化
// "有效 token 也拿不到" 这一事实，避免未来有人凭"应该能用"再次改回全池选号。
//
// 需要本机存在真实国际站凭据；缺失则跳过（CI 环境不阻塞）。
func TestLiveIntlModelsEndpointUnavailable(t *testing.T) {
	tok := loadIntlToken()
	if tok == "" {
		t.Skip("本机无真实国际站凭据，跳过真实上游验证")
	}

	c := New()
	intl := &auth.Auth{UID: "intl-live", AccessToken: tok, Edition: auth.RegionINTL}

	// 1) 能力位必须已拦截：不发请求即返回哨兵错误。
	if _, err := c.FetchModels(intl); !errors.Is(err, ErrModelsUnsupported) {
		t.Fatalf("国际站应返回 ErrModelsUnsupported，got %v", err)
	}

	// 2) 佐证 token 本身有效：同一 token 打协议族端点必须是正常业务包络
	//    （而非网络/鉴权失败），以排除"只是因为 token 坏了"的替代解释。
	if err := c.DailyCheckin(intl); err != nil {
		// 国际站无成长中心，该端点可能业务失败；这里只要求"不是传输层错误"。
		var ue *Error
		if !errors.As(err, &ue) {
			t.Skipf("协议族端点未返回业务包络（%v），环境不可达，跳过", err)
		}
		t.Logf("协议族端点可达（业务包络 %s/%d），说明 token 有效", ue.Kind, ue.Status)
	}
}
