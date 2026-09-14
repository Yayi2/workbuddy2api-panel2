// region.go 账号所属站点（国内站 / 国际站）的归一化与判定。
//
// 背景：WorkBuddy 上游有两套**同协议异构部署**——国内站 copilot.tencent.com
// 与国际站 www.workbuddy.ai。二者 /v2/plugin/* 端点路径与响应包络完全一致，
// 差异只在域名、Origin、登录 platform 参数与轮询中状态码（详见 internal/upstream/profile.go）。
//
// 站点标识存放于凭据文件的顶层 `edition` 字段，取值与
// [CangShui/workbuddy-gateway] 保持一致，使两个项目的 auths/ 目录**可直接互通**：
// 同一份 workbuddy*.json 既能被本网关加载，也能被 workbuddy-gateway 加载。
//
// 向后兼容：老凭据文件没有 `edition` 字段 → 零值 → 归一为 RegionCN，
// 行为与引入本特性之前逐字节一致。
package auth

import "strings"

// 站点标识常量（写入凭据文件 edition 字段的规范值）。
const (
	RegionCN   = "cn"   // 国内站 copilot.tencent.com
	RegionINTL = "intl" // 国际站 www.workbuddy.ai
)

// NormalizeRegion 把凭据文件里的 edition 字符串归一化为规范站点标识。
//
// 别名表与 workbuddy-gateway 的 profileForEdition 逐项对齐（intl / international /
// global / workbuddy.ai），保证两边对同一份文件的解读一致；空值与未知值一律回退
// 国内站——这是"未标注即为历史 CN 账号"的安全默认。
func NormalizeRegion(edition string) string {
	switch strings.ToLower(strings.TrimSpace(edition)) {
	case "intl", "international", "global", "workbuddy.ai":
		return RegionINTL
	default:
		return RegionCN
	}
}

// RegionLabel 返回站点中文展示名（面板与 /status 用）。
func RegionLabel(region string) string {
	if NormalizeRegion(region) == RegionINTL {
		return "国际站"
	}
	return "国内站"
}

// Region 返回账号所属站点（规范标识）；未标注回退国内站。
func (a *Auth) Region() string {
	if a == nil {
		return RegionCN
	}
	return NormalizeRegion(a.Edition)
}

// IsIntl 报告账号是否为国际站账号。
func (a *Auth) IsIntl() bool { return a.Region() == RegionINTL }
