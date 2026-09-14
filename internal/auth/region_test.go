package auth

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestNormalizeRegion 站点标识归一化：别名表必须与 workbuddy-gateway 的
// profileForEdition 逐项一致，否则两个项目对同一份凭据文件的解读会分叉。
func TestNormalizeRegion(t *testing.T) {
	intl := []string{"intl", "INTL", " Intl ", "international", "global", "workbuddy.ai", "WorkBuddy.AI"}
	for _, s := range intl {
		if got := NormalizeRegion(s); got != RegionINTL {
			t.Errorf("NormalizeRegion(%q) = %q, want %q", s, got, RegionINTL)
		}
	}
	cn := []string{"", "  ", "cn", "CN", "VSCode", "unknown", "tencent"}
	for _, s := range cn {
		if got := NormalizeRegion(s); got != RegionCN {
			t.Errorf("NormalizeRegion(%q) = %q, want %q", s, got, RegionCN)
		}
	}
}

// TestParseRegionNestedForm 嵌套形（插件 OAuth 输出）顶层 edition 必须被读取。
func TestParseRegionNestedForm(t *testing.T) {
	raw := []byte(`{
	  "auth": {"accessToken": "at-intl", "refreshToken": "rt", "expiresAt": 1800000000, "domain": "www.workbuddy.ai"},
	  "account": {"uid": "u-intl", "enterpriseId": "ent", "nickname": "Intl User"},
	  "edition": "intl"
	}`)
	a, err := Parse(raw)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if a.Edition != "intl" {
		t.Errorf("Edition = %q, want %q", a.Edition, "intl")
	}
	if !a.IsIntl() {
		t.Error("IsIntl() = false, want true")
	}
	if a.Region() != RegionINTL {
		t.Errorf("Region() = %q, want %q", a.Region(), RegionINTL)
	}
}

// TestParseRegionFlatForm 扁平形（手写/旧版）同样要取到 edition。
func TestParseRegionFlatForm(t *testing.T) {
	raw := []byte(`{"accessToken":"at","refreshToken":"rt","expiresAt":1800000000,"uid":"u1","edition":"global"}`)
	a, err := Parse(raw)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if a.Region() != RegionINTL {
		t.Errorf("Region() = %q, want %q（global 是 intl 的别名）", a.Region(), RegionINTL)
	}
}

// TestParseLegacyFileDefaultsToCN 无 edition 的老凭据文件必须回退国内站——
// 这是"未标注即历史 CN 账号"的安全默认，保证引入本特性前的老部署行为不变。
func TestParseLegacyFileDefaultsToCN(t *testing.T) {
	raw := []byte(`{
	  "auth": {"accessToken": "at-cn", "refreshToken": "rt", "expiresAt": 1800000000},
	  "account": {"uid": "u-cn", "nickname": "Legacy"}
	}`)
	a, err := Parse(raw)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if a.Edition != "" {
		t.Errorf("Edition = %q, want empty（磁盘上确实没有该字段）", a.Edition)
	}
	if a.Region() != RegionCN {
		t.Errorf("Region() = %q, want %q", a.Region(), RegionCN)
	}
	if a.IsIntl() {
		t.Error("IsIntl() = true, want false")
	}
}

// TestNilAuthRegionIsCN nil 账号不得 panic（池内竞态/已移除条目会走到这里）。
func TestNilAuthRegionIsCN(t *testing.T) {
	var a *Auth
	if a.Region() != RegionCN {
		t.Errorf("nil.Region() = %q, want %q", a.Region(), RegionCN)
	}
	if a.IsIntl() {
		t.Error("nil.IsIntl() = true, want false")
	}
}

