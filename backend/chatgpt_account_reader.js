// Runs only in the selected instance's ChatGPT tab. Read visible settings; never
// export cookies/tokens, inspect conversations, or invoke billing/reset actions.
(({labels, lines, parseAccount, parseUsage, parseBilling}) => {
  const visible = e => !!e && !!(e.getBoundingClientRect().width && e.getBoundingClientRect().height);
  const settingsText = () => {
    const root = document.querySelector('main') || document.querySelector('[role="dialog"]');
    return root?.innerText || '';
  };
  const loginRequired = () => location.hostname === 'auth.openai.com'
    || /\/(?:auth|login)(?:\/|$)/.test(location.pathname)
    || [...document.querySelectorAll('button,a')].some(e => visible(e) && /^(?:Log in|Sign in|登录|登入|登錄)$/i.test(e.innerText.trim()));
  const challenged = () => /just a moment|security verification/i.test(document.title)
    || !!document.querySelector('iframe[src*="challenges.cloudflare.com"]');
  async function waitFor(check, timeout) {
    const end = Date.now() + timeout;
    do { if (check()) return true; await new Promise(resolve => setTimeout(resolve, 350)); } while (Date.now() < end);
    return false;
  }
  async function openSection(label, isReady, timeout) {
    const deadline = Date.now() + timeout;
    const findButton = () => [...document.querySelectorAll('button,a,[role="tab"]')].find(e => visible(e) && label.test(e.innerText.trim() || e.getAttribute?.('aria-label') || ''));
    await waitFor(() => findButton() || loginRequired() || challenged(), timeout);
    const button = findButton();
    if (!button) return false;
    button.click();
    return waitFor(() => isReady(settingsText()) || loginRequired() || challenged(), Math.max(0, deadline - Date.now()));
  }
  async function read(options = {}) {
    if (!['chatgpt.com', 'auth.openai.com'].includes(location.hostname) || location.protocol !== 'https:') throw new Error('账户检查只能在 ChatGPT 官方页面执行');
    const timeout = Math.min(15000, Math.max(500, options.timeoutMs || 12000));
    const stopAt = Date.now() + 60000;
    const remainingTimeout = () => Math.max(0, Math.min(timeout, stopAt - Date.now()));
    const result = { status: 'partial', displayName: '', username: '', email: '', plan: '', renewalDate: '', expiryDate: '', subscriptionDetail: '', weekly: { remainingPercent: null, resetText: '' }, fiveHour: { remainingPercent: null, resetText: '' }, credits: null, resetCards: null, usageScope: '', warnings: [] };
    const interrupted = () => {
      if (challenged()) { result.status = 'verification_required'; result.warnings = ['请在实例浏览器中完成人机验证，随后自动继续。']; return true; }
      if (loginRequired()) { result.status = 'login_required'; result.warnings = ['请在实例浏览器中登录 ChatGPT，随后自动继续。']; return true; }
      return false;
    };
    await waitFor(() => {
      const account = parseAccount(settingsText());
      return (account.email && account.plan && account.displayName) || loginRequired() || challenged();
    }, remainingTimeout());
    if (interrupted()) return result;
    if (lines(settingsText()).some(x => labels.username.test(x))) {
      await waitFor(() => parseAccount(settingsText()).username || loginRequired() || challenged(), remainingTimeout());
      if (interrupted()) return result;
    }
    Object.assign(result, parseAccount(settingsText()));
    if (!result.email) { result.warnings.push('账户页面尚未加载完成，或网页布局已变化；请重试。'); return result; }
    if (options.expectedEmail && result.email.toLowerCase() !== options.expectedEmail.toLowerCase()) {
      result.status = 'account_mismatch'; result.warnings.push('页面登录邮箱与此实例指定的邮箱不同，已停止读取订阅和用量。'); return result;
    }
    if (await openSection(/^(?:Usage|用量|使用量)$/i, text => labels.weekly.test(lines(text).find(x => labels.weekly.test(x)) || '') || lines(text).some(x => labels.credits.test(x)), remainingTimeout())) {
      await waitFor(() => {
        const u = parseUsage(settingsText());
        return (u.weekly.remainingPercent !== null && u.credits !== null && u.resetCards !== null) || loginRequired() || challenged();
      }, remainingTimeout());
      if (interrupted()) return result;
      Object.assign(result, parseUsage(settingsText()));
    } else result.warnings.push('未能打开用量页面。');
    if (await openSection(/^(?:Billing|账单|帳單|付款)$/i, text => !!(parseBilling(text).renewalDate || parseBilling(text).expiryDate) || /^(Billing|账单|帳單)\n/.test(text), remainingTimeout())) {
      await waitFor(() => {
        const b = parseBilling(settingsText());
        return b.renewalDate || b.expiryDate || loginRequired() || challenged();
      }, remainingTimeout());
      if (interrupted()) return result;
      Object.assign(result, parseBilling(settingsText()));
    } else result.warnings.push('未能打开账单页面。');
    if (result.weekly.remainingPercent === null) result.warnings.push('页面未提供可识别的本周剩余额度。');
    if (result.credits === null) result.warnings.push('页面未提供可识别的积分余额。');
    if (result.resetCards === null) result.warnings.push('页面未提供可识别的可用重置卡数量。');
    if (!result.renewalDate && !result.expiryDate) result.warnings.push('页面未提供可识别的续费或到期日期。');
    if (!result.plan) result.warnings.push('页面未提供可识别的订阅类型。');
    if (!result.displayName) result.warnings.push('账户显示名尚未加载，或页面未提供。');
    result.status = result.warnings.length ? 'partial' : 'ok';
    return result;
  }
  return { read, parseAccount, parseUsage, parseBilling };
})
