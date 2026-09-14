// region.go 请求级区域选择：把调用方的"只走国内站/国际站"意图解析成池筛选参数。
//
// 背景：账号池支持国内站（cn）与国际站（intl）账号混挂轮询（默认行为，与参考实现
// workbuddy-gateway 一致）。在此之上，调用方可显式要求把本次请求限制在某站点：
//
//	X-WB-Region: cn | intl          显式指定站点
//	model 后缀 "模型名@intl"          等价写法（便于无自定义头能力的 OpenAI SDK）
//
// 设计取舍——**默认不限区域**：不指定时行为与引入本特性前完全一致，
// 既有 CN-only 部署不受任何影响，混挂部署沿用全池轮询。
//
// 区域收窄失败时的处理：不回落到其他站点（会把请求发到调用方未预期的计费主体），
// 而是返回 503 并附带 region 提示，同时在响应头标注，使调用方能明确区分
// "网关没有可用账号" 与 "我要求的站点没有可用账号"。
package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
	"github.com/linguo2625469/workbuddy2api-panel/internal/pool"
)

// HeaderRegion 调用方显式指定站点所用的请求头。
const HeaderRegion = "X-WB-Region"

// HeaderRegionApplied 响应头：本次请求实际生效的区域（空 = 不限）。
// 调用方可据此确认自己的区域要求被接受（未被静默忽略）。
const HeaderRegionApplied = "X-WB-Region-Applied"

// regionSuffixSep model 后缀分隔符（"glm-5.2@intl"）。
const regionSuffixSep = "@"

// requestRegion 解析请求的区域意图，返回 (region, bareModel)。
//
// 优先级：显式请求头 > model 后缀 > 不限。
// 请求头写法容错（区分大小写不敏感、允许别名）；无法识别的值一律视为"不限"
// 并在调用方侧标注 —— 绝不静默降级成"只走国内站"（那会把国际站账号无声排除）。
//
// bareModel 是剥掉 @region 后缀后的模型名：上游不认识该后缀，必须剥离后再透传，
// 否则上游会把 "glm-5.2@intl" 当成不存在的模型。
func requestRegion(headerValue, model string) (region, bareModel string) {
	bareModel = model

	// 1. model 后缀。
	if idx := strings.LastIndex(model, regionSuffixSep); idx > 0 && idx < len(model)-1 {
		suffix := model[idx+1:]
		if r := normalizeRegionToken(suffix); r != "" {
			region = r
			bareModel = model[:idx]
		}
	}

	// 2. 请求头优先覆盖（显式意图强于后缀约定）。
	if h := normalizeRegionToken(headerValue); h != "" {
		region = h
	}
	return region, bareModel
}

// rewriteModelField 把请求体里的 model 字段替换为 bare（剥离 @region 后缀后的模型名）。
//
// 只改 model 一个键、其余原样保留：用 map 往返会丢字段顺序与数字精度（大整数 token
// 计数可能变形），故这里走 json.RawMessage 逐键重建的方式保持其余键的原始字节。
// 解析失败返回错误，调用方回退用原始 body（宁可让上游拒绝，也不静默丢字段）。
func rewriteModelField(body []byte, bare string) ([]byte, error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(body, &obj); err != nil {
		return nil, err
	}
	if _, ok := obj["model"]; !ok {
		return nil, fmt.Errorf("no model field in request body")
	}
	enc, err := json.Marshal(bare)
	if err != nil {
		return nil, err
	}
	obj["model"] = enc
	return json.Marshal(obj)
}

// normalizeRegionToken 把区域 token 归一化为 cn/intl；无法识别返回空串（表"不限"）。
//
// 与 pool.NormalizeRegionFilter 的差别：这里额外接受"any/all/auto"期望值
// （显式表达"不限"，与省略等价），便于调用方写明意图而非留空。
func normalizeRegionToken(v string) string {
	s := strings.ToLower(strings.TrimSpace(v))
	switch s {
	case "", "any", "all", "auto", "none", "*":
		return ""
	}
	switch s {
	case auth.RegionINTL, "international", "global", "workbuddy.ai":
		return auth.RegionINTL
	case auth.RegionCN:
		return auth.RegionCN
	default:
		// 未知 token：视为未指定（不静默收窄到 cn）。
		return ""
	}
}

// pickForRequest 按区域选号。
//
// region 为空 → 走既有全池语义（PickExcludingForModel，含 6004 模型豁免）；
// region 非空 → 区域收窄，候选仅限该站点账号，且兜底也不跨站点。
func (h *Handler) pickForRequest(region string, tried map[string]bool, reqModel string) *auth.Auth {
	if pool.NormalizeRegionFilter(region) == pool.RegionFilterAny {
		return h.cfg.Pool.PickExcludingForModel(tried, reqModel)
	}
	return h.cfg.Pool.PickInRegion(region, tried, reqModel)
}

// writeRegionUnavailable 区域收窄无可用账号时的统一响应。
//
// 用 503（与"网关无可用账号"同族）而非 404：这是服务端资源暂不可用，
// 客户端按可重试处理；body 明确点出用户要求的站点，避免误判为配置错误。
func writeRegionUnavailable(w http.ResponseWriter, region string) {
	w.Header().Set(HeaderRegionApplied, region)
	msg := "no available account in region " + region + "（" + auth.RegionLabel(region) +
		"账号全部不可用；如需跨站点调用请去掉 " + HeaderRegion + " 头）"
	writeOpenAIError(w, 503, "no_available_account_in_region", msg)
}
