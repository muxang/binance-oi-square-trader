import { useState, useMemo } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import {
  fetchUptrend,
  fetchUptrendFavorites, addUptrendFavorite, removeUptrendFavorite,
  fetchStockSymbols,
  type UptrendItem, type SignalType,
} from '../api/client'
import SymbolLink from './SymbolLink'

type ViewFilter = 'pass' | 'all' | 'favorites'

// Defensive helpers: tolerate undefined/NaN/Infinity from stale cache or
// schema-drift backend so the panel never crashes on render.
function pct(v: number | undefined): string {
  if (v == null || !isFinite(v)) return '—'
  return (v >= 0 ? '+' : '') + (v * 100).toFixed(2) + '%'
}
function num(v: number | undefined, d = 2): string {
  if (v == null || !isFinite(v)) return '—'
  return v.toFixed(d)
}
function fmtPrice(p: number | undefined): string {
  if (p == null || !isFinite(p) || p === 0) return '—'
  if (p >= 1000) return p.toLocaleString('en-US', { maximumFractionDigits: 2 })
  if (p >= 1)    return p.toFixed(4)
  if (p >= 0.01) return p.toFixed(6)
  return p.toFixed(8)
}
function fmtVol(v: number | undefined): string {
  if (v == null || !isFinite(v)) return '—'
  if (v >= 1e9) return (v / 1e9).toFixed(2) + 'B'
  if (v >= 1e6) return (v / 1e6).toFixed(2) + 'M'
  if (v >= 1e3) return (v / 1e3).toFixed(2) + 'K'
  return v.toFixed(0)
}
function ratio(a: number | undefined, b: number | undefined, d = 3): string {
  if (a == null || b == null || !isFinite(a) || !isFinite(b) || b === 0) return '—'
  return (a / b).toFixed(d)
}

const ok   = 'text-green-400'
const bad  = 'text-red-400'
const warn = 'text-yellow-400'
const dim  = 'text-gray-500'

// 3-group pass count: baseTrend / relStrength / entrySignal (= breakout OR pullback)
function groupCount(it: UptrendItem): number {
  const entry = it.cond_breakout || it.cond_pullback
  return (it.cond_base_trend ? 1 : 0) +
         (it.cond_rel_strength ? 1 : 0) +
         (entry ? 1 : 0)
}

const signalCfg: Record<SignalType, { label: string; cls: string }> = {
  BREAKOUT:              { label: '突破',      cls: 'bg-blue-700/40   text-blue-300   border-blue-600/50' },
  PULLBACK:              { label: '回踩',      cls: 'bg-orange-700/40 text-orange-300 border-orange-600/50' },
  BREAKOUT_AND_PULLBACK: { label: '突破+回踩', cls: 'bg-purple-700/40 text-purple-300 border-purple-600/50' },
  NONE:                  { label: '—',         cls: 'bg-[#252525]     text-gray-600   border-[#3d3d3d]' },
}

function SignalBadge({ type }: { type: SignalType }) {
  const c = signalCfg[type]
  return (
    <span className={`px-1.5 py-0.5 rounded border text-[10px] font-semibold whitespace-nowrap ${c.cls}`}>
      {c.label}
    </span>
  )
}

