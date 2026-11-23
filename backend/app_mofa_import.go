package backend

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
)

func (a *App) BrowserMofaImport(profileID string) (map[string]interface{}, error) {
	profileID = strings.TrimSpace(profileID)
	if a.browserMgr == nil || profileID == "" {
		return nil, errors.New("请选择一个运行中的实例")
	}
	ctx, finish, err := a.beginMofaImport(profileID)
	if err != nil {
		return nil, err
	}
	defer finish()
	result, err := a.importMofaProfile(ctx, profileID)
	if err != nil {
		if errors.Is(err, errMofaResultUnknown) {
			return nil, err
		}
		switch ctx.Err() {
		case context.Canceled:
			return nil, errors.New("已取消 MOFA 导入")
		case context.DeadlineExceeded:
			return nil, errors.New("MOFA 导入已超时，请重新发起并在实例窗口完成 OpenAI 授权")
		}
		return nil, err
	}
	return map[string]interface{}{"status": result.Action, "accountId": result.AccountID, "groupId": result.GroupID}, nil
}

func (a *App) beginMofaImport(profileID string) (context.Context, func(), error) {
	a.mofaImportMu.Lock()
	defer a.mofaImportMu.Unlock()
	if a.mofaImportJobs[profileID] != nil {
		return nil, nil, errors.New("该实例正在导入到 MOFA，请在实例窗口继续")
	}
	parent, done, err := a.beginBackgroundTask()
	if err != nil {
		return nil, nil, err
	}
	ctx, cancel := context.WithTimeout(parent, 8*time.Minute)
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

func (a *App) importMofaProfile(ctx context.Context, profileID string) (mofaImportResult, error) {
	settings, err := a.readMofaImportSettings()
	if err != nil {
		return mofaImportResult{}, err
	}
	if settings.Site == "" || settings.Username == "" || settings.Password == "" {
		return mofaImportResult{}, errors.New("请先在「系统设置 → 基础设置」保存 MOFA 地址、管理员用户名和密码")
	}
	manager := a.browserMgr
	manager.Mutex.Lock()
	profile := manager.Profiles[profileID]
	if profile == nil || !profile.Running || !profile.DebugReady || profile.DebugPort <= 0 {
		manager.Mutex.Unlock()
		return mofaImportResult{}, errors.New("实例未运行或浏览器尚未就绪，请打开实例后重试")
	}
	port, profileDir, expected := profile.DebugPort, manager.ResolveUserDataDir(profile), expectedChatGPTEmail(profile.ProfileName)
	manager.Mutex.Unlock()
	instance, err := connectMofaBrowser(ctx, port)
	if err != nil {
		return mofaImportResult{}, err
	}
	email, err := instance.signedInEmail(ctx, profileDir)
	if err != nil {
		return mofaImportResult{}, err
	}
	if expected != "" && !strings.EqualFold(expected, email) {
		return mofaImportResult{}, errors.New("本实例已登录的 ChatGPT 邮箱与实例名称不同，请核对后重试")
	}
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
		return mofaImportResult{}, err
	}
	client := mofaClient{site: settings.Site, http: mofaHTTPClient}
	if err := client.login(ctx, settings); err != nil {
		return mofaImportResult{}, err
	}
	if err := checkOperation(); err != nil {
		return mofaImportResult{}, err
	}
	var closeAuthTab func()
	defer func() {
		if closeAuthTab != nil {
			closeAuthTab()
		}
	}()
	return client.importAccount(ctx, email, func(ctx context.Context, authorization mofaAuthorization) (string, error) {
		tab, closeTab, err := instance.openTab(ctx, false)
		if err != nil {
			return "", err
		}
		closeAuthTab = closeTab
		code, err := waitMofaAuthorization(ctx, tab, authorization)
		if err != nil {
			return "", err
		}
		if err := checkOperation(); err != nil {
			return "", err
		}
		return code, nil
	})
}

func (c *mofaClient) importAccount(ctx context.Context, email string, authorize func(context.Context, mofaAuthorization) (string, error)) (mofaImportResult, error) {
	job := "ant" + strings.ReplaceAll(uuid.NewString(), "-", "")
	result, err := c.importRequest(ctx, job, "start", map[string]string{"email": email})
	if err != nil {
		return mofaImportResult{}, mofaImportError(err)
	}
	if result.Status == "authorization_required" {
		authorization, err := parseMofaAuthorization(result.AuthorizationURL, c.site)
		if err != nil {
			return mofaImportResult{}, err
		}
		code, err := authorize(ctx, authorization)
		if err != nil {
			return mofaImportResult{}, err
		}
		result, err = c.importRequest(ctx, job, "complete", map[string]string{"email": email, "code": code, "state": authorization.state})
		if err != nil {
			return mofaImportResult{}, mofaImportError(err)
		}
	}
	if result.Status != "member" || result.AccountID <= 0 || result.GroupID <= 0 {
		return mofaImportResult{}, errors.New("MOFA 尚未确认导入结果，请在后台核对后重试")
	}
	switch result.Action {
	case "skipped", "group_added", "imported", "reauthorized":
		return result, nil
	default:
		return mofaImportResult{}, errors.New("MOFA 未返回账号授权状态，请升级 MOFA 的 Ant 导入功能后重试")
	}
}
