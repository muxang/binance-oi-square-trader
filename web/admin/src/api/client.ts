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

// ---- History ----

export interface HistoryItem {
  trade_id: number
  symbol: string
  direction: string
  entry_ts_ms: number
  exit_ts_ms: number
  hold_duration_ms: number
  entry_price: number
  exit_price: number
  qty: number
  realized_pnl: number
  exit_reason: string
  fees: number
  status: string
}

export interface HistoryData {
  total: number
  page: number
  items: HistoryItem[]
}

export interface HistoryParams {
  symbol?: string
  exit_reason?: string
  since?: number   // unix ms
  until?: number   // unix ms
  pnl_dir?: 'profit' | 'loss'
  page?: number
  page_size?: number
}

export const fetchPositionsHistory = (p: HistoryParams = {}): Promise<HistoryData> => {
  const params: Record<string, string> = {}
  if (p.symbol)     params.symbol     = p.symbol
  if (p.exit_reason) params.exit_reason = p.exit_reason
  if (p.since)      params.since      = String(p.since)
  if (p.until)      params.until      = String(p.until)
  if (p.pnl_dir)    params.pnl_dir    = p.pnl_dir
  if (p.page)       params.page       = String(p.page)
  if (p.page_size)  params.page_size  = String(p.page_size)
  return api.get<HistoryData>('/positions/history', { params }).then(r => r.data)
}

// ---- PnL ----

export type RangeKey = 'today' | 'week' | 'month' | 'all'

export interface CumulativePoint {
  date: string
  daily_pnl: number
  cumulative: number
}

export interface SymbolPnl {
  symbol: string
  realized_pnl: number
  trade_count: number
  win_count: number
}

export interface ExitReasonPnl {
  exit_reason: string
  count: number
  realized_pnl: number
}

export interface PnlStats {
  total_trades: number
  win_count: number
  loss_count: number
  win_rate: number
  total_pnl: number
  avg_pnl: number
  avg_win_pnl: number
  avg_loss_pnl: number
  avg_hold_ms: number
  profit_factor: number
}

const rangeParam = (range: RangeKey) => ({ params: { range: range === 'all' ? undefined : range } })

export const fetchPnlCumulative  = (range: RangeKey): Promise<CumulativePoint[]>  =>
  api.get<CumulativePoint[]>('/pnl/cumulative',    rangeParam(range)).then(r => r.data)

export const fetchPnlBySymbol    = (range: RangeKey): Promise<SymbolPnl[]>        =>
  api.get<SymbolPnl[]>('/pnl/by_symbol',           rangeParam(range)).then(r => r.data)

export const fetchPnlByExitReason = (range: RangeKey): Promise<ExitReasonPnl[]>   =>
  api.get<ExitReasonPnl[]>('/pnl/by_exit_reason',  rangeParam(range)).then(r => r.data)

export const fetchPnlStats       = (range: RangeKey): Promise<PnlStats>           =>
  api.get<PnlStats>('/pnl/stats',                  rangeParam(range)).then(r => r.data)
