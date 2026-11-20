import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import vm from 'node:vm';

const readScript = name => fs.readFileSync(new URL(`../../backend/${name}.js`, import.meta.url), 'utf8');
const source = `(${readScript('chatgpt_account_reader')})(${readScript('chatgpt_settings_parser')})`;
const reader = vm.runInNewContext(source);
const plain = value => JSON.parse(JSON.stringify(value));

test('account fields are separate; missing username cannot become the email label', () => {
  assert.deepEqual(plain(reader.parseAccount('Account\nName\nExample\nUsername\nEmail\nexample+test@example.com\nPlan\nPlus')), {
    displayName: 'Example', username: '', email: 'example+test@example.com', plan: 'Plus',
  });
  assert.equal(reader.parseAccount('Name\nExample\nUsername\n@example\nEmail\nexample@example.com').username, '@example');
});

test('unknown quota, credits and resets are null, never zero', () => {
  const result = reader.parseUsage('Usage\nWeekly limit\nLoading…\nUsage limit resets\nLoading…\nCredits\nCredit usage history\n50 credits');
  assert.equal(result.weekly.remainingPercent, null);
  assert.equal(result.credits, null);
  assert.equal(result.resetCards, null);
});

test('zero balances are valid and reset history does not count as available resets', () => {
  const result = reader.parseUsage('Usage\n5-hour limit\nResets in 4h\n100% left\nWeekly limit\nResets in 2d 5h\n0% left\nRecent plan usage\n50%\nUsage limit resets\nAvailable\n0\nHistory\nReset used\n3\nCredits\n0 credits remaining\nCredit usage history\n500 credits');
  assert.deepEqual(plain(result.weekly), { remainingPercent: 0, resetText: 'Resets in 2d 5h' });
  assert.equal(result.fiveHour.remainingPercent, 100);
  assert.equal(result.credits, 0);
  assert.equal(result.resetCards, 0);
});

test('positive balances and used percentages retain units', () => {
  const result = reader.parseUsage('Weekly limit\n25.5% used\nResets tomorrow\nUsage limit resets\nAvailable\n2\nCredits\n1,234.5 credits remaining\nCredit usage history\n999 credits');
  assert.equal(result.weekly.remainingPercent, 74.5);
  assert.equal(result.credits, 1234.5);
  assert.equal(result.resetCards, 2);
});

test('out of range usage and missing weekly heading cannot use the 5-hour value', () => {
  assert.equal(reader.parseUsage('5-hour limit\n100% left\nCredits\n1 credit remaining').weekly.remainingPercent, null);
  assert.equal(reader.parseUsage('Weekly limit\n101% left').weekly.remainingPercent, null);
});

test('Chinese labels are parsed without guessing omitted balances', () => {
  assert.equal(reader.parseAccount('姓名\n示例\n用户名\n@example\n电子邮件\na@example.com\n套餐\nPlus').email, 'a@example.com');
  const u = reader.parseUsage('每周限额\n剩余 42%\n2 天后重置\n额度重置\n可用\n3\n积分\n剩余 20 积分');
  assert.equal(u.weekly.remainingPercent, 42);
  assert.equal(u.resetCards, 3);
  assert.equal(u.credits, 20);
});

test('renewal date is not expiration; payment details are excluded', () => {
  const b = reader.parseBilling("Billing\nChatGPT Plus\nYou've got 100% off Plus. Your plan renews (at $20.00/month) on Dec 7, 2026.\nUpgrade\nBalance\n$12.00\nBilling information\nprivate@example.com\nPayment methods\n1234");
  assert.equal(b.renewalDate, 'Dec 7, 2026');
  assert.equal(b.expiryDate, '');
  assert.ok(!b.subscriptionDetail.includes('private@'));
  assert.ok(!b.subscriptionDetail.includes('1234'));
  assert.equal(reader.parseBilling('Your plan ends on December 7, 2026.').expiryDate, 'December 7, 2026');
  assert.equal(reader.parseBilling('Billing\nFree plan').renewalDate, '');
});

