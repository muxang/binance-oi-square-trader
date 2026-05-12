import axios from 'axios'

const api = axios.create({
  baseURL: '/api/admin',
  timeout: 10_000,
})

export interface CollectorStatus {
  name: string
  last_tick_seconds: number  // Unix ts; 0 if never ran
  success_rate_5min: number  // 0-1; -1 if no data
  status: 'active' | 'stale' | 'unknown'
}

export interface DashboardData {
  balance_usdt: number
  daily_pnl: number
  open_positions: number
  consecutive_losses: number
  btc_30m_drop_pct: number
  halt_status: string
  halt_reason: string | null
  collectors: CollectorStatus[]
}

export const fetchDashboard = (): Promise<DashboardData> =>
  api.get<DashboardData>('/dashboard').then(r => r.data)
