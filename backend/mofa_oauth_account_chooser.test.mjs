import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'
import { runInNewContext } from 'node:vm'

const source = readFileSync(new URL('./mofa_oauth_account_chooser.js', import.meta.url), 'utf8')
const chooser = 'https://auth.openai.com/choose-an-account'
const email = 'tester+ant@example.test'

function account(text, { disabled = false, hidden = false } = {}) {
  let clicks = 0
  const label = {
    childElementCount: 0, textContent: text, closest: () => null, checkVisibility: () => !hidden,
    getBoundingClientRect: () => ({ width: 40, height: 14, x: 25, y: 10 }),
  }
  return {
    querySelectorAll: selector => selector === 'span' ? [label] : [],
    closest: () => null, checkVisibility: () => true, matches: selector => selector === ':disabled' && disabled,
    getBoundingClientRect: () => ({ width: 100, height: 30, x: 0, y: 0 }),
    contains: element => element === label,
    hit: label,
    click: () => { clicks++ },
    clicks: () => clicks,
  }
}

function select(accounts, { url = chooser, subframe = false, hidden = false, hit, userAction } = {}) {
  const listeners = new Map()
  const window = {
    addEventListener: (type, callback) => listeners.set(type, callback),
    removeEventListener: type => listeners.delete(type),
  }
  window.top = subframe ? {} : window
  const trySelect = runInNewContext(source, {
    location: new URL(url), window,
    document: {
      readyState: 'complete', visibilityState: hidden ? 'hidden' : 'visible',
      querySelectorAll: selector => selector === 'li button[name="session_id"]' ? accounts : [],
      elementFromPoint: () => hit ?? accounts[0]?.hit,
    },
    setTimeout: fn => { if (userAction) listeners.get('pointerdown')?.(userAction); fn() },
  })
  return trySelect([url, email])
}

test('clicks only the unique session button showing the verified address', async () => {
  const match = account(email.toUpperCase())
  const other = account('someone@example.test')
  assert.equal(await select([other, match], { hit: match.hit }), true)
  assert.equal(match.clicks(), 1)
  assert.equal(other.clicks(), 0)
})

test('leaves ambiguous, hidden, similar and disabled account choices alone', async () => {
  for (const accounts of [
    [account(email), account(email, { disabled: true })],
    [account(email, { disabled: true })],
    [account(email, { hidden: true })],
    [account('tester@example.test')],
  ]) {
    assert.equal(await select(accounts), false)
    assert.ok(accounts.every(record => record.clicks() === 0))
  }
})

test('ignores unrelated documents and yields to the user', async () => {
  for (const options of [
    { url: 'https://auth.openai.com/consent' },
    { url: 'https://other.example.test/choose-an-account' },
    { subframe: true },
    { hidden: true },
    { hit: {} },
    { userAction: { isTrusted: true } },
  ]) {
    const match = account(email)
    assert.equal(await select([match], options), false)
    assert.equal(match.clicks(), 0)
  }
})
