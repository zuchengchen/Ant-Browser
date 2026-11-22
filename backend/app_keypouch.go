package backend

import (
	"context"
	"errors"
	"net/url"
	"os"
	goruntime "runtime"
	"strings"
	"time"

	"ant-chrome/backend/internal/keypouch"
	"ant-chrome/backend/internal/logger"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// The locally saved site takes precedence over ANT_KEYPOUCH_URL and the
// installer default supplied via -ldflags -X. A deep link carries only a ticket.
var bundledKeyPouchURL string

const missingKeyPouchSite = "请在「系统设置 → 基础设置 → KeyPouch 站点地址」填写站点地址并点击「保存」，然后返回 KeyPouch 网页配对。"

func keyPouchFallbackSite() string {
	if site := strings.TrimRight(strings.TrimSpace(os.Getenv("ANT_KEYPOUCH_URL")), "/"); site != "" {
		return site
	}
	return strings.TrimRight(strings.TrimSpace(bundledKeyPouchURL), "/")
}

func (a *App) keyPouchDevice(site string, create bool) (*keypouch.Device, error) {
	if site == "" {
		return nil, errors.New(missingKeyPouchSite)
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return nil, err
	}
	return keypouch.OpenDevice(dir, a.appRoot, site, create)
}

func (a *App) restoreKeyPouchRuntime() {
	a.keyPouchActionMu.Lock()
	defer a.keyPouchActionMu.Unlock()
	a.reloadKeyPouchSiteLocked()
}

// Settings and link actions share keyPouchActionMu. Loading an existing site
// never creates a device identity; only the pairing confirmation may do that.
func (a *App) reloadKeyPouchSiteLocked() {
	var device *keypouch.Device
	if site := a.keyPouchSite(); site != "" {
		var err error
		device, err = a.keyPouchDevice(site, false)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			logger.New("KeyPouch").Warn("无法读取本机配对信息")
		}
	}
	a.connectKeyPouchRuntime(device)
}

func IsKeyPouchLink(raw string) bool {
	_, _, ok := keypouch.ParseLink(raw)
	return ok
}

// This native entry point is called by the OS protocol handler after backend
// startup. It isn't exposed to the Wails webview as a bound application method.
func HandleKeyPouchLink(a *App, raw string, focusWindow func(int) bool) {
	action, ticket, ok := keypouch.ParseLink(raw)
	if !ok {
		return
	}
	_ = a.startBackgroundTask(func(ctx context.Context) {
		if err := a.handleKeyPouchAction(ctx, action, ticket, focusWindow); err != nil && ctx.Err() == nil {
			_, _ = keyPouchDialog(ctx, runtime.MessageDialogOptions{Type: runtime.ErrorDialog, Title: "KeyPouch", Message: err.Error()})
		}
	})
}

func (a *App) handleKeyPouchAction(ctx context.Context, action, ticket string, focusWindow func(int) bool) error {
	a.keyPouchActionMu.Lock()
	defer a.keyPouchActionMu.Unlock()
	if ctx.Err() != nil || a.browserMgr == nil {
		return ctx.Err()
	}
	site := a.keyPouchSite()
	if site == "" {
		return errors.New(missingKeyPouchSite)
	}
	if action == "pair" {
		return a.pairKeyPouch(ctx, site, ticket)
	}
	return a.openKeyPouchRecord(ctx, site, ticket, focusWindow)
}

func keyPouchDialog(ctx context.Context, opts runtime.MessageDialogOptions) (string, error) {
	if goruntime.GOOS == "linux" {
		// Wails passes the message to GTK as a printf format. Record names and
		// emails are plain text and can contain literal percent signs.
		opts.Message = strings.ReplaceAll(opts.Message, "%", "%%")
	}
	return runtime.MessageDialog(ctx, opts)
}

func (a *App) pairKeyPouch(ctx context.Context, site, ticket string) error {
	origin, err := url.Parse(site)
	if err != nil {
		return errors.New("KeyPouch 站点地址无效")
	}
	choice, err := keyPouchDialog(ctx, runtime.MessageDialogOptions{
		Type: runtime.QuestionDialog, Title: "配对 KeyPouch",
		Message: "允许 " + origin.Host + " 与这台电脑配对吗？在网页点「创建并打开 Ant 实例」时，Ant 可接收该记录的 GPT 登录凭据，并回报实例状态。",
		Buttons: []string{"Yes", "No"}, DefaultButton: "No", CancelButton: "No",
	})
	if err != nil || !strings.EqualFold(choice, "Yes") {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	d, err := a.keyPouchDevice(site, true)
	if err != nil {
		return errors.New("无法保存本机配对信息")
	}
	label, err := os.Hostname()
	if err != nil || strings.TrimSpace(label) == "" || len(label) > 80 {
		label = "Ant Browser"
	}
	if err = d.Offer(ctx, ticket, label); err != nil {
		return err
	}
	a.connectKeyPouchRuntime(d)
	_, err = keyPouchDialog(ctx, runtime.MessageDialogOptions{Type: runtime.InfoDialog, Title: "KeyPouch 配对短码", Message: "本机短码为 " + d.Fingerprint() + "。请到 KeyPouch 网页核对并确认。"})
	return err
}

func confirmKeyPouchProfile(ctx context.Context, c keyPouchOpenClaim) bool {
	choice, err := keyPouchDialog(ctx, runtime.MessageDialogOptions{
		Type: runtime.QuestionDialog, Title: "关联已有 Ant 实例",
		Message: "本机已有邮箱为 " + c.Email + " 的实例。是否关联这条 KeyPouch 记录，将该实例加入「" + c.GroupName + "」分组并打开？原有分组和浏览器数据会保留。",
		Buttons: []string{"Yes", "No"}, DefaultButton: "No", CancelButton: "No",
	})
	return err == nil && ctx.Err() == nil && strings.EqualFold(choice, "Yes")
}

func (a *App) openKeyPouchRecord(ctx context.Context, site, ticket string, focusWindow func(int) bool) error {
	d, err := a.keyPouchDevice(site, false)
	if err != nil {
		return errors.New("请先在 KeyPouch 网页选择「配对这台电脑」")
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	path := "/api/ant/device/instance-opens/" + ticket + "/"
	var claim keyPouchOpenClaim
	if err = d.Post(ctx, path+"claim", struct{}{}, &claim); err != nil {
		return err
	}
	if claim.Action != "create-open" || claim.RecordID <= 0 || claim.GroupID <= 0 || strings.TrimSpace(claim.Email) == "" || strings.TrimSpace(claim.GroupName) == "" {
		return errors.New("KeyPouch 返回的打开请求无效")
	}
	id, reason := a.keyPouchOpenProfile(ctx, site, claim, d)
	if reason != "" {
		return d.Post(ctx, path+"fail", map[string]string{"reason": reason}, nil)
	}
	synced := false
	if claim.CredentialSync {
		synced = a.syncKeyPouchCredential(ctx, d, path, site, claim, id) == nil
	}
	if reason = a.openKeyPouchWindow(ctx, id, focusWindow); reason != "" {
		return d.Post(ctx, path+"fail", map[string]string{"reason": reason}, nil)
	}
	receipt := map[string]any{"profile_id": id, "opened": true}
	if claim.CredentialSync {
		receipt["credential_status"] = "failed"
		if synced && a.publishOpenedGPTCredential(ctx, id) == nil {
			receipt["credential_status"] = "synced"
		}
	}
	if err = d.Post(ctx, path+"complete", receipt, nil); err != nil {
		return err
	}
	a.notifyKeyPouchRuntime()
	return nil
}
