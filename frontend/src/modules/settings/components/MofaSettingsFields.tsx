import { FormItem, Input } from '../../../shared/components'
import type { MofaSettings } from '../api'

export interface MofaSettingsDraft {
  siteUrl: string
  username: string
  password: string
  totpSecret: string
  clearCredentials: boolean
}

export interface MofaSettingsFieldsProps {
  draft: MofaSettingsDraft
  saved: MofaSettings | null | undefined
  canReuseCredentials: boolean
  disabled: boolean
  error: string
  onChange: (update: Partial<MofaSettingsDraft>) => void
}

export function MofaSettingsFields({ draft, saved, canReuseCredentials, disabled, error, onChange }: MofaSettingsFieldsProps) {
  const hasSite = Boolean(draft.siteUrl.trim())
  const credentialsDisabled = !hasSite || draft.clearCredentials
  const passwordSaved = !credentialsDisabled && canReuseCredentials && saved?.passwordConfigured
  const totpSaved = !credentialsDisabled && canReuseCredentials && saved?.totpConfigured
  const credentialsSaved = Boolean(saved?.username || saved?.passwordConfigured || saved?.totpConfigured)
  const needsNewPassword = !credentialsDisabled && !!draft.username.trim() && !passwordSaved

  return (
    <fieldset className="space-y-4" disabled={disabled} aria-label="MOFA 导入设置">
      <FormItem
        label={<label htmlFor="mofa-site-url">MOFA 后台地址</label>}
        hint="支持 HTTPS 站点地址或 /admin/accounts 完整地址。"
      >
        <Input
          id="mofa-site-url"
          type="url"
          value={draft.siteUrl}
          placeholder="https://mofa.example.com/admin/accounts"
          autoComplete="off"
          spellCheck={false}
          onChange={event => onChange({ siteUrl: event.target.value })}
        />
      </FormItem>
      <div className="grid grid-cols-1 gap-4 md:grid-cols-3">
        <FormItem label={<label htmlFor="mofa-username">MOFA 管理员用户名</label>}>
          <Input
            id="mofa-username"
            value={credentialsDisabled ? '' : draft.username}
            disabled={credentialsDisabled}
            autoCapitalize="none"
            autoComplete="off"
            spellCheck={false}
            onChange={event => onChange({ username: event.target.value })}
          />
        </FormItem>
        <FormItem label={<label htmlFor="mofa-password">管理员密码</label>} required={needsNewPassword}>
          <Input
            id="mofa-password"
            type="password"
            value={credentialsDisabled ? '' : draft.password}
            placeholder={passwordSaved ? '已保存，留空沿用' : undefined}
            disabled={credentialsDisabled}
            aria-required={needsNewPassword}
            autoComplete="new-password"
            onChange={event => onChange({ password: event.target.value })}
          />
        </FormItem>
        <FormItem
          label={<label htmlFor="mofa-totp">管理员 TOTP 密钥</label>}
          hint="MOFA 管理员已启用验证器时填写其密钥。"
        >
          <Input
            id="mofa-totp"
            type="password"
            value={credentialsDisabled ? '' : draft.totpSecret}
            placeholder={totpSaved ? '已保存，留空沿用' : undefined}
            disabled={credentialsDisabled}
            autoComplete="new-password"
            onChange={event => onChange({ totpSecret: event.target.value })}
          />
        </FormItem>
      </div>
      {credentialsSaved && !canReuseCredentials && !credentialsDisabled && (
        <p className="text-xs text-[var(--color-text-muted)]">更改后台地址或用户名后，请填写新账号的密码和 TOTP 密钥（如已启用），或选择清除凭据。</p>
      )}
      {credentialsSaved && !hasSite && (
        <p className="text-xs text-[var(--color-text-muted)]">保存空地址时会清除已保存的管理员凭据。</p>
      )}
      {(credentialsSaved || saved === null) && hasSite && (
        <label className="flex w-fit items-center gap-2 text-sm text-[var(--color-text-secondary)]">
          <input
            type="checkbox"
            className="h-4 w-4 accent-[var(--color-accent)]"
            checked={draft.clearCredentials}
            onChange={event => onChange({ clearCredentials: event.target.checked })}
          />
          保存时清除管理员凭据
        </label>
      )}
      {error && <p role="alert" className="text-xs text-[var(--color-error)]">{error}</p>}
    </fieldset>
  )
}
