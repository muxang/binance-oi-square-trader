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

// ---- Positions ----

export interface OpenPosition {
  trade_id: number
  symbol: string
  direction: string
  entry_ts_ms: number
  entry_price: number
  current_price: number
  current_qty: number
  margin: number
  hold_duration_ms: number
  unrealized_pnl: number
  unrealized_pnl_pct: number // % of margin
  margin_ratio: number       // 0-1; >0.8 danger
}

export interface RecentClosedTrade {
  trade_id: number
  symbol: string
  exit_ts_ms: number
  entry_price: number
  exit_price: number
  realized_pnl: number
  exit_reason: string
}

export interface PositionsOpenData {
  positions: OpenPosition[]
  recent: RecentClosedTrade[]
}

export const fetchPositionsOpen = (): Promise<PositionsOpenData> =>
  api.get<PositionsOpenData>('/positions/open').then(r => r.data)
