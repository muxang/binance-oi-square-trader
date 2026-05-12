// 币圈习惯: 绿涨红跌 (Binance 惯例 — green=up/profit, red=down/loss)
export const colors = {
  up: '#30bf78',
  down: '#f04864',
  normal: '#52c41a',
  warning: '#faad14',
  halt: '#ff4d4f',
  card: '#1f1f1f',
  border: '#2d2d2d',
  muted: '#8c8c8c',
} as const

export function pnlColor(value: number): string {
  if (value > 0) return colors.up
  if (value < 0) return colors.down
  return colors.muted
}

export function pnlPrefix(value: number): string {
  return value > 0 ? '+' : ''
}

export function haltColor(status: string): string {
  return status === 'NORMAL' ? colors.normal : colors.halt
}
