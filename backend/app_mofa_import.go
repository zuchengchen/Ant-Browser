package backend

import (
	"context"
	"errors"
	"strings"
	"time"
)

const mofaImportTimeout = 8 * time.Minute

func (a *App) BrowserMofaImport(profileID string) (map[string]interface{}, error) {
	return a.browserMofaImport(profileID, nil)
}

func (a *App) browserMofaImport(profileID string, source *mofaToolbarSource) (map[string]interface{}, error) {
	profileID = strings.TrimSpace(profileID)
	if a.browserMgr == nil || profileID == "" {
		return nil, errors.New("请选择一个运行中的实例")
	}
	ctx, finish, err := a.beginMofaImport(profileID)
	if err != nil {
		return nil, err
	}
	defer finish()
	progress := func(phase string) {
		a.emitRuntimeEvent("browser:mofa-import:progress", map[string]string{"profileId": profileID, "phase": phase})
	}
	progress("checking")
	result, err := a.importMofaProfile(ctx, profileID, source, progress)
	if err != nil {
		switch ctx.Err() {
		case context.Canceled:
			return nil, errors.New("已取消 MOFA 导入")
		case context.DeadlineExceeded:
			return nil, errors.New("导入 MOFA 超时，请再次核对账号后重试")
		}
		return nil, err
	}
	return map[string]interface{}{
		"status": result.Action, "accountId": result.AccountID, "groupId": result.GroupID,
		"inTargetGroup": result.InTargetGroup,
	}, nil
}

func (a *App) beginMofaImport(profileID string) (context.Context, func(), error) {
	a.mofaImportMu.Lock()
	defer a.mofaImportMu.Unlock()
	if a.mofaImportJobs[profileID] != nil {
		return nil, nil, errors.New("该实例正在导入到 MOFA，请稍候")
	}
	parent, done, err := a.beginBackgroundTask()
	if err != nil {
		return nil, nil, err
	}
	ctx, cancel := context.WithTimeout(parent, mofaImportTimeout)
	if a.mofaImportJobs == nil {
		a.mofaImportJobs = make(map[string]context.CancelFunc)
	}
	a.mofaImportJobs[profileID] = cancel
	return ctx, func() {
		cancel()
		a.mofaImportMu.Lock()
		delete(a.mofaImportJobs, profileID)
		a.mofaImportMu.Unlock()
		done()
	}, nil
}

func (a *App) BrowserMofaImportCancel(profileID string) {
	a.mofaImportMu.Lock()
	defer a.mofaImportMu.Unlock()
	if cancel := a.mofaImportJobs[strings.TrimSpace(profileID)]; cancel != nil {
		cancel()
	}
}

func (a *App) claimMofaImportAccount(site, email string) (func(), error) {
	key := site + "\x00" + mofaStockEmail(email)
	a.mofaImportMu.Lock()
	defer a.mofaImportMu.Unlock()
	if _, busy := a.mofaImportAccounts[key]; busy {
		return nil, errors.New("该 ChatGPT 账号正在导入到 MOFA，请稍候")
	}
	if a.mofaImportAccounts == nil {
		a.mofaImportAccounts = make(map[string]struct{})
	}
	a.mofaImportAccounts[key] = struct{}{}
	return func() {
		a.mofaImportMu.Lock()
		defer a.mofaImportMu.Unlock()
		delete(a.mofaImportAccounts, key)
	}, nil
}

