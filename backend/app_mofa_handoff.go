package backend

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"
	"unicode"
)

const mofaAccountManagerPath = "/admin/accounts"

var errMofaHandoff = errors.New("无法在所选实例中打开 MOFA 账号管理，请检查站点和网络连接")

func (b mofaBrowser) openAccountManager(parent context.Context, site string, result mofaStockResult) (opened bool, err error) {
	origin, err := normalizeMofaSite(site)
	if err != nil || origin == "" {
		return false, errMofaSite
	}
	ctx, cancel := context.WithTimeout(parent, 12*time.Second)
	defer cancel()
	tab, closeTab, err := b.openTab(ctx, false)
	if err != nil {
		return false, err
	}
	defer func() {
		if parent.Err() != nil {
			opened, err = false, parent.Err()
		}
		if err != nil {
			closeTab()
		}
	}()
	page, closeCDP, err := chatGPTCDP(ctx, tab)
	if err != nil {
		return false, errMofaBrowser
	}
	defer closeCDP()
	// Stock MOFA restores the full login destination without applying account
	// filters from the URL. Give it a destination without an account hint.
	nav, err := page("Page.navigate", map[string]any{"url": origin + mofaAccountManagerPath})
	frameID, _ := nav["frameId"].(string)
	if err != nil || frameID == "" || nav["errorText"] != nil || nav["isDownload"] == true {
		return false, errMofaHandoff
	}

	search := strings.TrimSpace(result.SearchHint)
	if len(search) > 1024 || strings.ContainsFunc(search, unicode.IsControl) {
		search = ""
	}
	verify := result.Action == "reauthorization_required" && result.AccountID > 0
	accountID := strconv.FormatInt(result.AccountID, 10)
	phase := "inspect"
	var contextID float64
	var matchedPage bool
	var prepareUntil time.Time
	ticker := time.NewTicker(150 * time.Millisecond)
	defer ticker.Stop()
	for ctx.Err() == nil && (prepareUntil.IsZero() || time.Now().Before(prepareUntil)) {
		if phase == "inspect" {
			world, err := page("Page.createIsolatedWorld", map[string]any{"frameId": frameID, "worldName": "ant-mofa-handoff"})
			contextID, _ = world["executionContextId"].(float64)
			if err != nil || contextID <= 0 {
				return false, errMofaHandoff
			}
		}
		state, err := mofaAccountManagerStep(page, contextID, origin, phase, search, accountID)
		if err != nil {
			break
		}
		switch state {
		case "foreign":
			return false, errMofaHandoff
		case "login", "manual":
			return false, nil
		case "accounts":
			matchedPage = true
		case "ready":
			matchedPage = true
			if search == "" {
				return false, nil
			}
			phase = "prefill"
			continue
		case "prefilled":
			if !verify {
				return false, nil
			}
			phase = "menu"
			prepareUntil = time.Now().Add(5 * time.Second)
		case "requested":
			phase = "dialog"
		case "opened":
			return true, nil
		}
		select {
		case <-ctx.Done():
		case <-ticker.C:
		}
	}
	if matchedPage {
		return false, nil
	}
	return false, errMofaHandoff
}

func mofaAccountManagerStep(page gptSessionCall, contextID float64, origin, phase, search, accountID string) (string, error) {
	arguments := []string{origin, mofaAccountManagerPath, phase}
	if phase != "inspect" {
		arguments = append(arguments, search, accountID)
	}
	encoded, err := json.Marshal(arguments)
	if err != nil {
		return "", err
	}
	result, err := page("Runtime.evaluate", map[string]any{
		"contextId": contextID, "returnByValue": true, "awaitPromise": true,
		"expression": "(" + mofaAccountManagerScript + ")(" + string(encoded) + ")",
	})
	remote, _ := result["result"].(map[string]any)
	state, _ := remote["value"].(string)
	return state, err
}

// The visible controls are from Sub2API 0.2.19.77. A changed layout falls back
// to its account listing. The only click effects here are opening the row menu
// and displaying the built-in dialog.
const mofaAccountManagerScript = `async ([origin, path, phase, search, accountID]) => {
  const onAccountsPage = () => window === window.top && window.isSecureContext &&
    location.origin === origin && location.pathname === path && !location.search && !location.hash
  if (!onAccountsPage()) {
    if (window === window.top && location.origin === origin && location.pathname === '/login') return 'login'
    return phase === 'inspect' && location.href === 'about:blank' ? 'pending' : 'foreign'
  }
  const visible = node => node instanceof HTMLElement && !node.closest('[hidden], [aria-hidden="true"], [inert]') &&
    node.checkVisibility({checkOpacity: true, checkVisibilityCSS: true}) && node.getClientRects().length > 0
  const matching = (root, selector, predicate = () => true) =>
    Array.from(root.querySelectorAll(selector)).filter(node => visible(node) && predicate(node))
  const only = nodes => nodes.length === 1 ? nodes[0] : null
  const text = node => node?.textContent.trim() || ''
  const dialogs = () => matching(document, '[role="dialog"]')
  const menus = () => matching(document, '.action-menu-content')
  const canClick = button => {
    if (!visible(button) || button.disabled || button.getAttribute('aria-disabled') === 'true') return false
    const rect = button.getBoundingClientRect()
    const hit = document.elementFromPoint(rect.x + rect.width / 2, rect.y + rect.height / 2)
    return button.contains(hit)
  }

  if (phase === 'inspect' || phase === 'prefill') {
    const input = only(matching(document, 'input[type="text"][placeholder]', node =>
      ['搜索账号...', 'Search accounts...'].includes(node.placeholder)))
    if (phase === 'inspect') return input ? 'ready' : 'accounts'
    if (!input || input.disabled || input.readOnly || dialogs().length || (input.value && input.value !== search)) return 'manual'
    if (input.value !== search) {
      input.value = search
      input.dispatchEvent(new Event('input', {bubbles: true}))
    }
    return 'prefilled'
  }

  if (phase === 'dialog') {
    const dialog = only(dialogs())
    const title = document.getElementById(dialog?.getAttribute('aria-labelledby') || '')
    const account = dialog && only(matching(dialog, 'span.block.font-semibold', node => text(node) === search))
    return account && visible(title) && ['重新授权账号', 'Re-Authorize Account'].includes(text(title)) ? 'opened' : 'pending'
  }

  // The stock desktop table renders its account ID as the row key even if the
  // ID column is hidden. The row's name cell must independently match.
  if (phase !== 'menu' || document.visibilityState !== 'visible' || dialogs().length || menus().length) return 'manual'
  const rows = matching(document, 'tr[data-row-id="' + accountID + '"]')
  if (rows.length !== 1) return rows.length ? 'manual' : 'pending'
  const row = rows[0]
  const heading = only(matching(row.closest('table'), 'th[scope="col"]', node => ['名称', 'Name'].includes(text(node))))
  const name = heading && row.cells[heading.cellIndex]
  const matchesName = () => name && only(matching(name, 'span.font-medium, a', node => text(node) === search))
  if (!matchesName()) return 'manual'
  const more = only(matching(row, 'button', node => ['更多', 'More'].includes(text(node))))
  if (!canClick(more)) return 'manual'
  more.click()
  await Promise.resolve()
  if (!onAccountsPage() || !visible(row) || row.getAttribute('data-row-id') !== accountID || !matchesName() || dialogs().length) return 'manual'
  const menu = only(menus())
  const action = menu && only(matching(menu, 'button', node => ['重新授权', 'Re-Authorize'].includes(text(node))))
  if (!canClick(action)) return 'manual'
  action.click()
  return 'requested'
}`
