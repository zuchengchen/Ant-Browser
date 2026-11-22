import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';

const source = await readFile(new URL('../../backend/internal/browser/builtinassets/keypouch-gpt/vault.js', import.meta.url));
const { normalizeVaultSite, VaultError, VAULT_PAGE_SIZE, getVaultSession, listVaultAccounts, getVaultAccount } =
  await import('data:text/javascript;base64,' + source.toString('base64'));

const site = 'https://vault.example.test:8443';
const email = 'Case+alias@example.test';
const privateFixture = 'private-fixture-never-display';
const record = {
  id: 5, username: email, password: '  fixture----!  ', totp_secret: 'SYNTHETIC', deleted_at: null,
  has_password: true, has_totp: true, fields: { notes: privateFixture },
};
const reply = (body, status = 200) => new Response(JSON.stringify(body), { status });
const hasCode = code => error => error instanceof VaultError && error.code === code && !error.message.includes(privateFixture);

test('normalizes exact HTTPS roots and loopback ports; rejects wildcard permissions and extraneous URL parts', () => {
  assert.equal(normalizeVaultSite(' HTTPS://VAULT.EXAMPLE.TEST:8443/ '), site);
  for (const origin of ['http://localhost:8080', 'http://127.0.1.2:4555', 'http://[::1]:3333']) {
    assert.equal(normalizeVaultSite(origin + '/'), origin);
  }
  assert.equal(normalizeVaultSite('http://[::ffff:127.0.0.42]:8000'), 'http://[::ffff:7f00:2a]:8000');
  for (const raw of ['https://', null, 'http://vault.example.test', 'http://127.0.0.1.evil.test', site + '/records',
    site + '/..', site + '?', site + '#', site + '?record=7', 'https://name@vault.example.test',
    'https://@example.test', 'https://*.example.test', 'https://%2a.example.test', 'https://vault.example.test:0',
    'https://vault.example.test:', 'https://vault.example.test:/', 'https://vault.example.test\\@evil.test']) {
    assert.throws(() => normalizeVaultSite(raw), hasCode('invalid_site'));
  }
});

test('uses the profile session with a credentialed read-only request; 401 allows sign-in and refresh', async t => {
  const signal = new AbortController().signal;
  const requests = t.mock.method(globalThis, 'fetch', async () => reply({ message: privateFixture }, 401));
  assert.equal(await getVaultSession(site, signal), false);
  const [url, options] = requests.mock.calls[0].arguments;
  assert.equal(url, site + '/api/session');
  const { signal: requestSignal, ...requestOptions } = options;
  assert.ok(requestSignal instanceof AbortSignal);
  assert.deepEqual(requestOptions, {
    method: 'GET', credentials: 'include', cache: 'no-store', redirect: 'error',
    referrerPolicy: 'no-referrer', headers: { Accept: 'application/json' },
  });
  requests.mock.mockImplementation(async () => reply({ user: { id: 8, username: 'test-owner', disabled: false }, csrf: privateFixture }));
  assert.equal(await getVaultSession(site), true);
  requests.mock.mockImplementation(async () => reply({ user: { id: 8, must_change_password: true } }));
  await assert.rejects(getVaultSession(site), hasCode('password_change_required'));
});

test('sends encoded search and raw page offset; exposes only usable email records and capability flags', async t => {
  const requests = t.mock.method(globalThis, 'fetch', async () => reply({
    total: 96,
    items: [
      record, { ...record, id: 6, username: email, has_password: false, has_totp: false },
      { ...record, id: 7, username: 'not an account' }, { ...record, id: 8, username: 'https://name@example.test' },
      { ...record, id: 9, username: '<someone@example.test>' }, { ...record, id: 10, deleted_at: '2026-01-01' },
      { ...record, id: -3 }, { ...record, id: 15, deleted_at: undefined }, null,
      { ...record, id: 16, username: '测试@例子.公司' },
    ],
  }));
  const results = await listVaultAccounts(site, '  +& /?#  ', VAULT_PAGE_SIZE);
  const url = new URL(requests.mock.calls[0].arguments[0]);
  assert.equal(url.origin + url.pathname, site + '/api/records');
  assert.equal(url.hash, '');
  assert.equal(url.searchParams.get('q'), '+& /?#');
  assert.equal(url.searchParams.get('offset'), '30');
  assert.equal(url.searchParams.get('limit'), '30');
  assert.equal(url.searchParams.get('sort'), 'username');
  assert.deepEqual(results, { total: 96, items: [
    { id: 5, email, hasPassword: true, hasTOTP: true },
    { id: 6, email, hasPassword: false, hasTOTP: false },
    { id: 16, email: '测试@例子.公司', hasPassword: true, hasTOTP: true },
  ] });
  await assert.rejects(listVaultAccounts(site, '', -1), hasCode('invalid_query'));
  assert.equal(requests.mock.callCount(), 1);
});

test('reads one selected record, preserving password whitespace and returning only login fields', async t => {
  const requests = t.mock.method(globalThis, 'fetch', async () => reply(record));
  assert.deepEqual(await getVaultAccount(site, 5, email), { email, password: record.password, totpSecret: record.totp_secret });
  assert.equal(requests.mock.calls[0].arguments[0], site + '/api/records/5');
  requests.mock.mockImplementation(async () => reply({ ...record, password: '', totp_secret: '' }));
  assert.deepEqual(await getVaultAccount(site, 5, email), { email, password: '', totpSecret: '' });
});

