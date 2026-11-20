import { IsAppReady } from '../wailsjs/go/main/App'

const READY_POLL_MS = 250
const STARTUP_TIMEOUT_MS = 120_000

let readiness: Promise<void> | null = null

// Share the startup wait across list requests. Querying state also covers a
// missed app:ready event or navigation to a page after startup has completed.
export function waitForAppReady(): Promise<void> {
  const app = (globalThis as typeof globalThis & { go?: { main?: { App?: unknown } } }).go?.main?.App
  if (!app) return Promise.resolve()

  if (!readiness) {
    readiness = new Promise<void>((resolve, reject) => {
      let settled = false
      let retryTimer: ReturnType<typeof setTimeout> | undefined
      const finish = (settle: () => void) => {
        if (settled) return
        settled = true
        clearTimeout(timeoutTimer)
        clearTimeout(retryTimer)
        settle()
      }
      const timeoutTimer = setTimeout(() => {
        finish(() => reject(new Error('应用初始化超时，暂时无法加载数据，请稍后重新进入页面或查看日志')))
      }, STARTUP_TIMEOUT_MS)
      const check = async () => {
        try {
          if (await IsAppReady()) finish(resolve)
          else if (!settled) retryTimer = setTimeout(check, READY_POLL_MS)
        } catch (error) {
          finish(() => reject(error))
        }
      }
      void check()
    }).catch((error) => {
      // A later page visit can retry after a timeout or a genuine bridge error.
      readiness = null
      throw error
    })
  }
  return readiness
}
