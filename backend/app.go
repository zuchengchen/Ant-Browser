package backend

import (
	"ant-chrome/backend/internal/automation"
	"ant-chrome/backend/internal/browser"
	"ant-chrome/backend/internal/config"
	"ant-chrome/backend/internal/database"
	"ant-chrome/backend/internal/keypouch"
	"ant-chrome/backend/internal/launchcode"
	"ant-chrome/backend/internal/logger"
	"ant-chrome/backend/internal/proxy"
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type quitMode uint8

const (
	quitModeFull quitMode = iota
	quitModeAppOnly
)

// App 应用结构体
type App struct {
	ctx                         context.Context
	config                      *config.Config
	db                          *database.DB
	interceptor                 *logger.MethodInterceptor
	browserMgr                  *browser.Manager
	xrayMgr                     *proxy.XrayManager
	clashMgr                    *proxy.ClashManager
	singboxMgr                  *proxy.SingBoxManager
	mieruMgr                    *proxy.MieruManager
	launchCodeSvc               *launchcode.LaunchCodeService
	launchServer                *launchcode.LaunchServer
	automationMgr               *automation.Manager
	speedScheduler              *browser.ProxySpeedScheduler
	backupScheduler             *backupScheduler
	proxySourceRefreshScheduler *proxySourceRefreshScheduler
	appRoot                     string
	version                     string

	forceQuit                   bool
	quitMode                    quitMode
	quitMu                      sync.RWMutex
	runtimeMu                   sync.RWMutex
	runtimeStopped              bool
	startupReady                atomic.Bool
	databaseRecoveryNotice      atomic.Pointer[database.RecoveryInfo]
	backgroundTaskCtx           context.Context
	backgroundTaskCancel        context.CancelFunc
	backgroundTasks             sync.WaitGroup
	backgroundTasksBlocked      bool
	maintenanceMu               sync.Mutex
	bridgeMu                    sync.Mutex
	proxyVirtualGroupMu         sync.Mutex
	proxyVirtualGroupCursor     map[string]int
	proxyVirtualGroupLocks      map[string]*sync.Mutex
	virtualGroupSnapshot        virtualGroupSnapshotCache
	virtualGroupMemberStateGate virtualGroupMemberStateGateCache
	proxyFailoverMu             sync.Mutex
	proxySourceIgnoredMu        sync.Mutex
	// 代理池写入在同一个临界区内读取和合并。订阅拉取单独串行，不占用写锁。
	proxySourceRefreshMu sync.Mutex
	proxySourceFetchMu   sync.Mutex
	proxyFailoverLastAt  map[string]time.Time
	proxyFailoverCount   map[string]int
	// instanceGate 实例并发启动闸门（安全控制），见 app_instance_launch_gate.go。
	instanceGate                  *instanceLaunchGate
	instanceGateOnce              sync.Once
	profileBridgeRefs             map[string]profileProxyBridgeRef
	deferredStartTargetsMu        sync.Mutex
	deferredStartTargets          map[string]deferredStartTargetsPlan
	automationTargetMu            sync.Mutex
	automationTargetCursor        map[string]string
	chatGPTMu                     sync.Mutex
	chatGPTSessions               map[string]*chatGPTInspectionSession
	browserPasswordAutosaveMu     sync.Mutex
	browserPasswordAutosaveWorker *browserPasswordAutosaveWorker
	profileWindowMarkersMu        sync.Mutex
	profileWindowMarkers          map[string]*profileWindowMarker
	browserProcessMonitors        map[string]*browserProcessMonitor
	keyPouchRuntimeChanged        chan struct{}
	keyPouchActionMu              sync.Mutex
	keyPouchRuntimeMu             sync.Mutex
	keyPouchRuntimeDevice         *keypouch.Device
	keyPouchRuntimeCancel         context.CancelFunc
	keyPouchRuntimeActive         bool
	mofaSiteMu                    sync.Mutex
	mofaImportMu                  sync.Mutex
	mofaImportJobs                map[string]context.CancelFunc
	mofaImportAccounts            map[string]struct{}
	mofaToolbarBridge             *mofaToolbarBridge
	backupLocalConfigPathOverride string
	stopServicesOnce              sync.Once
	finalizeOnce                  sync.Once
}

// NewApp 创建新的应用实例
func NewApp(appRoot string, appVersion ...string) *App {
	version := ""
	if len(appVersion) > 0 {
		version = strings.TrimSpace(appVersion[0])
	}
	return &App{
		appRoot:                 strings.TrimSpace(appRoot),
		version:                 version,
		profileBridgeRefs:       make(map[string]profileProxyBridgeRef),
		proxyVirtualGroupCursor: make(map[string]int),
		proxyVirtualGroupLocks:  make(map[string]*sync.Mutex),
		proxyFailoverLastAt:     make(map[string]time.Time),
		proxyFailoverCount:      make(map[string]int),
		deferredStartTargets:    make(map[string]deferredStartTargetsPlan),
		automationTargetCursor:  make(map[string]string),
		profileWindowMarkers:    make(map[string]*profileWindowMarker),
		browserProcessMonitors:  make(map[string]*browserProcessMonitor),
		keyPouchRuntimeChanged:  make(chan struct{}, 1),
	}
}

func (a *App) appName() string {
	if a.config != nil {
		if name := strings.TrimSpace(a.config.App.Name); name != "" {
			return name
		}
	}
	return "Ant Browser"
}

func (a *App) appVersion() string {
	version := strings.TrimSpace(a.version)
	if version == "" {
		return "unknown"
	}
	return version
}