test('fails closed on swapped records, changed address, soft deletion and non-email records', async t => {
  const requests = t.mock.method(globalThis, 'fetch');
  for (const changes of [{ id: 6 }, { username: 'other@example.test' }, { username: 'record' },
    { deleted_at: '2025-06-04' }, { deleted_at: undefined }]) {
    requests.mock.mockImplementation(async () => reply({ ...record, ...changes }));
    await assert.rejects(getVaultAccount(site, 5, email), hasCode('record_changed'));
  }
  requests.mock.resetCalls();
  await assert.rejects(getVaultAccount(site, '5/../6', email), hasCode('record_changed'));
  await assert.rejects(getVaultAccount(site, 5, 'Note'), hasCode('record_changed'));
  await assert.rejects(getVaultAccount('http://remote.example.test', 5, email), hasCode('invalid_site'));
  assert.equal(requests.mock.callCount(), 0);
});

test('recognizes expired authentication and forced password changes without surfacing server-provided strings', async t => {
  const requests = t.mock.method(globalThis, 'fetch', async () => reply({ error: privateFixture, message: privateFixture }, 401));
  await assert.rejects(listVaultAccounts(site), hasCode('unauthorized'));
  requests.mock.mockImplementation(async () => reply({ error: 'password_change_required', message: privateFixture }, 403));
  await assert.rejects(getVaultAccount(site, 5, email), hasCode('password_change_required'));
  requests.mock.mockImplementation(async () => reply({ error: privateFixture, message: privateFixture }, 403));
  await assert.rejects(listVaultAccounts(site), hasCode('forbidden'));
  requests.mock.mockImplementation(async () => reply({ message: privateFixture }, 500));
  await assert.rejects(getVaultSession(site), hasCode('service_error'));
});

test('sanitizes network, redirect, and decoding failures; cancellation conceals its reason', async t => {
  const requests = t.mock.method(globalThis, 'fetch', async () => { throw new Error(privateFixture); });
  await assert.rejects(getVaultSession(site), hasCode('network'));
  const controller = new AbortController();
  controller.abort(privateFixture);
  await assert.rejects(getVaultSession(site, controller.signal), error => error.name === 'AbortError' && !error.message.includes(privateFixture));
  requests.mock.mockImplementation(async () => ({ status: 200, url: site + '/different', redirected: true }));
  await assert.rejects(getVaultSession(site), hasCode('network'));
  requests.mock.mockImplementation(async () => new Response(privateFixture));
  await assert.rejects(getVaultSession(site), hasCode('invalid_response'));
  requests.mock.mockImplementation(async () => reply({ ...record, password: undefined }));
  await assert.rejects(getVaultAccount(site, 5, email), hasCode('invalid_response'));
  requests.mock.mockImplementation(async () => reply({ items: [], total: privateFixture }));
  await assert.rejects(listVaultAccounts(site), hasCode('invalid_response'));
});

test('one 15-second deadline covers both the connection and reading the entire response body', async t => {
  const schedule = globalThis.setTimeout;
  const timers = t.mock.method(globalThis, 'setTimeout', (callback, ms) => {
    assert.equal(ms, 15_000);
    return schedule(callback, 5);
  });
  const waitForAbort = signal => new Promise((_, reject) => {
    signal.addEventListener('abort', () => reject(new Error(privateFixture)), { once: true });
  });
  const requests = t.mock.method(globalThis, 'fetch', (_url, { signal }) => waitForAbort(signal));
  await assert.rejects(getVaultSession(site), hasCode('timeout'));
  for (const status of [200, 403]) {
    requests.mock.mockImplementation(async (_url, { signal }) => ({ status, json: () => waitForAbort(signal) }));
    await assert.rejects(getVaultSession(site), hasCode('timeout'));
  }
  assert.equal(timers.mock.callCount(), 3);
});

test('caller can cancel a slow body read without forwarding its custom abort reason', async t => {
  const caller = new AbortController();
  let requestSignal;
  t.mock.method(globalThis, 'fetch', async (_url, { signal }) => ({
    status: 200,
    json: () => {
      requestSignal = signal;
      caller.abort(privateFixture);
      throw new Error(privateFixture);
    },
  }));
  await assert.rejects(getVaultSession(site, caller.signal), error => error.name === 'AbortError' && !error.message.includes(privateFixture));
  assert.equal(requestSignal.reason.name, 'AbortError');
  assert.notEqual(requestSignal.reason, caller.signal.reason);
});

test('a selected web session obtains a receipt for the exact record version with CSRF', async t => {
  const paths = [];
  const attempt = 'w'.repeat(32);
  const requests = t.mock.method(globalThis, 'fetch', async (url, options) => {
    paths.push(new URL(url).pathname);
    if (url.endsWith('/login-attempt')) {
      assert.equal(options.credentials, 'include');
      assert.equal(options.headers['X-CSRF-Token'], 'fixture-csrf');
      assert.deepEqual(JSON.parse(options.body), { version: 3 });
      return reply({ attempt });
    }
    return reply(url.endsWith('/session') ? { csrf: 'fixture-csrf' } : { ...record, version: 3 });
  });
  assert.equal((await getVaultAccount(site, 5, email, undefined, true)).attempt, attempt);
  assert.deepEqual(paths, ['/api/records/5', '/api/session', '/api/records/5/login-attempt']);
  requests.mock.mockImplementation(async url => url.endsWith('/session') ? reply({}, 401) : reply({ ...record, version: 3 }));
  assert.deepEqual(await getVaultAccount(site, 5, email, undefined, true), { email, password: record.password, totpSecret: record.totp_secret });
});