export default function UptrendPanel({ onSelect }: { onSelect?: (sym: string) => void }) {
  const [search, setSearch] = useState('')
  const [view, setView] = useState<ViewFilter>('pass')
  const [limit, setLimit] = useState(50)
  // R.31 follow: stock-token filter. Defaults to showing them (less surprising
  // default — user opts out when crypto-focused).
  const [showStocks, setShowStocks] = useState(true)
  const qc = useQueryClient()

  // Favorites view always fetches the full evaluated set (passing=false) so
  // a favorited but-not-currently-passing symbol still shows up. Other views
  // honor the user's pass/all toggle.
  const wantAll = view !== 'pass'
  const { data, isLoading, isFetching } = useQuery({
    queryKey: ['uptrend', search, wantAll, limit, view],
    queryFn: () => fetchUptrend({
      passing: !wantAll,
      search: search || undefined,
      // In favorites view we don't paginate — backend cap is 500, sufficient.
      limit: view === 'favorites' ? 500 : limit,
    }),
    refetchInterval: 60_000,
    placeholderData: (prev) => prev,
  })

  // R.28: favorites — Redis-backed user watchlist, one HTTP call across the
  // tree (deduped by React Query).
  const { data: favData } = useQuery({
    queryKey: ['uptrend-favorites'],
    queryFn: fetchUptrendFavorites,
    staleTime: 30_000,
  })
  const favSet = useMemo(() => new Set(favData?.symbols ?? []), [favData])

  // R.31 follow: stock symbol set. Same queryKey as SymbolLink's internal hook
  // so React Query dedupes — only one HTTP call across the tree.
  const { data: stockData } = useQuery({
    queryKey: ['stock-symbols'],
    queryFn: fetchStockSymbols,
    staleTime: 30 * 60 * 1000,
    refetchInterval: 30 * 60 * 1000,
  })
  const stockSet = useMemo(() => new Set(stockData?.symbols ?? []), [stockData])

  const addFav = useMutation({
    mutationFn: (sym: string) => addUptrendFavorite(sym),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['uptrend-favorites'] }),
  })
  const removeFav = useMutation({
    mutationFn: (sym: string) => removeUptrendFavorite(sym),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['uptrend-favorites'] }),
  })

  // View-aware sort:
  //   pass:       backend's rel_strength desc preserved
  //   all:        pass true first, then group-count desc, then rel_strength
  //   favorites:  filter to favSet, then same as 'all' ordering
  const items = useMemo(() => {
    if (!data) return []
    let arr = data.items
    // R.31 follow: stock filter applies BEFORE view filter so favorites tab
    // also hides stocks when toggle is off.
    if (!showStocks && stockSet.size > 0) {
      arr = arr.filter(it => !stockSet.has(it.symbol))
    }
    if (view === 'favorites') {
      arr = arr.filter(it => favSet.has(it.symbol))
    }
    if (view === 'pass') return arr
    return [...arr].sort((a, b) => {
      if (a.pass !== b.pass) return a.pass ? -1 : 1
      const ga = groupCount(a), gb = groupCount(b)
      if (ga !== gb) return gb - ga
      return b.rel_strength - a.rel_strength
    })
  }, [data, view, favSet, showStocks, stockSet])

  // Count how many stocks are currently in the raw scan result (regardless of
  // current view/show toggle) — shows the user how many would be hidden.
  const stocksInScan = useMemo(() => {
    if (!data || stockSet.size === 0) return 0
    return data.items.reduce((n, it) => (stockSet.has(it.symbol) ? n + 1 : n), 0)
  }, [data, stockSet])

  return (
    <div className="space-y-3">
      <div className="flex items-center gap-2 flex-wrap">
        <input className="bg-[#252525] border border-[#3d3d3d] rounded px-2 py-1 text-xs text-gray-300 w-28 focus:outline-none"
          placeholder="Symbol..." value={search}
          onChange={e => setSearch(e.target.value.toUpperCase())} />
        <div className="flex gap-1 text-xs border border-[#3d3d3d] rounded p-0.5">
          {(['pass','all','favorites'] as ViewFilter[]).map(v => (
            <button key={v} onClick={() => setView(v)}
              className={`px-2 py-1 rounded ${view === v ? 'bg-blue-700 text-white' : 'text-gray-400 hover:text-white'}`}>
              {v === 'pass' ? '通过' : v === 'all' ? '全部(调试)' : `★ 自选${favSet.size ? ` (${favSet.size})` : ''}`}
            </button>
          ))}
        </div>
        {/* R.31 follow: hide/show stock-backed perpetuals (📈). */}
        <label className="flex items-center gap-1 text-xs text-gray-400 cursor-pointer select-none"
               title={`Binance Futures 股票合约 (underlyingType=EQUITY), 共 ${stockSet.size} 个`}>
          <input type="checkbox" checked={showStocks} onChange={e => setShowStocks(e.target.checked)}
            className="accent-blue-600" />
          📈 股票{stocksInScan > 0 ? ` (${stocksInScan})` : ''}
        </label>
        {view !== 'favorites' && (
          <select value={limit} onChange={e => setLimit(Number(e.target.value))}
            className="bg-[#252525] border border-[#3d3d3d] rounded px-2 py-1 text-xs text-gray-300 focus:outline-none ml-auto">
            <option value={50}>50</option>
            <option value={100}>100</option>
            <option value={200}>200</option>
          </select>
        )}
        {data && (
          <span className={`text-xs text-gray-500 ${view === 'favorites' ? 'ml-auto' : ''}`}>
            {view === 'favorites'
              ? `${items.length}/${favSet.size} 显示中 · ${isFetching ? '刷新中' : '60s 刷新'}`
              : `${data.passing}/${data.total} 通过 · ${isFetching ? '刷新中' : '60s 刷新'}`}
          </span>
        )}
      </div>

      <div className="text-[11px] text-gray-500 px-1 leading-relaxed">
        信号 = <b className="text-gray-300">baseTrend</b> &amp; <b className="text-gray-300">relStrength</b> &amp; (<b className="text-blue-400">breakout</b> OR <b className="text-orange-400">pullback</b>)
        <br/>
        <span className="text-gray-600">
          baseTrend: close&gt;EMA50 &amp; EMA20&gt;EMA50 &amp; EMA20↗ &amp; 4h close&gt;4h EMA20 ·
          relStrength: token 4h &gt;= BTC 4h − 0.5pp ·
          <span className="text-blue-500"> breakout</span>: close&gt;High20×1.002, vol&gt;1.5×MA, RSI&gt;55, ADX&gt;20, +DI&gt;−DI ·
          <span className="text-orange-500"> pullback</span>: 0.98EMA20≤close, low≤1.01EMA20, close&gt;EMA50, 45&lt;RSI&lt;70, vol≤1.3×MA
        </span>
      </div>

      <div className="bg-[#1f1f1f] border border-[#2d2d2d] rounded-lg overflow-x-auto">
        {isLoading && <div className="p-8 text-gray-500 text-sm text-center">加载中...</div>}
        {data && items.length === 0 && !isLoading && (
          <div className="p-8 text-gray-500 text-sm text-center">
            {view === 'favorites'
              ? (favSet.size === 0
                  ? '还没有自选 symbol — 在「通过」或「全部」视图中点 ☆ 添加'
                  : '自选 symbol 暂未出现在当前扫描结果中(可能数据缺失)')
              : view === 'all'
                ? '暂无评估数据(扫描尚未运行)'
                : '当前 0 个 symbol 通过 — 全市场偏弱或回调'}
          </div>
        )}
        {data && items.length > 0 && (
          <table className="w-full text-xs">
            <thead className="border-b border-[#2d2d2d]">
              <tr className="text-gray-500">
                <th className="text-left  py-2 px-2 font-medium">Symbol / 信号</th>
                <th className="text-right py-2 px-2 font-medium" title="最新已收盘 1h close">价格</th>
                <th className="text-right py-2 px-2 font-medium" title="EMA20 (1h)">EMA20</th>
                <th className="text-right py-2 px-2 font-medium" title="EMA50 (1h),绿=close&gt;EMA50">EMA50</th>
                <th className="text-right py-2 px-2 font-medium" title="EMA20 3根前,绿=斜率向上">EMA20[3]</th>
                <th className="text-right py-2 px-2 font-medium" title="4h close / 4h EMA20,绿=站上">4h Cl/EMA</th>
                <th className="text-right py-2 px-2 font-medium" title="前20根 1h 最高,绿=close>High20×1.002">High20</th>
                <th className="text-right py-2 px-2 font-medium" title="vol/MA20(前20根均量,优先 quoteVolume)">Vol×</th>
                <th className="text-right py-2 px-2 font-medium">RSI14</th>
                <th className="text-right py-2 px-2 font-medium">ADX14</th>
                <th className="text-right py-2 px-2 font-medium" title="+DI vs −DI 方向">±DI</th>
                <th className="text-right py-2 px-2 font-medium" title="(latest 4h close − open) / open">4h%</th>
                <th className="text-right py-2 px-2 font-medium" title="token 4h − BTC 4h,绿=≥−0.5pp">vs BTC</th>
              </tr>
            </thead>
            <tbody>
              {items.map((it: UptrendItem) => {
                const gc = groupCount(it)
                // Per-cell color independent: each color reflects ONE condition.
                const rsiOK = it.cond_breakout_rsi ||
                  (it.cond_pullback_rsi_min && it.cond_pullback_rsi_max)
                const volColor = it.cond_breakout_vol ? ok
                  : it.cond_pullback_vol ? warn
                  : bad
                return (
                  <tr key={it.symbol}
                      className="group border-b border-[#252525] last:border-b-0 hover:bg-[#252525] cursor-pointer"
                      title="点击行打开 OI/Square 详情侧栏"
                      onClick={() => onSelect?.(it.symbol)}>
                    {/* Symbol + status + ★ favorite + signal_type badge */}
                    <td className="py-2 px-2 font-mono text-gray-200 whitespace-nowrap">
                      {it.pass
                        ? <span className="text-green-500 mr-1.5" title="3/3 通过 → 触发提醒">●</span>
                        : <span className={`mr-1.5 text-[10px] tabular-nums font-semibold ${
                            gc >= 2 ? warn : gc === 1 ? 'text-orange-500' : 'text-gray-600'
                          }`} title={`${gc}/3 组通过 (baseTrend / relStrength / entry)`}>{gc}/3</span>
                      }
                      <button
                        onClick={(e) => {
                          e.stopPropagation()
                          if (favSet.has(it.symbol)) removeFav.mutate(it.symbol)
                          else                       addFav.mutate(it.symbol)
                        }}
                        title={favSet.has(it.symbol) ? '取消自选' : '加入自选(长期跟踪)'}
                        className={`mr-1 text-sm transition-colors ${
                          favSet.has(it.symbol)
                            ? 'text-yellow-400 hover:text-yellow-300'
                            : 'text-gray-600 hover:text-yellow-400'
                        }`}
                      >{favSet.has(it.symbol) ? '★' : '☆'}</button>
                      <SymbolLink symbol={it.symbol} />
                      <span className="ml-1.5"><SignalBadge type={it.signal_type} /></span>
                      {/* R.29 NEW: passing this hour AND wasn't in previous hour */}
                      {it.is_new_this_hour && (
                        <span className="ml-1 px-1.5 py-0.5 text-[9px] font-bold bg-cyan-700/40 text-cyan-300 border border-cyan-600/50 rounded animate-pulse"
                              title="本小时新出现的通过 — 上一小时未通过 finalSignal">
                          NEW
                        </span>
                      )}
                      {/* R.29 7d count: distinct hours pass=true in last 7 days */}
                      {it.pass_count_7d > 1 && (
                        <span className="ml-1 px-1.5 py-0.5 text-[9px] tabular-nums bg-purple-700/30 text-purple-300 border border-purple-600/40 rounded"
                              title={`过去 7 天内 ${it.pass_count_7d} 个不同小时通过过 finalSignal`}>
                          7d×{it.pass_count_7d}
                        </span>
                      )}
                    </td>
                    {/* 价格 */}
                    <td className="py-2 px-2 text-right tabular-nums text-gray-200 font-semibold">
                      {fmtPrice(it.close)}
                    </td>
                    {/* EMA20 — color: EMA20 > EMA50 (baseTrend) */}
                    <td className={`py-2 px-2 text-right tabular-nums ${it.cond_ema20_above_ema50 ? ok : bad}`}
                        title={`EMA20 ${fmtPrice(it.ema20)} ${it.cond_ema20_above_ema50 ? '>' : '≤'} EMA50 ${fmtPrice(it.ema50)}`}>
                      {fmtPrice(it.ema20)}
                    </td>
                    {/* EMA50 — color: close > EMA50 (baseTrend critical) */}
                    <td className={`py-2 px-2 text-right tabular-nums ${it.cond_close_above_ema50 ? ok : bad}`}
                        title={`close ${fmtPrice(it.close)} ${it.cond_close_above_ema50 ? '>' : '≤'} EMA50 ${fmtPrice(it.ema50)}`}>
                      {fmtPrice(it.ema50)}
                    </td>
                    {/* EMA20[3] — color: ema20 > ema20[3] (slope rising) */}
                    <td className={`py-2 px-2 text-right tabular-nums ${it.cond_ema20_rising ? ok : bad}`}
                        title={`EMA20 ${it.cond_ema20_rising ? '↗ 上升' : '↘ 未升'} vs 3 bars ago = ${fmtPrice(it.ema20_3bars_ago)}`}>
                      {fmtPrice(it.ema20_3bars_ago)} {it.cond_ema20_rising ? '↗' : '↘'}
                    </td>
                    {/* 4h MTF — color: 4h close > 4h EMA20 */}
                    <td className={`py-2 px-2 text-right tabular-nums ${it.cond_mtf_close4h_above_ema20 ? ok : bad}`}
                        title={`4h close ${fmtPrice(it.close_4h)} ${it.cond_mtf_close4h_above_ema20 ? '>' : '≤'} 4h EMA20 ${fmtPrice(it.ema20_4h)}`}>
                      {it.ema20_4h > 0 ? ratio(it.close_4h, it.ema20_4h) + '×' : '—'}
                    </td>
                    {/* High20 — color: close > High20 * 1.002 (breakout high) */}
                    <td className={`py-2 px-2 text-right tabular-nums ${it.cond_breakout_high ? ok : dim}`}
                        title={`close/High20 = ${num(it.breakout_ratio, 4)} ${it.cond_breakout_high ? '> 1.002 ✓ (突破)' : '≤ 1.002 (未突破)'}`}>
                      {fmtPrice(it.highest20)}
                    </td>
                    {/* Vol× — green if breakout vol, yellow if pullback vol, red if neither */}
                    <td className={`py-2 px-2 text-right tabular-nums ${volColor}`}
                        title={`vol ${fmtVol(it.volume)} / MA20 ${fmtVol(it.volume_ma20)} = ${num(it.vol_ratio, 2)}× · breakout >1.5 / pullback ≤1.3`}>
                      {num(it.vol_ratio, 2)}×
                    </td>
                    {/* RSI14 — green if either signal's RSI condition met */}
                    <td className={`py-2 px-2 text-right tabular-nums ${rsiOK ? ok : bad}`}
                        title={`RSI14 ${num(it.rsi14, 2)} · breakout>55 ${it.cond_breakout_rsi?'✓':'✗'} · pullback 45-70 ${(it.cond_pullback_rsi_min && it.cond_pullback_rsi_max)?'✓':'✗'}`}>
                      {num(it.rsi14, 1)}
                    </td>
                    {/* ADX14 — green if >20 (breakout requirement only) */}
                    <td className={`py-2 px-2 text-right tabular-nums ${it.cond_breakout_adx ? ok : dim}`}
                        title={`ADX14 ${num(it.adx14, 2)} ${it.cond_breakout_adx ? '> 20 (有趋势,breakout 需要)' : '≤ 20 (震荡市,breakout 不满足;pullback 不要求)'}`}>
                      {num(it.adx14, 1)}
                    </td>
                    {/* ±DI — green if +DI > -DI */}
                    <td className={`py-2 px-2 text-right tabular-nums whitespace-nowrap ${it.cond_breakout_di_plus ? ok : bad}`}
                        title={`+DI ${num(it.plus_di14, 2)} ${it.cond_breakout_di_plus ? '>' : '≤'} −DI ${num(it.minus_di14, 2)}`}>
                      {it.cond_breakout_di_plus ? '▲' : '▼'}{num(it.plus_di14, 0)}|{num(it.minus_di14, 0)}
                    </td>
                    {/* 4h% — pct_4h direction */}
                    <td className={`py-2 px-2 text-right tabular-nums font-semibold ${it.pct_4h >= 0 ? ok : bad}`}>
                      {pct(it.pct_4h)}
                    </td>
                    {/* vs BTC — green if rel_strength >= -0.005 */}
                    <td className={`py-2 px-2 text-right tabular-nums ${it.cond_rel_strength ? ok : bad}`}
                        title={`token 4h ${pct(it.pct_4h)} − BTC 4h ${pct(it.btc_pct_4h)} = ${pct(it.rel_strength)} (需 ≥ −0.5pp)`}>
                      {pct(it.rel_strength)}
                    </td>
                  </tr>
                )
              })}
            </tbody>
            <tfoot>
              <tr className="text-[10px] text-gray-600 border-t border-[#2d2d2d]">
                <td colSpan={13} className="py-1.5 px-2">
                  💡 行点击 → OI/Square 详情侧栏 · symbol 文字 → 币安永续 · 悬停每格看完整阈值对比 · 调试模式按组通过数排序
                </td>
              </tr>
            </tfoot>
          </table>
        )}
      </div>
    </div>
  )
}
