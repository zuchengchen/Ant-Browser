// Pure parsers shared by the desktop account view and the KeyPouch read-only tab.
(() => {
  const labels = {
    name: /^(?:Name|Display name|姓名|名称|名稱|显示名称|顯示名稱)$/i,
    username: /^(?:Username|用户名|使用者名稱|用戶名稱)$/i,
    email: /^(?:Email|Email address|电子邮件|電子郵件|邮箱|電郵)$/i,
    plan: /^(?:Plan|Subscription|套餐|方案|订阅|訂閱)$/i,
    weekly: /^(?:Weekly limit|Weekly usage limit|每周限额|每週限額|每周额度|每週額度|周限额|週限額)$/i,
    short: /^(?:5.hour limit|5 小时限额|5 小時限額|5小时限额|5小時限額)$/i,
    resets: /^(?:Usage limit resets|额度重置|額度重設|用量限额重置|使用量限制重設|使用限额重置|重置卡)$/i,
    credits: /^(?:Credits|积分|積分|点数|點數)$/i,
  };
  const lines = text => String(text || '').split('\n').map(x => x.trim()).filter(Boolean);
  const valueAfter = (rows, label) => {
    const i = rows.findIndex(x => label.test(x));
    const value = i < 0 ? '' : rows[i + 1] || '';
    return Object.values(labels).some(re => re.test(value)) ? '' : value;
  };
  const section = (rows, start, end, otherEnd) => {
    const i = rows.findIndex(x => start.test(x));
    if (i < 0) return '';
    const tail = rows.slice(i + 1);
    const j = tail.findIndex(x => end.test(x) || otherEnd?.test(x));
    return tail.slice(0, j < 0 ? undefined : j).join('\n');
  };
  const numeric = value => {
    if (value == null || !/^(?:\d+|\d{1,3}(?:,\d{3})+)(?:\.\d+)?$/.test(String(value).trim())) return null;
    const n = Number(String(value).replaceAll(',', ''));
    return Number.isFinite(n) && n >= 0 ? n : null;
  };
  function parseAccount(text) {
    const rows = lines(text);
    const email = valueAfter(rows, labels.email);
    return {
      displayName: valueAfter(rows, labels.name), username: valueAfter(rows, labels.username),
      email: /^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(email) ? email : '',
      plan: valueAfter(rows, labels.plan),
    };
  }
  function parseWindow(text) {
    const left = text.match(/(\d+(?:\.\d+)?)\s*%\s*(?:left|remaining|剩余|剩餘)/i)
      || text.match(/(?:剩余|剩餘)\s*(\d+(?:\.\d+)?)\s*%/);
    const used = text.match(/(\d+(?:\.\d+)?)\s*%\s*used/i)
      || text.match(/(?:已用|已使用)\s*(\d+(?:\.\d+)?)\s*%/);
    const n = numeric((left || used)?.[1]);
    return {
      remainingPercent: n === null || n > 100 ? null : left ? n : 100 - n,
      resetText: lines(text).find(x => /^(?:Resets?\b|重置|重設)|(?:后|後)重[置設]|^于.+重置|^於.+重設/i.test(x)) || '',
    };
  }
  function parseUsage(text, weeklyOnly = false) {
    const rows = lines(text);
    const weekly = section(rows, labels.weekly, /^(?:Recent plan usage|近期方案用量|近期套餐用量|Usage limit resets|Credits|Connected App Limits|额度重置|用量限额重置|积分|積分)$/i, labels.short);
    const resets = section(rows, labels.resets, labels.credits);
    const available = resets.match(/(?:^|\n)(?:Available|可用|可用次数|可用次數)\s*\n\s*(\d+)\b/i);
    const creditText = section(rows, labels.credits, /^(?:Credit usage history|积分使用记录|積分使用記錄|积分使用历史)$/i);
    const credit = creditText.match(/([\d,.]+)\s*credits?\s*(?:remaining|left)/i)
      || creditText.match(/(?:剩余|剩餘)\s*([\d,.]+)\s*(?:积分|積分|点数|點數)/)
      || creditText.match(/([\d,.]+)\s*(?:积分|積分|点数|點數)\s*(?:剩余|剩餘)/);
    return {
      weekly: parseWindow(weekly),
      ...(weeklyOnly ? {} : { fiveHour: parseWindow(section(rows, labels.short, labels.weekly)) }),
      credits: numeric(credit?.[1]),
      resetCards: available ? numeric(available[1]) : /No (?:usage limit )?resets? available|暂无可用重置|沒有可用重設/i.test(resets) ? 0 : null,
      usageScope: /Chat conversations are not included/i.test(text)
        ? 'Codex、Work 等共享额度，不含 Chat 对话' : '',
    };
  }
  function parseBilling(text) {
    // Billing information, transaction history and payment methods are irrelevant.
    const detail = String(text || '').split(/\n(?:Balance|余额|餘額|Cancel plan|取消方案|取消订阅|Transaction history|交易记录|交易紀錄|Billing information|账单信息|帳單資訊|Payment methods|付款方式)\n/i)[0].slice(0, 1200).trim();
    const date = '([A-Za-z]{3,9}\\s+\\d{1,2},?\\s+\\d{4}|\\d{4}年\\s*\\d{1,2}月\\s*\\d{1,2}日|\\d{4}-\\d{2}-\\d{2})';
    const renewal = detail.match(new RegExp('(?:renews?|next billing date)[^\\n]*?\\b(?:on\\s+)?' + date, 'i'))
      || detail.match(new RegExp('(?:将于|將於|下次续费日期[：:]?|下次續費日期[：:]?)\\s*' + date + '[^\\n]*(?:续费|續費|续订|續訂)', 'i'));
    const expiry = detail.match(new RegExp('(?:expires?|ends?|access until|cancels?)[^\\n]*?(?:on\\s+)?' + date, 'i'))
      || detail.match(new RegExp('(?:将于|將於|有效期至|到期日期[：:]?)\\s*' + date + '[^\\n]*(?:到期|结束|結束|失效)?', 'i'));
    // A renewal date is not an expiration date; do not fill one from the other.
    const subscriptionDetail = lines(detail).filter(line => /^(?:ChatGPT\s+(?:Free|Go|Plus|Pro|Business|Team|Enterprise|Edu)\b)|(?:Your (?:plan|subscription)|你的(?:套餐|订阅)|您的(?:方案|訂閱))/.test(line)).join('\n');
    return { renewalDate: renewal?.[1] || '', expiryDate: renewal && expiry?.[1] === renewal[1] ? '' : expiry?.[1] || '', subscriptionDetail };
  }
  return {labels, lines, parseAccount, parseUsage, parseBilling};
})()
