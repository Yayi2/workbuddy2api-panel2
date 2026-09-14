// order.go 面板「账号池使用顺序」接口：查看/调整选号顺序，切换选号策略。
//
// 背景：默认选号是"三因子加权 + Top5 加权随机"，目的是打散热点；但多账号用户
// 常需要**指定用号顺序**（如"先用主号、额度用完再动备用号"）。本文件提供面板侧的
// 顺序编辑能力，落到 pool 的选号策略上（pool/strategy.go）。
//
// 设计约束：
//   - 顺序的权威存储在 pool（state.json 的 order 字段），本层只做参数校验与转发，
//     不自己维护第二份顺序，避免两处不一致；
//   - 策略切换是**热生效**的（pool.SetStrategy），但为让重启后仍是用户选定的策略，
//     同时写回 config.json（pool.strategy）——否则重启就 silently 回到加权随机；
//   - 顺序调整只在 priority/round_robin 策略下影响选号；weighted 下仅作记录，
//     接口会明确告知调用方这一点，避免"我排了序但没生效"的困惑。
package panel

import (
	"encoding/json"
	"log"
	"net/http"

	"github.com/linguo2625469/workbuddy2api-panel/internal/pool"
)

// poolOrder 返回当前选号顺序、策略与可用策略清单（面板渲染排序列表用）。
func (p *Panel) poolOrder(w http.ResponseWriter, r *http.Request) {
	order := p.cfg.Pool.Order()
	strategy := p.cfg.Pool.Strategy()

	// 顺序在 weighted 下不参与选号：明确透出该事实，前端据此给出提示。
	effective := strategy != pool.StrategyWeighted

	// 每个 uid 附上站点与可用性，面板可在排序列表里直接显示状态，
	// 不必再拉一次 overview 做关联（少一次往返，且避免两处数据不一致）。
	type item struct {
		UID      string `json:"uid"`
		Nickname string `json:"nickname,omitempty"`
		Region   string `json:"region"`
		Label    string `json:"region_label"`
		Healthy  bool   `json:"healthy"`
		Disabled bool   `json:"disabled"`
		Cooling  bool   `json:"cooling"`
	}
	items := make([]item, 0, len(order))
	for _, uid := range order {
		st, ok := p.cfg.Pool.Status(uid)
		if !ok {
			continue
		}
		items = append(items, item{
			UID:      uid,
			Nickname: st.Nickname,
			Region:   st.Region,
			Label:    st.RegionLabel,
			Healthy:  !st.Cooling && !st.Disabled,
			Disabled: st.Disabled,
			Cooling:  st.Cooling,
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":        true,
		"order":     items,
		"strategy":  strategy,
		"effective": effective,
		"strategies": []map[string]any{
			{"id": pool.StrategyWeighted, "label": pool.StrategyLabel(pool.StrategyWeighted),
				"desc": "三因子加权（积分占比 / 闲置补偿 / 成功率）+ Top5 加权随机，打散热点、均摊额度"},
			{"id": pool.StrategyPriority, "label": pool.StrategyLabel(pool.StrategyPriority),
				"desc": "按下方顺序取第一个可用账号——优先号冷却/禁用时才自动用下一个"},
			{"id": pool.StrategyRoundRobin, "label": pool.StrategyLabel(pool.StrategyRoundRobin),
				"desc": "按下方顺序轮流使用，每次请求换下一个账号，均摊各号额度"},
		},
	})
}

// poolSetOrder 调整顺序。两种用法（二选一）：
//
//	{"uid":"<uid>","to":<索引>}      移动到指定位置（面板拖拽用）
//	{"uid":"<uid>","delta":-1|1}     相对上移/下移（面板按钮用）
//	{"reset":true}                   恢复默认顺序（uid 升序）
//
// 返回新顺序。uid 不存在 → 404。
func (p *Panel) poolSetOrder(w http.ResponseWriter, r *http.Request) {
	var body struct {
		UID   string `json:"uid"`
		To    *int   `json:"to"`
		Delta *int   `json:"delta"`
		Reset bool   `json:"reset"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}

	if body.Reset {
		order := p.cfg.Pool.ResetOrder()
		log.Printf("panel: 重置选号顺序（回到 uid 升序），共 %d 个账号", len(order))
		p.writeOrder(w, order)
		return
	}
	if body.UID == "" {
		writeErr(w, http.StatusBadRequest, "uid required")
		return
	}

	var (
		order []string
		ok    bool
	)
	switch {
	case body.To != nil:
		order, ok = p.cfg.Pool.MoveAccount(body.UID, *body.To)
	case body.Delta != nil:
		order, ok = p.cfg.Pool.MoveAccountBy(body.UID, *body.Delta)
	default:
		writeErr(w, http.StatusBadRequest, "需要提供 to / delta / reset 之一")
		return
	}
	if !ok {
		writeErr(w, http.StatusNotFound, "account not found")
		return
	}
	log.Printf("panel: 调整选号顺序 uid=%s（共 %d 个账号）", body.UID, len(order))
	p.writeOrder(w, order)
}

// writeOrder 统一输出顺序响应（含策略与是否生效，前端一次拿到全部所需信息）。
func (p *Panel) writeOrder(w http.ResponseWriter, order []string) {
	strategy := p.cfg.Pool.Strategy()
	resp := map[string]any{
		"ok":        true,
		"order":     order,
		"strategy":  strategy,
		"effective": strategy != pool.StrategyWeighted,
	}
	if strategy == pool.StrategyWeighted {
		// 顺序已保存但当前策略不用它——必须显式说明，否则用户会以为排序没生效。
		resp["message"] = "顺序已保存；当前策略为「加权随机」，顺序暂不参与选号。" +
			"切换到「手动优先级」或「轮流使用」后生效"
	}
	writeJSON(w, http.StatusOK, resp)
}

// poolSetStrategy 切换选号策略（热生效 + 写回 config.json 持久化）。
//
// 为什么还要写回 config：只调 pool.SetStrategy 的话下次重启就回到 weighted，
// 用户会觉得"设置丢了"。写回失败不阻断本次切换（已热生效），但在响应里标注出来。
func (p *Panel) poolSetStrategy(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Strategy string `json:"strategy"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	s := pool.NormalizeStrategy(body.Strategy)
	p.cfg.Pool.SetStrategy(s)
	log.Printf("panel: 切换选号策略为 %s（%s）", pool.StrategyLabel(s), s)

	resp := map[string]any{
		"ok":        true,
		"strategy":  s,
		"effective": s != pool.StrategyWeighted,
	}

	// 持久化到 config.json（复用配置保存通路，保证与手工编辑同一套校验/落盘逻辑）。
	if p.cfg.SaveConfig != nil {
		raw, err := json.Marshal(map[string]any{"pool": map[string]any{"strategy": s}})
		if err == nil {
			if _, err := p.cfg.SaveConfig(raw); err != nil {
				log.Printf("panel: 选号策略写回配置失败（本次已热生效，重启会回到原值）: %v", err)
				resp["persist_error"] = err.Error()
			}
		}
	}
	writeJSON(w, http.StatusOK, resp)
}
