export const logLevels = ['debug', 'info', 'warn', 'error'] as const

export type LogLevel = typeof logLevels[number]
