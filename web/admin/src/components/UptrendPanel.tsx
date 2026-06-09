import { useState, useMemo } from 'react'
import { useQuery } from '@tanstack/react-query'
import { fetchUptrend, type UptrendItem } from '../api/client'
import SymbolLink from './SymbolLink'

// Count how many of the 6 conditions a symbol satisfies. Used in showAll mode
// to sort 6/6 first, 5/6, 4/6 ... so "almost passing" candidates surface to the top.
function passCount(it: UptrendItem): number {
  return (it.cond_ema_stack    ? 1 : 0) +
         (it.cond_breakout     ? 1 : 0) +
         (it.cond_vol_surge    ? 1 : 0) +
         (it.cond_rsi          ? 1 : 0) +
         (it.cond_adx          ? 1 : 0) +
         (it.cond_rel_strength ? 1 : 0)
}

function pct(v: number) { return (v >= 0 ? '+' : '') + (v * 100).toFixed(2) + '%' }
function num(v: number, d = 2) { return v.toFixed(d) }
function fmtPrice(p: number) {
  if (!p) return '—'
  if (p >= 1000) return p.toLocaleString('en-US', { maximumFractionDigits: 2 })
  if (p >= 1)    return p.toFixed(4)
  if (p >= 0.01) return p.toFixed(6)
  return p.toFixed(8)
}

// pass/fail tailwind utility — green if condition met, red if violated. Used per cell
// so a glance at the row reveals exactly which condition(s) failed.
const ok  = 'text-green-400'
const bad = 'text-red-400'

