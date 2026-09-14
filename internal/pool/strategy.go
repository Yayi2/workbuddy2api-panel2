// strategy.go 选号策略：weighted（默认，三因子加权随机）/ priority（手动优先级）/ round_robin（严格轮流）。
//
// 背景：既有选号是"三因子加权 + Top5 加权随机"，目的是**打散热点**、避免额度集中。
// 但多账号用户常见另一诉求：**指定用号顺序**——例如"先用主号，额度用完再动备用号"，
// 或"先消耗小号、把主号留到最后"。加权随机无法表达这种意图（它刻意反均匀地打散）。
//
// 因此新增可切换策略，**默认保持 weighted**，既有部署行为逐字节不变：
//
//	weighted     既有行为：三因子加权 Top5 + 加权随机（打散热点）
//	priority     手动优先级：按面板排定的顺序，取第一个可用账号（优先号不可用才用下一个）
//	round_robin  严格轮流：按顺序循环使用，均摊各号额度
//
// priority 与 round_robin 的区别（用户最关心的一点）：
//   - priority：只要队首可用就一直用它 —— "不耗尽不换号"
//   - round_robin：每次请求都换下一个 —— "均摊消耗"
//
// ⚠️ 与**会话粘性**的关系（实测确认，最容易困惑的一点）：
// 会话粘性（session_sticky）优先级**高于**本策略。同一会话（conversation_id 或由
// system+首条 user 派生的键）在首次成功后会被绑定到某个账号，后续同一会话的多轮
// 请求直接复用该绑定（PickByUID），**不再走选号策略**。
//
// 这是正确设计（多轮上下文必须留在同一账号），但会导致"我明明把 A 置顶了，
// 实际却在用 B"——那个会话此前已绑定到 B。验证顺序调整是否生效时，请：
//   - 用不同的会话（不同 conversation_id / 不同首条消息），或
//   - 临时把 session_sticky.enabled 设为 false。
package pool

import (
	"sort"
	"strings"
	"time"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
)

// 选号策略标识（写入 config.json 与 state.json）。
const (
	StrategyWeighted   = "weighted"    // 默认：三因子加权随机
	StrategyPriority   = "priority"    // 手动优先级（按序取第一个可用）
	StrategyRoundRobin = "round_robin" // 严格轮流
)

// NormalizeStrategy 归一化策略标识；空值/未知回退 weighted（默认，保证老配置行为不变）。
func NormalizeStrategy(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case StrategyPriority, "prio", "order":
		return StrategyPriority
	case StrategyRoundRobin, "rr", "round-robin", "rotate":
		return StrategyRoundRobin
	default:
		return StrategyWeighted
	}
}

// StrategyLabel 策略中文名（面板展示）。
func StrategyLabel(s string) string {
	switch NormalizeStrategy(s) {
	case StrategyPriority:
		return "手动优先级"
	case StrategyRoundRobin:
		return "轮流使用"
	default:
		return "加权随机（默认）"
	}
}

// SetStrategy 注入选号策略（main 从 config 解析后调用）。
func (p *Pool) SetStrategy(s string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.strategy = NormalizeStrategy(s)
}

// Strategy 返回当前生效的选号策略。
func (p *Pool) Strategy() string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return NormalizeStrategy(p.strategy)
}

// ---------------------------------------------------------------------------
// 优先级顺序（仅 priority / round_robin 使用）
// ---------------------------------------------------------------------------

// Order 返回账号的优先级顺序（uid 列表，靠前者优先）。
//
// 顺序的权威存储是 state.json 的 order 字段（与账号状态同文件、同一次原子落盘），
// 这样重启后顺序不丢，也不会与 auths/ 目录的发现顺序耦合。
// 未在顺序表里的账号（新增账号）按 uid 升序追加到末尾——保证**确定性**：
// 否则 map 遍历顺序会让"新账号排在哪"每次随机，用户排好的顺序会被莫名其妙打乱。
func (p *Pool) Order() []string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.orderLocked()
}

// orderLocked 计算当前完整顺序（持有锁时调用）。调用方必须已持 p.mu（读或写均可）。
func (p *Pool) orderLocked() []string {
	out := make([]string, 0, len(p.byUID))
	seen := make(map[string]bool, len(p.byUID))
	for _, uid := range p.order {
		if _, ok := p.byUID[uid]; ok {
			out = append(out, uid)
			seen[uid] = true
		}
	}
	// 新增/未排入的账号按 uid 升序追加（稳定、可预期）。
	rest := make([]string, 0)
	for uid := range p.byUID {
		if !seen[uid] {
			rest = append(rest, uid)
		}
	}
	sort.Strings(rest)
	return append(out, rest...)
}

