// 中国 trader 习惯: 红涨绿跌 (与 Grafana 默认相反)
export const colors = {
  up: '#f04864',
  down: '#30bf78',
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
