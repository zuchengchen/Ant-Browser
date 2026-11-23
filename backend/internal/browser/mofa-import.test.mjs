import assert from 'node:assert/strict'
import test from 'node:test'
import { accountManagerURL, configuredSite } from './builtinassets/mofa-import/site.js'

test('the MOFA address comes only from this extension and resolves to an HTTPS origin', async t => {
  t.mock.method(globalThis, 'fetch', async (url, options) => {
    assert.equal(url, 'chrome-extension://abcdef/site.json')
    assert.deepEqual(options, { cache: 'no-store', credentials: 'omit' })
    return Response.json({ site })
  })
  const previous = globalThis.chrome
  globalThis.chrome = { runtime: { getURL: path => 'chrome-extension://abcdef/' + path } }
  t.after(() => { globalThis.chrome = previous })
  let site = ''
  assert.equal(await configuredSite(), '')
  site = 'https://[0:0:0:0:0:0:0:1]:443'
  assert.equal(await configuredSite(), 'https://[::1]')
  for (const unsafe of [
    'http://example.test',
    'https://user:secret@example.test',
    'https://example.test/another/path',
    'https://example.test?private=value',
    'https://example.test#unwanted',
    'https://example.test\\',
    {},
  ]) {
    site = unsafe
    await assert.rejects(configuredSite)
  }
})

test('the toolbar opens the standard MOFA account manager', () => {
  assert.equal(accountManagerURL('https://mofa.example.test:8443'), 'https://mofa.example.test:8443/admin/accounts')
})
