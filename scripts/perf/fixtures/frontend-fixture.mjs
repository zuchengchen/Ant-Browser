// Fixed catalog inputs for Linux GUI comparisons. No product mocks or API replacement.
// Persist fixtureNow once in the parent run manifest and pass it to both builds.
export function makeFrontendCatalog(size, fixtureNow) {
  if (![100, 1000, 10000].includes(size)) throw new Error('Unsupported catalog size')
  if (!Number.isFinite(Date.parse(fixtureNow))) throw new Error('Invalid fixture time')
  const profiles = []
  const proxies = []
  for (let index = 0; index < size; index += 1) {
    const suffix = String(index).padStart(5, '0')
    const proxyId = `perf-proxy-${suffix}`
    proxies.push({
      proxyId,
      proxyName: `性能代理 ${suffix}`,
      proxyConfig: `http://127.0.0.1:${20000 + index}`,
      groupName: `性能分组 ${index % 10}`,
      sortOrder: index,
      lastTestOk: true,
      lastLatencyMs: 10 + index % 200,
      lastTestedAt: fixtureNow,
      lastTestEngine: 'fixture-display-only',
    })
    profiles.push({
      profileId: `perf-profile-${suffix}`,
      profileName: `性能实例 ${suffix}`,
      userDataDir: `perf-data/${suffix}`,
      coreId: 'perf-chromium',
      fingerprintArgs: [],
      proxyId,
      proxyConfig: '',
      proxyMode: 'fixed',
      memoryLimitMb: 0,
      launchArgs: [],
      tags: [`性能标签 ${index % 5}`],
      keywords: [`perf-keyword-${suffix}`],
      running: false,
      debugPort: 0,
      debugReady: false,
      pid: 0,
      runtimeWarning: '',
      lastError: '',
      createdAt: fixtureNow,
      updatedAt: fixtureNow,
      launchCode: `P${suffix}`,
    })
  }
  return { size, fixtureNow, profiles, proxies }
}

export const frontendScenarios = Object.freeze({
  sizes: [100, 1000, 10000],
  bindings: 'one fixed proxy per profile',
  initialLoad: 'all profile names and proxy labels rendered after real backend load',
  profileFilters: ['', '性能实例 0000', 'perf-keyword-00042', 'absent-fixture-query'],
  proxyFilters: ['', '性能代理 0000', '性能代理 00042', 'absent-fixture-query'],
  keywordUpdateTarget: 'perf-profile-00042',
  keywordUpdate: 'alternate perf-updated-00042-a and perf-updated-00042-b',
  resultFreshness: 'current-run fixed fixtureNow, same across before/after',
})
