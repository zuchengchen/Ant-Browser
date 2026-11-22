import assert from 'node:assert/strict'
import { createRequire, Module } from 'node:module'
import { dirname, resolve } from 'node:path'
import { test } from 'node:test'
import { fileURLToPath } from 'node:url'

const frontendRoot = resolve(dirname(fileURLToPath(import.meta.url)), '..')
const require = createRequire(resolve(frontendRoot, 'package.json'))
const { outputFiles } = require('esbuild').buildSync({
  stdin: {
    contents: `
      export { socks5ProxyInputProps } from './src/modules/browser/pages/proxyPool/socks5ProxyPaste'
      export { buildDirectImportCandidate, buildDirectImportCandidatesFromText }
        from './src/modules/browser/pages/proxyPool/helpers.direct'
      export { toast } from './src/shared/components/Toast'
    `,
    resolveDir: frontendRoot,
  },
  bundle: true, platform: 'node', format: 'cjs', packages: 'external', write: false,
})
const sourcePath = resolve(frontendRoot, 'scripts/proxy-url-import-test.cjs')
const testModule = new Module(sourcePath)
testModule.filename = sourcePath
testModule.paths = Module._nodeModulePaths(dirname(sourcePath))
testModule._compile(outputFiles[0].text, sourcePath)
const { socks5ProxyInputProps, buildDirectImportCandidate, buildDirectImportCandidatesFromText, toast } = testModule.exports

const proxyURL = 'socks5://%20demo%40lab%20:%20p%3Aa%40ss%25%20@127.0.0.1:1080'
const expectedFields = {
  protocol: 'socks5', server: '127.0.0.1', port: '1080', username: ' demo@lab ', password: ' p:a@ss% ',
}

function paste(props, text, current = {}) {
  const { tagName = 'INPUT', value = '', selectionStart = 0, selectionEnd = value.length } = current
  let prevented = false
  props.onPaste({
    currentTarget: { tagName, value, selectionStart, selectionEnd },
    clipboardData: { getData: format => format === 'text/plain' ? text : '' },
    preventDefault() { prevented = true },
  })
  return prevented
}

test('pasting a SOCKS5 URL fills the form and round-trips encoded user information', () => {
  const fields = []
  const props = socks5ProxyInputProps(patch => fields.push(patch))
  assert.equal(paste(props, proxyURL, { value: 'old.example.test', selectionStart: 3, selectionEnd: 3 }), true)
  assert.deepEqual(fields, [expectedFields])
  const candidate = buildDirectImportCandidate({ proxyName: 'Kept name', ...fields[0] })
  assert.deepEqual(candidate, { proxyName: 'Kept name', proxyConfig: proxyURL })
})

test('native hostname, subscription, YAML and multiline pastes keep their input behavior', () => {
  const props = socks5ProxyInputProps(() => assert.fail('Unexpected switch to the SOCKS5 form'))
  const second = 'socks5://127.0.0.2:1081'
  const batch = proxyURL + '\n' + second
  for (const text of ['host.example.test', 'https://example.test/sub', 'proxies:\n  - name: sample', batch]) {
    assert.equal(paste(props, text), false)
    props.onBlur({ currentTarget: { value: text } })
  }
  const afterFirstLine = { tagName: 'TEXTAREA', value: proxyURL + '\n', selectionStart: proxyURL.length + 1 }
  const insideYaml = { tagName: 'TEXTAREA', value: 'proxies:\n  - name: draft', selectionStart: 11, selectionEnd: 11 }
  assert.equal(paste(props, second, afterFirstLine), false)
  assert.equal(paste(props, proxyURL, insideYaml), false)
  const { candidates } = buildDirectImportCandidatesFromText(batch)
  assert.deepEqual(candidates.map(node => node.proxyConfig), [proxyURL, second])
})

test('a single SOCKS5 URL entered without clipboard data is recognized on blur', () => {
  const fields = []
  const props = socks5ProxyInputProps(patch => fields.push(patch))
  props.onBlur({ currentTarget: { value: '  ' + proxyURL + '  ' } })
  assert.deepEqual(fields, [expectedFields])
})

test('an unauthenticated IPv6 URL clears old credentials and preserves the selected name', () => {
  let form = { proxyName: 'Kept name', ...expectedFields }
  const props = socks5ProxyInputProps(fields => { form = { ...form, ...fields } })
  const address = 'socks5://[::1]:1082'
  assert.equal(paste(props, address), true)
  assert.deepEqual(form, {
    proxyName: 'Kept name', protocol: 'socks5', server: '::1', port: '1082', username: '', password: '',
  })
  assert.equal(buildDirectImportCandidate(form).proxyConfig, address)
})

test('invalid port and password without a username produce errors and preserve the form', t => {
  const reportError = t.mock.method(toast, 'error', () => {})
  const props = socks5ProxyInputProps(() => assert.fail('Invalid URL overwrote the form'))
  for (const text of ['socks5://127.0.0.1:65536', 'socks5://:sample@127.0.0.1:1080']) {
    assert.equal(paste(props, text), true)
  }
  assert.equal(reportError.mock.callCount(), 2)
})