func (a *App) importMofaProfile(ctx context.Context, profileID string, source *mofaToolbarSource, progress func(string)) (mofaStockResult, error) {
	settings, err := a.readMofaImportSettings()
	if err != nil {
		return mofaStockResult{}, err
	}
	if settings.Site == "" || settings.Username == "" || settings.Password == "" {
		return mofaStockResult{}, errors.New("请先在「系统设置 → 基础设置」保存 MOFA 地址、管理员用户名和密码")
	}
	manager := a.browserMgr
	manager.Mutex.Lock()
	profile := manager.Profiles[profileID]
	if profile == nil || !profile.Running || !profile.DebugReady || profile.DebugPort <= 0 {
		manager.Mutex.Unlock()
		return mofaStockResult{}, errors.New("实例未运行或浏览器尚未就绪，请打开实例后重试")
	}
	port, profileDir, expected := profile.DebugPort, manager.ResolveUserDataDir(profile), expectedChatGPTEmail(profile.ProfileName)
	manager.Mutex.Unlock()
	if source != nil && (source.site != settings.Site || source.port != port || !sameGPTDirectory(source.profileDir, profileDir)) {
		return mofaStockResult{}, errors.New("实例或 MOFA 后台地址已改变，请重新发起导入")
	}
	instance, err := connectMofaBrowser(ctx, port)
	if err != nil {
		return mofaStockResult{}, err
	}
	if source != nil && source.socket != instance.socket {
		return mofaStockResult{}, errMofaBrowser
	}
	email, err := instance.signedInEmail(ctx, profileDir)
	if err != nil {
		return mofaStockResult{}, err
	}
	if mofaStockEmail(email) == "" {
		return mofaStockResult{}, errors.New("未能核对本实例的 ChatGPT 邮箱")
	}
	if expected != "" && !strings.EqualFold(expected, email) {
		return mofaStockResult{}, errors.New("本实例已登录的 ChatGPT 邮箱与实例名称不同，请核对后重试")
	}
	releaseAccount, err := a.claimMofaImportAccount(settings.Site, email)
	if err != nil {
		return mofaStockResult{}, err
	}
	defer releaseAccount()
	checkOperation := func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		manager.Mutex.Lock()
		currentProfile := manager.Profiles[profileID]
		unchanged := currentProfile != nil && currentProfile.Running && currentProfile.DebugReady && currentProfile.DebugPort == port &&
			manager.ResolveUserDataDir(currentProfile) == profileDir && expectedChatGPTEmail(currentProfile.ProfileName) == expected
		manager.Mutex.Unlock()
		if !unchanged {
			return errors.New("实例配置或运行状态在导入期间已更改，请重新发起导入")
		}
		current, err := a.readMofaImportSettings()
		if err != nil {
			return err
		}
		if current != settings {
			return errors.New("MOFA 设置在导入期间已更改，请重新发起导入")
		}
		return nil
	}
	if err := checkOperation(); err != nil {
		return mofaStockResult{}, err
	}
	client := mofaClient{site: settings.Site, http: mofaHTTPClient}
	if err := client.login(ctx, settings); err != nil {
		return mofaStockResult{}, err
	}
	result, err := client.lookupStockAccount(ctx, email)
	if err != nil {
		return mofaStockResult{}, err
	}
	if err := checkOperation(); err != nil {
		return mofaStockResult{}, err
	}
	switch result.Action {
	case "skipped":
		return result, nil
	case "group_required":
		progress("saving")
		return client.addStockTargetGroup(ctx, email, result)
	case "creation_required", "reauthorization_required":
		return runMofaOAuth(ctx, &client, instance, email, result, checkOperation, progress)
	default:
		return mofaStockResult{}, errors.New("MOFA 未返回可处理的账号状态")
	}
}

func runMofaOAuth(ctx context.Context, client *mofaClient, instance mofaBrowser, email string, result mofaStockResult, checkOperation func() error, progress func(string)) (mofaStockResult, error) {
	proxyID, err := client.stockOAuthProxyID(ctx, email, result)
	if err != nil {
		return mofaStockResult{}, err
	}
	flow, err := client.startStockOAuth(ctx, proxyID)
	if err != nil {
		return mofaStockResult{}, err
	}
	if err := checkOperation(); err != nil {
		return mofaStockResult{}, err
	}
	code, state, err := instance.waitMofaOAuth(ctx, flow, email, func() { progress("waiting_for_oauth") })
	if err != nil {
		return mofaStockResult{}, err
	}
	if err := checkOperation(); err != nil {
		return mofaStockResult{}, err
	}
	progress("saving")
	return client.completeStockOAuth(ctx, flow, code, state, email, result)
}
