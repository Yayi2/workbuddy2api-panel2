// blackcat.go 夜猫子任务执行器：23:00–08:00 窗口内对池内账号补足 glm-5.2 对话
// 并上报事件链（black_cat 判据）。窗口外触发则直接跳过（只观测，不报错）。
package scheduler

import (
	"log"
	"time"

	"github.com/linguo2625469/workbuddy2api-panel/internal/upstream"
)

// RunBlackcatNow 对所有**支持成长中心**的账号执行夜猫子对话补足（窗口外跳过）。
// 由 blackcat_hours 排程（默认 [23]）触发；执行前二次校验 InNightWindow。
// 国际站无 black_cat 任务（其任务清单是另一套 schema），由 growthAccounts 过滤，
// 否则每个夜间窗口都会对国际站账号白跑一次任务查询。
func (s *Scheduler) RunBlackcatNow() {
	if !upstream.InNightWindow(time.Now()) {
		log.Printf("blackcat: 当前不在 23:00–08:00 计数窗口，跳过")
		return
	}
	for _, a := range s.growthAccounts(true) {
		need, err := s.cfg.Upstream.BlackcatNeed(a)
		if err != nil {
			log.Printf("blackcat %s: %v", a.UID, err)
			continue
		}
		if need <= 0 {
			continue
		}
		ok, err := s.cfg.Upstream.RunNightChats(a, int(need))
		if err != nil {
			log.Printf("blackcat %s: %d/%d 完成，中断: %v", a.UID, ok, need, err)
			continue
		}
		log.Printf("blackcat %s: 完成 %d 次夜间对话", a.UID, ok)
		time.Sleep(activityAccountDelay)
	}
}
