import axios from 'axios'

const api = axios.create({
  baseURL: '/api/admin',
  timeout: 10_000,
})

// Phase 5.2 Round 1: CSRF token cache + auto-fetch for write requests.
// On first write, browser prompts for Caddy basic auth (mu/password); subsequent
// writes reuse cached credentials. Token cached in sessionStorage (clears on tab close).
const CSRF_KEY = 'admin_csrf_token'
const CSRF_EXP_KEY = 'admin_csrf_expires_at'

interface CsrfResponse {
  token: string
  expires_at: string
}

export async function ensureCsrfToken(): Promise<string> {
  const cached = sessionStorage.getItem(CSRF_KEY)
  const exp = sessionStorage.getItem(CSRF_EXP_KEY)
  if (cached && exp && new Date(exp).getTime() > Date.now() + 60_000) {
    return cached  // valid + >60s remaining
  }
  // Fetch new (Caddy basic auth prompts browser on first call per session)
  const { data } = await api.get<CsrfResponse>('/csrf-token')
  sessionStorage.setItem(CSRF_KEY, data.token)
  sessionStorage.setItem(CSRF_EXP_KEY, data.expires_at)
  return data.token
}

// Interceptor: add X-CSRF-Token header to all non-GET requests.
api.interceptors.request.use(async (config) => {
  const method = (config.method ?? 'get').toLowerCase()
  if (method !== 'get' && method !== 'head' && method !== 'options') {
    const token = await ensureCsrfToken()
    config.headers.set('X-CSRF-Token', token)
  }
  return config
})

