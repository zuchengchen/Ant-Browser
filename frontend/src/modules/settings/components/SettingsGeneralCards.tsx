import { Card, FormItem, Input, Select, Switch } from '../../../shared/components'
import { MofaSettingsFields, type MofaSettingsFieldsProps } from './MofaSettingsFields'
import { logLevels, type LogLevel } from '../types'

interface BasicSettingsCardProps {
  siteUrl: string
  fallbackUrl: string
  siteDisabled: boolean
  siteError: string
  onSiteUrlChange: (value: string) => void
  mofa: MofaSettingsFieldsProps
}

export function SettingsBasicCard({
  siteUrl, fallbackUrl, siteDisabled, siteError, onSiteUrlChange, mofa,
}: BasicSettingsCardProps) {
  return (
    <Card title="基础设置">
      <div className="space-y-4">
        <FormItem
          label={<label htmlFor="keypouch-site-url">KeyPouch 站点地址</label>}
          hint={fallbackUrl ? `留空时使用默认地址 ${fallbackUrl}` : '请输入 HTTPS 地址。留空时使用环境变量或安装包提供的地址。'}
          error={siteError}
        >
          <Input
            id="keypouch-site-url"
            type="url"
            value={siteUrl}
            placeholder={fallbackUrl || 'https://keypouch.example.com'}
            disabled={siteDisabled}
            error={!!siteError}
            aria-invalid={!!siteError}
            spellCheck={false}
            onChange={e => onSiteUrlChange(e.target.value)}
          />
        </FormItem>
        <MofaSettingsFields {...mofa} />
      </div>
    </Card>
  )
}

interface SettingsDiagnosticsCardProps {
  enableNotifications: boolean
  onNotificationHistoryChange: (enabled: boolean) => void
  logLevel: LogLevel | null
  onLogLevelChange: (level: LogLevel) => void
  logLevelError: string
  onLogLevelRetry: () => void
  disabled: boolean
}

const logLevelOptions = logLevels.map(value => ({
  value,
  label: { debug: 'Debug', info: 'Info', warn: 'Warning', error: 'Error' }[value],
}))

export function SettingsDiagnosticsCard({
  enableNotifications, onNotificationHistoryChange, logLevel, onLogLevelChange, logLevelError, onLogLevelRetry, disabled,
}: SettingsDiagnosticsCardProps) {
  return (
    <Card title="日志与通知">
      <div className="space-y-4">
        <div className="flex items-center justify-between gap-4">
          <div>
            <p className="text-sm font-medium text-[var(--color-text-primary)]">保存通知历史</p>
            <p className="mt-0.5 text-xs text-[var(--color-text-muted)]">关闭后仍会显示即时提示。</p>
          </div>
          <Switch
            aria-label="保存通知历史"
            checked={enableNotifications}
            onChange={onNotificationHistoryChange}
            disabled={disabled}
          />
        </div>
        <FormItem label={<label htmlFor="app-log-level">日志级别</label>}>
          <div className="flex flex-wrap items-center gap-2">
            <Select
              id="app-log-level"
              className="w-36"
              value={logLevel ?? ''}
              disabled={disabled || logLevel === null}
              options={logLevel === null
                ? [{ value: '', label: logLevelError ? '暂不可用' : '读取中…' }]
                : logLevelOptions}
              aria-describedby={logLevelError ? 'app-log-level-error' : undefined}
              onChange={e => onLogLevelChange(e.target.value as LogLevel)}
            />
            {logLevel === null && logLevelError && (
              <button
                type="button"
                disabled={disabled}
                className="rounded px-2 py-1 text-xs font-medium text-[var(--color-accent)] hover:bg-[var(--color-accent-muted)] disabled:opacity-50"
                onClick={onLogLevelRetry}
              >
                重新读取
              </button>
            )}
          </div>
          {logLevelError && <p id="app-log-level-error" role="alert" className="text-xs text-[var(--color-error)]">{logLevelError}</p>}
        </FormItem>
      </div>
    </Card>
  )
}