// MoveAccount 调整账号优先级：把 uid 移动到 order 中 to 位置（越界自动钳制）。
// 返回新顺序；uid 不存在返回 false（不做任何改动）。
//
// 采用"整表重写 + 原子落盘"而非增量插入：顺序表规模是有界的（账号数），
// 整表重写逻辑简单且不会出现部分更新导致顺序错乱。
func (p *Pool) MoveAccount(uid string, to int) ([]string, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, ok := p.byUID[uid]; !ok {
		return nil, false
	}
	cur := p.orderLocked()
	from := -1
	for i, u := range cur {
		if u == uid {
			from = i
			break
		}
	}
	if from < 0 {
		return nil, false
	}
	if to < 0 {
		to = 0
	}
	if to >= len(cur) {
		to = len(cur) - 1
	}
	if from == to {
		p.order = cur
		return cur, true
	}
	// 抽出再插入（保持其余元素相对顺序）。
	moved := cur[from]
	rest := make([]string, 0, len(cur)-1)
	rest = append(rest, cur[:from]...)
	rest = append(rest, cur[from+1:]...)
	next := make([]string, 0, len(cur))
	next = append(next, rest[:to]...)
	next = append(next, moved)
	next = append(next, rest[to:]...)
	p.order = next
	p.dirty.Store(true)
	p.saveLocked() // 立即落盘：顺序是用户的显式意图，不应等到 5s 后的后台 flush
	return next, true
}

// MoveAccountBy 相对移动（delta 为负上移、正下移），供面板"上移/下移"按钮使用。
func (p *Pool) MoveAccountBy(uid string, delta int) ([]string, bool) {
	cur := p.Order()
	idx := -1
	for i, u := range cur {
		if u == uid {
			idx = i
			break
		}
	}
	if idx < 0 {
		return nil, false
	}
	return p.MoveAccount(uid, idx+delta)
}

// ResetOrder 清空手动顺序（回到 uid 升序的默认顺序）。
func (p *Pool) ResetOrder() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.order = nil
	p.dirty.Store(true)
	p.saveLocked()
	return p.orderLocked()
}

// ---------------------------------------------------------------------------
// 策略选号实现
// ---------------------------------------------------------------------------

// pickByOrderLocked 按顺序取第一个可用账号（priority 与 round_robin 共用）。
//
// rrStart 非空且策略为 round_robin 时，从该 uid 之后开始找（实现"每次换下一个"）。
// 调用方必须已持 p.mu。
//
// 与 weighted 的关键差异：**不做 Top5 截断、不做加权随机、不做 minPickGap 防撞号**——
// 用户显式排了序，就应当严格按序使用；掺入随机或"跳过刚用过的"会让顺序失去意义。
func (p *Pool) pickByOrderLocked(region string, tried map[string]bool, reqModel string, now time.Time, rr bool) *auth.Auth {
	ids := p.orderLocked()
	if len(ids) == 0 {
		return nil
	}
	healthyOf := func(e *entry) bool { return e.healthy(now) }
	if reqModel != "" {
		healthyOf = func(e *entry) bool { return e.healthyForModel(now, reqModel) }
	}
	ok := func(uid string) *entry {
		e, exists := p.byUID[uid]
		if !exists {
			return nil
		}
		if tried != nil && tried[uid] {
			return nil
		}
		if region != RegionFilterAny && e.a.Region() != region {
			return nil
		}
		if !healthyOf(e) {
			return nil
		}
		if p.inFlightFull(e) {
			return nil
		}
		return e
	}
	// round_robin：从上次用过的下一个开始（环形）。
	start := 0
	if rr && p.rrCursor != "" {
		for i, uid := range ids {
			if uid == p.rrCursor {
				start = (i + 1) % len(ids)
				break
			}
		}
	}
	for i := 0; i < len(ids); i++ {
		uid := ids[(start+i)%len(ids)]
		if e := ok(uid); e != nil {
			e.lastUsed = now
			if rr {
				p.rrCursor = uid
			}
			return e.a
		}
	}
	return nil
}

// StrategyFor 供面板/状态展示：返回当前策略标识与展示名。
func (p *Pool) StrategyInfo() (id, label string) {
	s := p.Strategy()
	return s, StrategyLabel(s)
}