// TestSaveAtomicPreservesEdition 原子写回必须保留站点标识。
//
// 为什么关键：refresh 路径会调 SaveAtomic 重写整个文件。若写回时丢掉 edition，
// 国际站账号下次启动就会被当成国内站账号路由到 copilot.tencent.com，
// 表现为"刷新一次 token 后账号突然全部 401"——且 workbuddy-gateway 也无法再识别它。
func TestSaveAtomicPreservesEdition(t *testing.T) {
	for _, region := range []string{RegionCN, RegionINTL} {
		dir := t.TempDir()
		fp := filepath.Join(dir, "workbuddy-"+region+".json")
		a := &Auth{
			AccessToken: "at", RefreshToken: "rt", ExpiresAt: 1800000000,
			UID: "u1", Nickname: "n", Edition: region, FilePath: fp,
		}
		if err := a.SaveAtomic(); err != nil {
			t.Fatalf("SaveAtomic(%s): %v", region, err)
		}
		b, err := os.ReadFile(fp)
		if err != nil {
			t.Fatal(err)
		}
		var doc map[string]any
		if err := json.Unmarshal(b, &doc); err != nil {
			t.Fatal(err)
		}
		if got, _ := doc["edition"].(string); got != region {
			t.Errorf("saved edition = %v, want %q", doc["edition"], region)
		}
		// 落盘文件必须能被自己重新解析回同一站点。
		back, err := Parse(b)
		if err != nil {
			t.Fatalf("re-parse saved file: %v", err)
		}
		if back.Region() != region {
			t.Errorf("round-trip Region() = %q, want %q", back.Region(), region)
		}
	}
}

// TestSaveAtomicLegacyEmptyEditionWritesCN 老账号（Edition 为空）写回时补写 cn，
// 使文件从"未标注"变为"显式国内站"，同时对 workbuddy-gateway 保持可读。
func TestSaveAtomicLegacyEmptyEditionWritesCN(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "workbuddy-legacy.json")
	a := &Auth{AccessToken: "at", UID: "u1", FilePath: fp} // Edition 留空
	if err := a.SaveAtomic(); err != nil {
		t.Fatalf("SaveAtomic: %v", err)
	}
	b, _ := os.ReadFile(fp)
	var doc map[string]any
	_ = json.Unmarshal(b, &doc)
	if got, _ := doc["edition"].(string); got != RegionCN {
		t.Errorf("edition = %v, want %q", doc["edition"], RegionCN)
	}
}

// TestInteropWithGatewayCredential 与 workbuddy-gateway 的凭据格式互通。
//
// 样例取自 CangShui/workbuddy-gateway 的 StoredAuth 形状：
//
//	type StoredAuth struct {
//	    Auth    StoredTokens  `json:"auth"`
//	    Account StoredAccount `json:"account"`
//	    Edition string        `json:"edition,omitempty"`
//	}
//
// 本网关必须能直接加载该文件并识别为国际站——这是"两个项目共用 auths/ 目录"
// 的硬性契约，一旦字段名或层级漂移，混挂部署会静默把国际站账号当国内站用。
func TestInteropWithGatewayCredential(t *testing.T) {
	// 逐字复刻 workbuddy-gateway saveAuth 产出的 JSON 形状。
	gatewayFile := []byte(`{
  "auth": {
    "accessToken": "gw-access",
    "refreshToken": "gw-refresh",
    "expiresAt": 1799999999,
    "domain": "www.workbuddy.ai"
  },
  "account": {
    "uid": "gw-uid-1",
    "enterpriseId": "gw-ent",
    "nickname": "Gateway User"
  },
  "edition": "intl"
}`)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "workbuddy-intl.json"), gatewayFile, 0o600); err != nil {
		t.Fatal(err)
	}
	// LoadDir 必须发现它（workbuddy*.json glob）并识别站点。
	as, err := LoadDir(dir)
	if err != nil {
		t.Fatalf("LoadDir: %v", err)
	}
	if len(as) != 1 {
		t.Fatalf("LoadDir found %d accounts, want 1", len(as))
	}
	got := as[0]
	if got.UID != "gw-uid-1" || got.AccessToken != "gw-access" {
		t.Errorf("解析字段错位: uid=%q token=%q", got.UID, got.AccessToken)
	}
	if !got.IsIntl() {
		t.Errorf("Region = %q, want intl（workbuddy-gateway 的国际站凭据必须被识别）", got.Region())
	}
}