// Interceptor: on 403 csrf error, clear cache so next call re-fetches.
api.interceptors.response.use(undefined, (err) => {
  if (err.response?.status === 403 && err.response?.data?.error?.includes?.('csrf')) {
    sessionStorage.removeItem(CSRF_KEY)
    sessionStorage.removeItem(CSRF_EXP_KEY)
  }
  return Promise.reject(err)
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

// ---- Circuit Breaker (Round R.1 Part 2: manual reset) ----

export interface CBResetResponse {
  ok: boolean
  previous_halt_reason: string
  previous_halt_until: string
  manual_reset_at: string
  manual_reset_by: string
}

export const resetCircuitBreaker = (note?: string): Promise<CBResetResponse> =>
  api.post<CBResetResponse>('/circuit-breaker/reset', { confirm: true, actor: 'mu', note: note ?? '' }).then(r => r.data)

// ---- Round 2 write endpoints (Phase 5.2) ----

export interface SimpleAck { ok: boolean }

export const resetDailyPnl = (note?: string): Promise<SimpleAck> =>
  api.post<SimpleAck>('/circuit-breaker/daily-pnl-reset', { confirm: true, note: note ?? '' }).then(r => r.data)

export const resetConsecutiveLosses = (note?: string): Promise<SimpleAck> =>
  api.post<SimpleAck>('/circuit-breaker/consec-reset', { confirm: true, note: note ?? '' }).then(r => r.data)

export interface ManualHaltRequest {
  duration_hours: number
  note?: string
}
export const manualHalt = (req: ManualHaltRequest): Promise<SimpleAck> =>
  api.post<SimpleAck>('/circuit-breaker/halt', { confirm: true, ...req }).then(r => r.data)

// Threshold update payloads — all decimal-pct fields sent as string to preserve precision.
export interface CBThresholdsRequest {
  daily_loss_halt_pct?: string
  consecutive_losses_halt?: number
  total_float_loss_halt_pct?: string
  btc_panic_drop_pct?: string
  max_stop_pct?: string
  trail_stage1_activate_pct?: string
  trail_stage2_upgrade_pct?: string
  trail_stage3_upgrade_pct?: string
  trail_stage4_upgrade_pct?: string
  // Round 2.w: trail callback rates.
  trail_stage1_callback_rate?: string
  trail_stage2_callback_rate?: string
  trail_stage3_callback_rate?: string
  trail_stage4_callback_rate?: string
  // Round R.7 F2: API 错误率熔断阈值
  api_error_rate_limit?: number
  note?: string
}
export interface ThresholdsAck { ok: boolean; updated_keys: number }

export const updateCBThresholds = (req: CBThresholdsRequest): Promise<ThresholdsAck> =>
  api.put<ThresholdsAck>('/config/circuit-breaker-thresholds', { confirm: true, ...req }).then(r => r.data)

export interface SignalThresholdsRequest {
  oi_growth_from_min_pct?: string
  oi_surge_recent_periods?: number
  square_ratio_threshold?: string
  square_hot_acceleration_threshold?: string
  note?: string
}
export const updateSignalThresholds = (req: SignalThresholdsRequest): Promise<ThresholdsAck> =>
  api.put<ThresholdsAck>('/config/signal-thresholds', { confirm: true, ...req }).then(r => r.data)

export const watchlistInclude = (symbol: string, reason: string): Promise<SimpleAck> =>
  api.put<SimpleAck>(`/watchlist/include/${encodeURIComponent(symbol)}`, { confirm: true, reason }).then(r => r.data)

export const watchlistExclude = (symbol: string, reason: string): Promise<SimpleAck> =>
  api.put<SimpleAck>(`/watchlist/exclude/${encodeURIComponent(symbol)}`, { confirm: true, reason }).then(r => r.data)

export interface ManualCloseAck { ok: boolean; trade_id: number; symbol: string; note: string }
export const manualCloseTrade = (id: number, reason: string): Promise<ManualCloseAck> =>
  api.post<ManualCloseAck>(`/trades/${id}/close`, { confirm: true, reason }).then(r => r.data)

// ---- Audit Log (Round 3, public read) ----

export interface AuditLogEntry {
  id: number
  ts: string
  operator: string
  action_type: string
  resource_type: string
  resource_id: string
  previous_state: Record<string, unknown> | null
  new_state: Record<string, unknown> | null
  note: string
  ip_address: string
  user_agent: string
}
export interface AuditLogData {
  total: number
  page: number
  items: AuditLogEntry[]
}

export const fetchAuditLog = (page = 1, pageSize = 20, action?: string): Promise<AuditLogData> => {
  const params: Record<string, string> = { page: String(page), page_size: String(pageSize) }
  if (action) params.action = action
  return api.get<AuditLogData>('/audit-log', { params }).then(r => r.data)
}

// ---- Halt RCA (Round 4 Part 2) ----

export type RcaAction = 'resolved' | 'investigating' | 'ignored'

export interface HaltRCAEntry {
  id: number
  halt_type: string
  triggered_at: string
  context_json: Record<string, unknown>
  mu_acknowledged: boolean
  mu_action?: string
  mu_acknowledged_at?: string
  resolved_at?: string
}

export interface HaltRCAUnackData {
  total: number
  items: HaltRCAEntry[]
}

export const fetchHaltRCAUnack = (): Promise<HaltRCAUnackData> =>
  api.get<HaltRCAUnackData>('/halt-rca/unacknowledged').then(r => r.data)

export const ackHaltRCA = (id: number, action: RcaAction, note?: string): Promise<{ ok: boolean }> =>
  api.post<{ ok: boolean }>(`/halt-rca/${id}/ack`, { confirm: true, action, note: note ?? '' }).then(r => r.data)

// R.15: batch ack — flag every unacked halt_rca as ignored in one call.
export const ackAllHaltRCA = (note?: string): Promise<{ ok: boolean; count: number; ids: number[] }> =>
  api.post<{ ok: boolean; count: number; ids: number[] }>('/halt-rca/ack-all', { confirm: true, note: note ?? '' }).then(r => r.data)

export interface CBEvent {
  id: number
  ts: string
  event_type: string
  halt_reason: string
  halt_until_before: string | null
  actor: string
  daily_pnl_snapshot: string
  consecutive_losses_snapshot: number
  note: string
}

export const fetchCBEvents = (): Promise<{ events: CBEvent[] }> =>
  api.get<{ events: CBEvent[] }>('/circuit-breaker/events').then(r => r.data)

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

export const fetchPnlStats       = (range: RangeKey, ds?: DataSource): Promise<PnlStats>           =>
  api.get<PnlStats>('/pnl/stats',        { params: { ...rangeParam(range).params, data_source: ds } }).then(r => r.data)

export const fetchPnlCumulative2  = (range: RangeKey, ds?: DataSource): Promise<CumulativePoint[]>  =>
  api.get<CumulativePoint[]>('/pnl/cumulative',  { params: { ...rangeParam(range).params, data_source: ds } }).then(r => r.data)

export const fetchPnlBySymbol2    = (range: RangeKey, ds?: DataSource): Promise<SymbolPnl[]>        =>
  api.get<SymbolPnl[]>('/pnl/by_symbol',         { params: { ...rangeParam(range).params, data_source: ds } }).then(r => r.data)

export const fetchPnlByExitReason2 = (range: RangeKey, ds?: DataSource): Promise<ExitReasonPnl[]>  =>
  api.get<ExitReasonPnl[]>('/pnl/by_exit_reason',{ params: { ...rangeParam(range).params, data_source: ds } }).then(r => r.data)

// ---- data_source ----

export type DataSource = 'mainnet' | 'testnet' | 'all'

// ---- Market ----

export interface MarketItem {
  symbol: string
  cmcap_usd_m: number      // 流动市值 USD millions = circulating_supply × current_price; 0 = no data
  oi_usd_m: number
  oi_1h_pct: number
  oi_24h_pct: number
  current_price: number
  price_24h_pct: number
  square_mentions: number  // 24h mention count
  square_24h_pct: number   // vs prior 24h; 0 = no prior data
  // R.11.B1 contract-monitor.js 3 维度. 0 = collector 未运行 / 数据缺失.
  acct_ls_ratio: number    // 大户账户多空比 (top trader account-weighted)
  pos_ls_ratio: number     // 大户持仓多空比 (top trader position-weighted)
  mcap_ratio_pct: number   // OI_USD / market_cap × 100; 0 = supply 缺失
  in_watchlist: boolean
  in_open_position: boolean
}

export interface MarketData {
  total: number
  items: MarketItem[]
}

export type MarketScope = 'all' | 'watchlist' | 'positions'
export type MarketSort  = 'oi_1h_pct' | 'oi_24h_pct' | 'oi_usd' | 'price_24h_pct' | 'square' | 'square_24h_pct'
  | 'cmcap_usd' | 'acct_ls' | 'pos_ls' | 'mcap_pct'  // R.11.B1+
  | 'symbol' | 'current_price'  // R.12 mu 2026-05-21: 全字段可排
export type SortOrder   = 'asc' | 'desc'

export interface MarketParams {
  scope?: MarketScope
  sort?: MarketSort
  order?: SortOrder
  search?: string
  page?: number
  size?: number
}

export const fetchMarket = (p: MarketParams = {}): Promise<MarketData> => {
  const params: Record<string, string> = {}
  if (p.scope)  params.scope  = p.scope
  if (p.sort)   params.sort   = p.sort
  if (p.order)  params.order  = p.order
  if (p.search) params.search = p.search
  if (p.page)   params.page   = String(p.page)
  if (p.size)   params.size   = String(p.size)
  return api.get<MarketData>('/market', { params }).then(r => r.data)
}

// ---- R.24 Uptrend Discovery (composite: baseTrend AND relStrength AND (breakout OR pullback)) ----

export type SignalType = 'BREAKOUT' | 'PULLBACK' | 'BREAKOUT_AND_PULLBACK' | 'NONE'

export interface UptrendItem {
  symbol: string

  // 1h snapshot
  close: number
  low: number
  volume: number          // quote volume (USDT) when available
  volume_ma20: number     // SMA of PREVIOUS 20 bars (excludes current)
  highest20: number       // max(high) over PREVIOUS 20 bars

  // 1h indicators
  ema20: number
  ema20_3bars_ago: number
  ema50: number
  rsi14: number
  adx14: number
  plus_di14: number
  minus_di14: number

  // 4h snapshot + indicator
  close_4h: number
  ema20_4h: number

  // 4h relative strength
  pct_4h: number          // (latest 4h close − open) / open, decimal
  btc_pct_4h: number
  rel_strength: number    // pct_4h − btc_pct_4h, decimal

  // Derived ratios
  breakout_ratio: number  // close / highest20
  vol_ratio: number       // volume / volume_ma20
  close_to_ema20: number
  close_to_ema50: number

  // baseTrend sub-conditions
  cond_close_above_ema50: boolean
  cond_ema20_above_ema50: boolean
  cond_ema20_rising: boolean
  cond_mtf_close4h_above_ema20: boolean
  cond_base_trend: boolean

  // relStrength
  cond_rel_strength: boolean

  // breakoutSignal sub-conditions
  cond_breakout_high: boolean
  cond_breakout_vol: boolean
  cond_breakout_rsi: boolean
  cond_breakout_adx: boolean
  cond_breakout_di_plus: boolean
  cond_breakout: boolean

  // pullbackSignal sub-conditions (RSI band split)
  cond_pullback_close: boolean
  cond_pullback_low: boolean
  cond_pullback_above_ema50: boolean
  cond_pullback_rsi_min: boolean
  cond_pullback_rsi_max: boolean
  cond_pullback_vol: boolean
  cond_pullback: boolean

  pass: boolean
  signal_type: SignalType
  trigger_time: string    // RFC3339
}

export interface UptrendData {
  total: number    // total symbols evaluated
  passing: number  // count satisfying all 6 conditions
  items: UptrendItem[]
}

export interface UptrendParams {
  passing?: boolean   // default true; pass false to include partial matches
  search?: string
  limit?: number      // default 50
}

export const fetchUptrend = (p: UptrendParams = {}): Promise<UptrendData> => {
  const params: Record<string, string> = {}
  if (p.passing === false) params.passing = '0'
  if (p.search)            params.search  = p.search
  if (p.limit)             params.limit   = String(p.limit)
  return api.get<UptrendData>('/market/uptrend', { params }).then(r => r.data)
}

// ---- Square ----

export interface SquareTrendingItem {
  symbol: string
  mentions: number
  views: number
  likes: number
  latest_ts_ms: number
}

export interface SquareTrendingData {
  total: number
  items: SquareTrendingItem[]
}

export const fetchSquareTrending = (limit = 50): Promise<SquareTrendingData> =>
  api.get<SquareTrendingData>('/square/trending', { params: { limit } }).then(r => r.data)

// ---- Symbol Detail ----

export interface OiPoint             { ts_ms: number; oi_usd_m: number }
export interface PricePoint          { ts_ms: number; close: number }
export interface SquareMentionPoint  { ts_ms: number; mentions: number }
// R.11.B3: large_holder_ratios time series.
export interface RatioPoint          { ts_ms: number; acct_ratio: number; pos_ratio: number; mcap_pct: number }

export interface SymbolSquarePost {
  ts_ms: number; title: string; content: string; views: number; likes: number
}

export interface SymbolTrade {
  trade_id: number; entry_ts_ms: number; exit_ts_ms: number
  entry_price: number; exit_price: number; realized_pnl: number
  exit_reason: string; status: string; data_source: string
}

export interface SymbolDetailData {
  symbol: string
  current_price: number
  price_24h_pct: number
  oi_series:     OiPoint[]
  price_series:  PricePoint[]
  square_series: SquareMentionPoint[]
  square_posts:  SymbolSquarePost[]
  trades:        SymbolTrade[]
  ratios_series: RatioPoint[]   // R.11.B3
}

export const fetchSymbolDetail = (symbol: string, hours = 6, ds: DataSource = 'mainnet'): Promise<SymbolDetailData> =>
  api.get<SymbolDetailData>(`/symbol/${symbol}`, { params: { hours, data_source: ds } }).then(r => r.data)

// ---- Trade Detail ----

export interface TradeDetailSignal {
  signal_id: number
  ts_ms: number
  oi_triggered: boolean
  oi_data: Record<string, unknown> | null
  square_hot: boolean
  square_data: Record<string, unknown> | null
  decision: string
  rejection_reason?: string
}

export interface TradeDetailPosition {
  current_qty: number
  highest_price?: number
  trailing_stop_active: boolean
  trailing_stop_price?: number
  tp_stage1_done: boolean
  tp_stage2_done: boolean
  entry_oi?: number
  last_check_ts_ms?: number
}

export interface TradeDetailExit {
  ts_ms: number
  type: string
  qty: number
  price: number
  pnl: number
}

export interface TradeDetailApiError {
  ts_ms: number
  source: string
  endpoint: string
  http_code: number
  error_code: number
  message: string
}

export interface TradeDetailData {
  trade_id: number
  symbol: string
  direction: string
  status: string
  data_source: string
  margin: number
  notional: number
  leverage: number
  entry_ts_ms?: number
  entry_price?: number
  initial_atr?: number
  initial_stop_loss?: number
  initial_take_profit_1?: number
  initial_take_profit_2?: number
  exit_ts_ms?: number
  exit_price?: number
  exit_reason?: string
  realized_pnl?: number
  fees?: number
  signal: TradeDetailSignal | null
  position: TradeDetailPosition | null
  entry_ratios: TradeEntryRatios | null
  exits: TradeDetailExit[]
  api_errors: TradeDetailApiError[]
}

// R.11.B2: large_holder_ratios snapshot at/before entry. NULL if no row exists
// (pre-R.11 trades, or collector hadn't run for this symbol yet at entry time).
export interface TradeEntryRatios {
  snapshot_ts_ms: number
  acct_ls_ratio: number     // 0 = no data
  pos_ls_ratio: number
  open_interest_usd: number
  circulating_supply: number
  mcap_ratio_pct: number
}

export const fetchTradeDetail = (id: number): Promise<TradeDetailData> =>
  api.get<TradeDetailData>(`/trade/${id}`).then(r => r.data)

// R.12 Q2: CoinGecko mapping
export interface MappingRow {
  binance_symbol: string
  coingecko_id: string
  market_cap_usd_m: number
  circulating_supply: number
  in_watchlist: boolean
  in_open_position: boolean
  last_refreshed_ms: number
}
export interface MappingResponse { total: number; items: MappingRow[] }

export const fetchMappingList = (): Promise<MappingResponse> =>
  api.get<MappingResponse>('/coingecko-mapping').then(r => r.data)

export const updateMapping = (symbol: string, coingeckoId: string): Promise<{ status: string }> =>
  api.put<{ status: string }>(`/coingecko-mapping/${encodeURIComponent(symbol)}`,
    { coingecko_id: coingeckoId }).then(r => r.data)

// R.13: batch auto-fix mis-mapped symbols by /search canonical
export interface MappingAutoFixRow {
  symbol: string
  old_id: string
  new_id: string
  old_ratio_pct: number
  status: string  // 'fixed' | 'no_candidate' | 'no_change' | 'search_failed' | ...
}
export interface MappingAutoFixResponse {
  threshold_pct: number
  scanned: number
  fixed: number
  items: MappingAutoFixRow[]
}
export const autoFixMappings = (thresholdPct = 200): Promise<MappingAutoFixResponse> =>
  api.post<MappingAutoFixResponse>('/coingecko-mapping/auto-fix',
    { threshold_pct: thresholdPct }).then(r => r.data)

// R.14: price marks / alerts
export interface PriceMarkRow {
  id: number
  symbol: string
  target_price: string
  direction: 'above' | 'below'
  note: string
  status: 'active' | 'triggered'
  acknowledged: boolean
  current_price: string   // '' if unavailable
  triggered_price: string // '' until triggered
  triggered_at_ms: number // 0 until triggered
  created_at_ms: number
}
export interface PriceMarkListResponse {
  total: number
  unacked_triggered: number
  items: PriceMarkRow[]
}
export interface PriceMarkCreateReq {
  symbol: string
  target_price: string
  direction: 'above' | 'below'
  note?: string
  current_price?: string
}

export const fetchPriceMarks = (): Promise<PriceMarkListResponse> =>
  api.get<PriceMarkListResponse>('/price-marks').then(r => r.data)

export const createPriceMark = (req: PriceMarkCreateReq): Promise<{ status: string; id: number }> =>
  api.post<{ status: string; id: number }>('/price-marks', req).then(r => r.data)

export const ackPriceMark = (id: number): Promise<{ status: string }> =>
  api.post<{ status: string }>(`/price-marks/${id}/ack`, {}).then(r => r.data)

export const deletePriceMark = (id: number): Promise<{ status: string }> =>
  api.delete<{ status: string }>(`/price-marks/${id}`).then(r => r.data)
