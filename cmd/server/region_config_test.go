package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestRegionsConfigDefaultsEmpty 默认配置的 regions 段必须全空——
// 空 = 使用内置实测值，这是"零配置可用"的硬约束。
func TestRegionsConfigDefaultsEmpty(t *testing.T) {
	c := Default()
	if c.Regions.CN.ChatBase != "" || c.Regions.INTL.ChatBase != "" {
		t.Error("默认 regions 的 chat_base 应为空（空 = 用内置实测值）")
	}
	if c.Regions.INTL.Platform != "" || c.Regions.INTL.Origin != "" {
		t.Error("默认 regions 的 platform/origin 应为空")
	}
}

// TestRegionsConfigParse 解析 regions 段（部分填写也应正常）。
func TestRegionsConfigParse(t *testing.T) {
	raw := []byte(`{
	  "regions": {
	    "intl": { "chat_base": "https://intl.example", "platform": "custom-ai" }
	  }
	}`)
	c, err := ParseConfig(raw)
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if c.Regions.INTL.ChatBase != "https://intl.example" {
		t.Errorf("intl.chat_base = %q", c.Regions.INTL.ChatBase)
	}
	if c.Regions.INTL.Platform != "custom-ai" {
		t.Errorf("intl.platform = %q", c.Regions.INTL.Platform)
	}
	// 未填写的项保持空（下游回落内置值）。
	if c.Regions.INTL.WebBase != "" {
		t.Errorf("未填写的 intl.web_base = %q, want 空", c.Regions.INTL.WebBase)
	}
	// 国内站不受国际站配置影响。
	if c.Regions.CN.ChatBase != "" {
		t.Errorf("cn.chat_base = %q, want 空", c.Regions.CN.ChatBase)
	}
}

// TestLegacyConfigWithoutRegionsStillLoads 老 config.json（无 regions 段）必须照常加载，
// 且行为与引入本特性前一致——这是既有部署的向后兼容硬约束。
func TestLegacyConfigWithoutRegionsStillLoads(t *testing.T) {
	legacy := []byte(`{
	  "listen": ":7863",
	  "api_key": "sk-test",
	  "auth_dir": "./auths",
	  "state_file": "./data/state.json",
	  "server": { "max_body_mb": 8 },
	  "cooldown": { "soft_rate": "600s", "soft_rate_max": "2h" },
	  "schedule": { "checkin_hours": [9, 21], "checkin_enabled": true },
	  "upstream": { "timeout_seconds": 120 },
	  "pool": { "max_in_flight": 3 },
	  "session_sticky": { "enabled": true, "ttl": "30m", "gc_interval": "5m" }
	}`)
	c, err := ParseConfig(legacy)
	if err != nil {
		t.Fatalf("老配置应能加载，err=%v", err)
	}
	if c.APIKey != "sk-test" {
		t.Errorf("api_key = %q", c.APIKey)
	}
	// regions 全空 → 下游用内置站点值。
	if c.Regions.CN.ChatBase != "" || c.Regions.INTL.ChatBase != "" {
		t.Error("老配置的 regions 应全空（回落内置值）")
	}
}

// TestRegionsEnvOverride 环境变量覆盖国际站端点（部署时不改文件即可切换）。
func TestRegionsEnvOverride(t *testing.T) {
	t.Setenv("WB2A_INTL_CHAT_BASE", "https://env-intl.example")
	t.Setenv("WB2A_INTL_PLATFORM", "env-platform")

	c, err := Load("") // 无文件 + env
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Regions.INTL.ChatBase != "https://env-intl.example" {
		t.Errorf("intl.chat_base = %q, want env 值", c.Regions.INTL.ChatBase)
	}
	if c.Regions.INTL.Platform != "env-platform" {
		t.Errorf("intl.platform = %q, want env 值", c.Regions.INTL.Platform)
	}
}

// TestRegionsPreservedInConfigRoundTrip 面板保存配置时 regions 段必须被保留。
//
// 背景：面板保存走 mergeConfigMaps 深合并（只覆盖表单管理的键），
// 而 regions 不在表单里——若被合并逻辑丢掉，用户手配的国际站端点会在
// 任意一次面板保存后静默失效，表现为"国际站账号突然全部 401"。
func TestRegionsPreservedInConfigRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	initial := `{
	  "api_key": "sk-1",
	  "regions": { "intl": { "chat_base": "https://keep.example" } }
	}`
	if err := os.WriteFile(path, []byte(initial), 0o600); err != nil {
		t.Fatal(err)
	}

	// 模拟面板提交：只带它管理的键（不含 regions）。
	oldRaw, _ := os.ReadFile(path)
	var cur, incoming map[string]any
	_ = json.Unmarshal(oldRaw, &cur)
	_ = json.Unmarshal([]byte(`{"api_key":"sk-2"}`), &incoming)
	merged := mergeConfigMaps(cur, incoming)

	if _, err := ParseConfig(mergedJSON(merged)); err != nil {
		t.Fatalf("合并后配置非法: %v", err)
	}
	regions, ok := merged["regions"].(map[string]any)
	if !ok {
		t.Fatal("面板保存后 regions 段丢失——国际站端点配置会被静默抹除")
	}
	intl, ok := regions["intl"].(map[string]any)
	if !ok {
		t.Fatal("regions.intl 丢失")
	}
	if intl["chat_base"] != "https://keep.example" {
		t.Errorf("intl.chat_base = %v, want 保留原值", intl["chat_base"])
	}
}