test('Chinese renewal and expiry are kept separate', () => {
  const renewal = reader.parseBilling('账单\n你的套餐将于 2026年12月7日续费。');
  assert.equal(renewal.renewalDate, '2026年12月7日');
  assert.equal(renewal.expiryDate, '');
  assert.equal(reader.parseBilling('你的套餐将于 2026年12月7日到期。').expiryDate, '2026年12月7日');
});

function pageFixture({ login = false, challenge = false, email = 'expected@example.com', missing = false, delayedIdentity = false } = {}) {
  let stage = 'Account'; const clicked = [];
  const startedAt = Date.now();
  const content = {
    Account: `Account\nName\nExample\nUsername\n@example\nEmail\n${email}\nPlan\nPlus`,
    Usage: 'Usage\nWeekly limit\n75% left\nResets in 3d\nUsage limit resets\nAvailable\n1\nCredits\n12 credits remaining',
    Billing: 'Billing\nYour plan renews on Dec 7, 2026.',
  };
  const nodes = (missing ? [] : ['Account', 'Usage', 'Billing', 'Cancel plan', 'Use reset', 'Add more']).map(text => ({
    innerText: text, getBoundingClientRect: () => ({ width: 20, height: 20 }), click: () => { clicked.push(text); stage = text; },
  }));
  if (login) nodes.push({ innerText: 'Log in', getBoundingClientRect: () => ({ width: 10, height: 10 }) });
  return {
    clicked,
    reader: vm.runInNewContext(source, {
      URL, setTimeout, location: { hostname: 'chatgpt.com', protocol: 'https:', pathname: '/settings/account' },
      document: { title: challenge ? 'Just a moment…' : 'ChatGPT', querySelectorAll: () => nodes, querySelector: selector => selector === 'main' ? { innerText: delayedIdentity && stage === 'Account' && Date.now() - startedAt < 700 ? content.Account.replace('\n@example', '') : content[stage] } : null },
    }),
  };
}

test('logged-in read only visits Usage and Billing; never redeems or buys', async () => {
  const fixture = pageFixture();
  const result = await fixture.reader.read({ expectedEmail: 'expected@example.com' });
  assert.equal(result.status, 'ok');
  assert.deepEqual(fixture.clicked, ['Usage', 'Billing']);
  assert.equal(result.resetCards, 1);
});

test('wrong account stops before usage and billing', async () => {
  const fixture = pageFixture({ email: 'different@example.com' });
  const result = await fixture.reader.read({ expectedEmail: 'expected@example.com' });
  assert.equal(result.status, 'account_mismatch');
  assert.deepEqual(fixture.clicked, []);
  assert.equal(result.credits, null);
});

test('a late-loaded username is awaited before leaving Account', async () => {
  const fixture = pageFixture({ delayedIdentity: true });
  const result = await fixture.reader.read({ expectedEmail: 'expected@example.com', timeoutMs: 1500 });
  assert.equal(result.status, 'ok');
  assert.equal(result.username, '@example');
});

for (const [name, config, status] of [
  ['login', { login: true }, 'login_required'],
  ['verification', { challenge: true }, 'verification_required'],
  ['missing sections', { missing: true }, 'partial'],
]) test(`${name} has a recoverable state without fabricated balances`, async () => {
  const fixture = pageFixture(config);
  const result = await fixture.reader.read({ timeoutMs: 500 });
  assert.equal(result.status, status);
  assert.equal(result.credits, null);
  assert.deepEqual(fixture.clicked, []);
});

test('KeyPouch weekly parsing stops at a later five-hour heading and never exports a short window', () => {
  const summary = reader.parseUsage('Weekly limit\nLoading…\n5-hour limit\n100% left\nResets in 4h\nCredits\n0 credits remaining', true)
  assert.deepEqual(plain(summary.weekly), { remainingPercent: null, resetText: '' })
  assert.equal(summary.credits, 0)
  assert.ok(!Object.hasOwn(summary, 'fiveHour'))
})