export default function UptrendPanel({ onSelect }: { onSelect?: (sym: string) => void }) {
  const [search, setSearch] = useState('')
  const [showAll, setShowAll] = useState(false)
  const [limit, setLimit] = useState(50)

  const { data, isLoading, isFetching } = useQuery({
    queryKey: ['uptrend', search, showAll, limit],
    queryFn: () => fetchUptrend({
      passing: !showAll,
      search: search || undefined,
      limit,
    }),
    refetchInterval: 60_000,
    placeholderData: (prev) => prev,
  })

  // showAll mode: re-sort by pass-count desc so 6/6 → 5/6 → 4/6 ... cluster from the top.
  // Stable sort preserves backend's rel_strength desc order within the same pass-count tier.
  // showAll=false: backend already returns only pass=6 items sorted by rel_strength — no re-sort.
  const items = useMemo(() => {
    if (!data) return []
    if (!showAll) return data.items
    return [...data.items].sort((a, b) => passCount(b) - passCount(a))
  }, [data, showAll])

  return (
    <div className="space-y-3">
      <div className="flex items-center gap-2 flex-wrap">
        <input className="bg-[#252525] border border-[#3d3d3d] rounded px-2 py-1 text-xs text-gray-300 w-28 focus:outline-none"
          placeholder="Symbol..." value={search}
          onChange={e => setSearch(e.target.value.toUpperCase())} />
        <label className="flex items-center gap-1 text-xs text-gray-400 cursor-pointer">
          <input type="checkbox" checked={showAll} onChange={e => setShowAll(e.target.checked)}
            className="accent-blue-600" />
          显示未通过(调试)
        </label>
        <select value={limit} onChange={e => setLimit(Number(e.target.value))}
          className="bg-[#252525] border border-[#3d3d3d] rounded px-2 py-1 text-xs text-gray-300 focus:outline-none ml-auto">
          <option value={50}>50</option>
          <option value={100}>100</option>
          <option value={200}>200</option>
        </select>
        {data && (
          <span className="text-xs text-gray-500">
            {data.passing}/{data.total} 通过 · {isFetching ? '刷新中' : '60s 刷新'}
          </span>
        )}
      </div>

      <div className="text-[11px] text-gray-500 px-1">
        阈值: close &gt; EMA20 &gt; EMA50 · close &gt; High20 · Vol &gt; 1.5× · RSI &gt; 55 · ADX &gt; 20 · 4h &gt; BTC 4h
        <span className="ml-2">·</span>
        <span className="ml-2"><span className={ok}>绿</span> = 满足</span>
        <span className="ml-2"><span className={bad}>红</span> = 未满足</span>
      </div>

      <div className="bg-[#1f1f1f] border border-[#2d2d2d] rounded-lg overflow-x-auto">
        {isLoading && <div className="p-8 text-gray-500 text-sm text-center">加载中...</div>}
        {data && data.items.length === 0 && !isLoading && (
          <div className="p-8 text-gray-500 text-sm text-center">
            {showAll ? '暂无评估数据(扫描尚未运行)' : '当前 0 个 symbol 通过 6 条件 — 全市场偏弱'}
          </div>
        )}
        {data && data.items.length > 0 && (
          <table className="w-full">
            <thead className="border-b border-[#2d2d2d]">
              <tr className="text-xs text-gray-500">
                <th className="text-left  py-2 px-2 font-medium">Symbol</th>
                <th className="text-right py-2 px-2 font-medium" title="最新已收盘 1h close">价格</th>
                <th className="text-right py-2 px-2 font-medium" title="条件 1a: close > EMA20">EMA20</th>
                <th className="text-right py-2 px-2 font-medium" title="条件 1b: EMA20 > EMA50">EMA50</th>
                <th className="text-right py-2 px-2 font-medium" title="条件 2: close > 前 20 根已收盘 high">High20</th>
                <th className="text-right py-2 px-2 font-medium" title="条件 3: volume / volume_ma20 > 1.5">Vol×</th>
                <th className="text-right py-2 px-2 font-medium" title="条件 4: RSI14 > 55(Wilder 平滑)">RSI14</th>
                <th className="text-right py-2 px-2 font-medium" title="条件 5: ADX14 > 20(Wilder 平滑)">ADX14</th>
                <th className="text-right py-2 px-2 font-medium" title="过去 4h 涨跌幅">4h%</th>
                <th className="text-right py-2 px-2 font-medium" title="条件 6: 4h% &gt; BTC 4h%">vs BTC</th>
              </tr>
            </thead>
            <tbody>
              {items.map((it: UptrendItem) => {
                // Split cond_ema_stack into its two sub-conditions for cell-level color:
                //   EMA20 cell green if close > EMA20
                //   EMA50 cell green if EMA20 > EMA50
                const emaStackPart1 = it.close > it.ema20
                const emaStackPart2 = it.ema20 > it.ema50
                const cnt = passCount(it)
                return (
                  <tr key={it.symbol}
                      className="group border-b border-[#252525] last:border-b-0 hover:bg-[#252525] cursor-pointer"
                      title="点击行打开 OI/Square 详情侧栏"
                      onClick={() => onSelect?.(it.symbol)}>
                    <td className="py-2 px-2 text-xs font-mono text-gray-200">
                      {it.pass
                        ? <span className="text-green-500 mr-1.5" title="6/6 全部通过">●</span>
                        : <span
                            className={`mr-1.5 text-[10px] tabular-nums font-semibold ${
                              cnt >= 5 ? 'text-yellow-400'
                              : cnt >= 3 ? 'text-orange-500'
                              : 'text-gray-600'
                            }`}
                            title={`${cnt}/6 条件通过`}
                          >{cnt}/6</span>
                      }
                      <SymbolLink symbol={it.symbol} />
                    </td>
                    <td className="py-2 px-2 text-xs text-right tabular-nums text-gray-200 font-semibold">
                      {fmtPrice(it.close)}
                    </td>
                    <td className={`py-2 px-2 text-xs text-right tabular-nums ${emaStackPart1 ? ok : bad}`}
                        title={emaStackPart1 ? `close ${fmtPrice(it.close)} > EMA20 ${fmtPrice(it.ema20)}` : `close ${fmtPrice(it.close)} ≤ EMA20 ${fmtPrice(it.ema20)} — 未达多头排列`}>
                      {fmtPrice(it.ema20)}
                    </td>
                    <td className={`py-2 px-2 text-xs text-right tabular-nums ${emaStackPart2 ? ok : bad}`}
                        title={emaStackPart2 ? `EMA20 > EMA50` : `EMA20 ${fmtPrice(it.ema20)} ≤ EMA50 ${fmtPrice(it.ema50)} — 多头未排列`}>
                      {fmtPrice(it.ema50)}
                    </td>
                    <td className={`py-2 px-2 text-xs text-right tabular-nums ${it.cond_breakout ? ok : bad}`}
                        title={it.cond_breakout ? `close > 前 20 根 high (${fmtPrice(it.highest20)})` : `close ${fmtPrice(it.close)} ≤ High20 ${fmtPrice(it.highest20)} — 未突破`}>
                      {fmtPrice(it.highest20)}
                    </td>
                    <td className={`py-2 px-2 text-xs text-right tabular-nums ${it.cond_vol_surge ? ok : bad}`}
                        title={`成交量 ${it.volume.toLocaleString('en-US', {maximumFractionDigits: 0})} / 20根MA ${it.volume_ma20.toLocaleString('en-US', {maximumFractionDigits: 0})} = ${num(it.vol_ratio, 2)}× (需 > 1.5)`}>
                      {num(it.vol_ratio, 2)}×
                    </td>
                    <td className={`py-2 px-2 text-xs text-right tabular-nums ${it.cond_rsi ? ok : bad}`}
                        title={`RSI14 ${num(it.rsi14, 2)} ${it.cond_rsi ? '> 55 ✓' : '≤ 55 ✗'}`}>
                      {num(it.rsi14, 1)}
                    </td>
                    <td className={`py-2 px-2 text-xs text-right tabular-nums ${it.cond_adx ? ok : bad}`}
                        title={`ADX14 ${num(it.adx14, 2)} ${it.cond_adx ? '> 20 ✓ (有趋势)' : '≤ 20 ✗ (震荡市)'}`}>
                      {num(it.adx14, 1)}
                    </td>
                    <td className={`py-2 px-2 text-xs text-right tabular-nums font-semibold ${it.pct_4h >= 0 ? ok : bad}`}>
                      {pct(it.pct_4h)}
                    </td>
                    <td className={`py-2 px-2 text-xs text-right tabular-nums ${it.cond_rel_strength ? ok : bad}`}
                        title={it.cond_rel_strength ? `领先 BTC ${pct(it.rel_strength)}` : `落后 BTC ${pct(it.rel_strength)} — 相对强度不足`}>
                      {pct(it.rel_strength)}
                    </td>
                  </tr>
                )
              })}
            </tbody>
            <tfoot>
              <tr className="text-[10px] text-gray-600 border-t border-[#2d2d2d]">
                <td colSpan={10} className="py-1.5 px-2">
                  💡 行点击打开 OI/Square 详情侧栏(含大户多空 / 市值占比 / 24h 趋势) · symbol 文字 → 币安永续 · 悬停数值格看完整阈值对比
                </td>
              </tr>
            </tfoot>
          </table>
        )}
      </div>
    </div>
  )
}
