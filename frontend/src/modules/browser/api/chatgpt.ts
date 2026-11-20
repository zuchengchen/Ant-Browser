import { getBindings } from './runtime'

export interface ChatGPTUsageWindow { remainingPercent: number | null; resetText: string }
export interface ChatGPTAccountInfo {
  profileId: string
  status: 'ok' | 'partial' | 'login_required' | 'verification_required' | 'account_mismatch'
  checkedAt: string
  savedAt: string
  displayName: string
  username: string
  email: string
  plan: string
  renewalDate: string
  expiryDate: string
  subscriptionDetail: string
  weekly: ChatGPTUsageWindow
  fiveHour: ChatGPTUsageWindow
  credits: number | null
  resetCards: number | null
  usageScope: string
  warnings: string[]
}

export async function readChatGPTAccount(profileId: string): Promise<ChatGPTAccountInfo> {
  const bindings: any = await getBindings()
  if (!bindings?.BrowserChatGPTAccountRead || !(globalThis as any).go?.main?.App) {
    throw new Error('请在已更新的 Ant Browser 桌面应用中读取 ChatGPT 账户')
  }
  return bindings.BrowserChatGPTAccountRead(profileId)
}

export async function getSavedChatGPTAccount(profileId: string): Promise<ChatGPTAccountInfo | null> {
  const bindings: any = await getBindings()
  if (!bindings?.BrowserChatGPTAccountGet || !(globalThis as any).go?.main?.App) {
    throw new Error('请在已更新的 Ant Browser 桌面应用中查看 GPT 账户信息')
  }
  return bindings.BrowserChatGPTAccountGet(profileId)
}
