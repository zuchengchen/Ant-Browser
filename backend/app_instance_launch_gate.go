package backend

import (
	"sync"
	"time"
)

// instanceLaunchGate 实例并发启动闸门（安全控制）：
// limit<=0 时不限制；否则超出"同时运行实例数"的启动请求排队等待，
// 直到有实例停止腾出名额后自动放行。等待期间不持有任何其它锁，
// 停止实例不会被排队阻塞，避免"等名额→但名额只能靠停止释放→停止被锁"的死锁。
//
// 占用计数统计"经本闸门获准启动或已恢复的运行实例"；释放统一挂在
// markProfileStoppedLocked（全部停止路径的总汇：手动停止/状态巡检/备份恢复/关机）。
type instanceLaunchGate struct {
	mu        sync.Mutex
	admitted  map[string]*instanceLaunchAdmission // 每个实例只占一个名额
	releaseCh chan struct{}                       // 关闭以唤醒排队中的启动请求
	waiting   int                                 // 当前排队中的启动请求数
}

// Each admitted lifetime has its own identity. A delayed failure/repeated
// release from an earlier start must not release a later start of the profile.
type instanceLaunchAdmission struct {
	profileID  string
	references int  // 同实例并发请求共享名额，各自失败时只撤销自己的引用
	running    bool // 恢复出的进程独立于启动请求，直到停止才释放
}

func newLaunchGate() *instanceLaunchGate {
	return &instanceLaunchGate{
		admitted:  map[string]*instanceLaunchAdmission{},
		releaseCh: make(chan struct{}),
	}
}

// occupied and broadcast require g.mu.
func (g *instanceLaunchGate) occupied() int {
	return len(g.admitted)
}

func (g *instanceLaunchGate) broadcast() {
	close(g.releaseCh)
	g.releaseCh = make(chan struct{})
}

func (g *instanceLaunchGate) release(profileID string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, exists := g.admitted[profileID]; exists {
		delete(g.admitted, profileID)
		g.broadcast()
	}
}

func (g *instanceLaunchGate) releaseAdmission(admission *instanceLaunchAdmission) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.admitted[admission.profileID] == admission {
		admission.references--
		if admission.references == 0 && !admission.running {
			delete(g.admitted, admission.profileID)
			g.broadcast()
		}
	}
}

func (g *instanceLaunchGate) releaseFunc(admission *instanceLaunchAdmission) func() {
	var once sync.Once
	return func() { once.Do(func() { g.releaseAdmission(admission) }) }
}

func (g *instanceLaunchGate) retain(profileId string) {
	g.mu.Lock()
	if g.admitted[profileId] == nil {
		g.admitted[profileId] = &instanceLaunchAdmission{profileID: profileId}
	}
	g.admitted[profileId].running = true
	g.mu.Unlock()
}

// Both settings updates and admission checks hold the gate mutex.
func (a *App) setInstanceLaunchLimit(limit int) {
	gate := a.launchGate()
	gate.mu.Lock()
	defer gate.mu.Unlock()
	limit = max(0, limit)
	if a.config.Browser.MaxConcurrentInstances != limit {
		a.config.Browser.MaxConcurrentInstances = limit
		gate.broadcast()
	}
}

func (a *App) launchGate() *instanceLaunchGate {
	a.instanceGateOnce.Do(func() {
		a.instanceGate = newLaunchGate()
	})
	return a.instanceGate
}

// releaseInstanceLaunchSlot 供停止路径调用：实例停止即归还名额并唤醒排队请求。
func (a *App) releaseInstanceLaunchSlot(profileID string) {
	if a == nil {
		return
	}
	// Stop and first-start calls can race. Both must synchronize with Once
	// before reading the lazily initialized pointer.
	a.launchGate().release(profileID)
}

// retainInstanceLaunchSlot 将已在运行的恢复实例计入并发上限。
func (a *App) retainInstanceLaunchSlot(profileId string) {
	if a == nil || profileId == "" {
		return
	}
	a.launchGate().retain(profileId)
}

func (a *App) reconcileInstanceLaunchSlots() {
	if a == nil || a.browserMgr == nil {
		return
	}
	a.browserMgr.Mutex.Lock()
	defer a.browserMgr.Mutex.Unlock()
	for profileId, profile := range a.browserMgr.Profiles {
		if profile != nil && profile.Running {
			a.retainInstanceLaunchSlot(profileId)
		}
	}
}

// acquireInstanceLaunchSlot 返回名额释放函数。
// queued=true 表示该请求经历过排队（前端据此提示"已进入启动队列"）。
// 同一实例重复的启动请求不重复占名额（启动逻辑内部会按"已在运行"处理）。
func (a *App) acquireInstanceLaunchSlot(profileId string) (release func(), queued bool) {
	gate := a.launchGate()
	queued = false
	for {
		gate.mu.Lock()
		if !a.browserInstanceStartAllowed() {
			if queued {
				gate.waiting--
			}
			gate.mu.Unlock()
			return func() {}, queued
		}
		limit := 0
		if a.config != nil {
			limit = a.config.Browser.MaxConcurrentInstances
		}
		if admission := gate.admitted[profileId]; admission != nil {
			admission.references++
			if queued {
				gate.waiting--
			}
			gate.mu.Unlock()
			return gate.releaseFunc(admission), queued
		}
		if limit <= 0 || gate.occupied() < limit {
			admission := &instanceLaunchAdmission{profileID: profileId, references: 1}
			gate.admitted[profileId] = admission
			if queued {
				gate.waiting--
			}
			gate.mu.Unlock()
			if limit > 0 {
				a.emitRuntimeEvent("instance:launch:admitted", map[string]interface{}{"profileId": profileId})
			}
			return gate.releaseFunc(admission), queued
		}
		newlyQueued := !queued
		if newlyQueued {
			queued = true
			gate.waiting++
		}
		// Keep the notification channel from the state we just observed. A
		// release after unlocking closes this channel even if no waiter has
		// reached its select yet, so that wake-up cannot be lost.
		position := gate.waiting
		releaseCh := gate.releaseCh
		gate.mu.Unlock()
		if newlyQueued {
			a.emitRuntimeEvent("instance:launch:queued", map[string]interface{}{
				"profileId": profileId,
				"position":  position,
				"limit":     limit,
			})
		}
		select {
		case <-releaseCh:
		case <-time.After(300 * time.Millisecond):
			// 定期复查名额状态；正常释放通过 releaseCh 立即唤醒。
		}
	}
}
