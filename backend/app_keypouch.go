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

// A site-specific installer may supply this via -ldflags -X. A generic build
// takes its trusted origin from the local environment, never from a deep link.
var bundledKeyPouchURL string

const missingKeyPouchSite = "请设置 ANT_KEYPOUCH_URL 为你的 KeyPouch 站点地址，重新启动 Ant 后再配对。"

func keyPouchSite() string {
	if site := strings.TrimRight(strings.TrimSpace(os.Getenv("ANT_KEYPOUCH_URL")), "/"); site != "" {
		return site
	}
	return strings.TrimRight(strings.TrimSpace(bundledKeyPouchURL), "/")
}

func (a *App) keyPouchDevice(create bool) (*keypouch.Device, error) {
	if keyPouchSite() == "" {
		return nil, errors.New(missingKeyPouchSite)
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return nil, err
	}
	return keypouch.OpenDevice(dir, a.appRoot, keyPouchSite(), create)
}

func (a *App) restoreKeyPouchRuntime() {
	if keyPouchSite() == "" {
		return
	}
	d, err := a.keyPouchDevice(false)
	if err == nil {
		a.connectKeyPouchRuntime(d.Post)
	} else if !errors.Is(err, os.ErrNotExist) {
		logger.New("KeyPouch").Warn("无法读取本机配对信息")
	}
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
		a.keyPouchActionMu.Lock()
		defer a.keyPouchActionMu.Unlock()
		if ctx.Err() != nil || a.browserMgr == nil {
			return
		}
		var err error
		if keyPouchSite() == "" {
			err = errors.New(missingKeyPouchSite)
		} else if action == "pair" {
			err = a.pairKeyPouch(ctx, ticket)
		} else {
			err = a.openKeyPouchRecord(ctx, ticket, focusWindow)
		}
		if err != nil && ctx.Err() == nil {
			_, _ = keyPouchDialog(ctx, runtime.MessageDialogOptions{Type: runtime.ErrorDialog, Title: "KeyPouch", Message: err.Error()})
		}
	})
}

func keyPouchDialog(ctx context.Context, opts runtime.MessageDialogOptions) (string, error) {
	if goruntime.GOOS == "linux" {
		// Wails passes the message to GTK as a printf format. Record names and
		// emails are plain text and can contain literal percent signs.
		opts.Message = strings.ReplaceAll(opts.Message, "%", "%%")
	}
	return runtime.MessageDialog(ctx, opts)
}

func (a *App) pairKeyPouch(ctx context.Context, ticket string) error {
	site, err := url.Parse(keyPouchSite())
	if err != nil {
		return errors.New("KeyPouch 站点地址无效")
	}
	choice, err := keyPouchDialog(ctx, runtime.MessageDialogOptions{
		Type: runtime.QuestionDialog, Title: "配对 KeyPouch",
		Message: "允许 " + site.Host + " 与这台电脑上的 Ant Browser 配对，以创建实例并查看开关状态吗？",
		Buttons: []string{"Yes", "No"}, DefaultButton: "No", CancelButton: "No",
	})
	if err != nil || !strings.EqualFold(choice, "Yes") {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	d, err := a.keyPouchDevice(true)
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
	a.connectKeyPouchRuntime(d.Post)
	_, err = keyPouchDialog(ctx, runtime.MessageDialogOptions{Type: runtime.InfoDialog, Title: "KeyPouch 配对短码", Message: "本机短码为 " + d.Fingerprint() + "。请到 KeyPouch 网页核对并确认。"})
	return err
}

func confirmKeyPouchProfile(ctx context.Context, c keyPouchOpenClaim) bool {
	choice, err := keyPouchDialog(ctx, runtime.MessageDialogOptions{
		Type: runtime.QuestionDialog, Title: "关联已有 Ant 实例",
		Message: "本机的「" + c.GroupName + "」分组已有实例 " + c.Email + "。要将这条 KeyPouch 记录关联到该实例，并打开原有窗口和数据吗？",
		Buttons: []string{"Yes", "No"}, DefaultButton: "No", CancelButton: "No",
	})
	return err == nil && ctx.Err() == nil && strings.EqualFold(choice, "Yes")
}

func (a *App) openKeyPouchRecord(ctx context.Context, ticket string, focusWindow func(int) bool) error {
	d, err := a.keyPouchDevice(false)
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
	id, reason := a.keyPouchOpen(ctx, claim, d, focusWindow)
	if reason != "" {
		return d.Post(ctx, path+"fail", map[string]string{"reason": reason}, nil)
	}
	if err = d.Post(ctx, path+"complete", map[string]any{"profile_id": id, "opened": true}, nil); err != nil {
		return err
	}
	a.notifyKeyPouchRuntime()
	return nil
}
