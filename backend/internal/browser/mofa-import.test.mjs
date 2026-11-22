import assert from 'node:assert/strict'
import test from 'node:test'
import { configuredSite, importURL } from './builtinassets/mofa-import/site.js'

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

test('the import link sends its account hint in the fragment alone', () => {
  const email = 'subject+qa&probe@example.test'
  const target = new URL(importURL('https://mofa.example.test:8443', email))
  assert.equal(target.origin, 'https://mofa.example.test:8443')
  assert.equal(target.pathname, '/admin/accounts')
  assert.equal(target.search, '')
  assert.equal(new URLSearchParams(target.hash.slice(1)).get('ant-import'), email)
})
