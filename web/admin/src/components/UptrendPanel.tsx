import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { fetchUptrend, type UptrendItem } from '../api/client'

function pct(v: number) { return (v >= 0 ? '+' : '') + (v * 100).toFixed(2) + '%' }
function num(v: number, d = 2) { return v.toFixed(d) }
function fmtPrice(p: number) {
  if (!p) return '—'
  if (p >= 1000) return p.toLocaleString('en-US', { maximumFractionDigits: 2 })
  if (p >= 1)    return p.toFixed(4)
  return p.toFixed(6)
}

function Cond({ ok }: { ok: boolean }) {
  return (
    <span className={ok ? 'text-green-400' : 'text-gray-600'}>
      {ok ? '✓' : '·'}
    </span>
  )
}

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
                <th className="text-right py-2 px-2 font-medium">价格</th>
                <th className="text-right py-2 px-2 font-medium" title="4h 涨跌幅 (1h close vs 4h ago)">4h%</th>
                <th className="text-right py-2 px-2 font-medium" title="4h 跑赢 BTC 的差值">vs BTC</th>
                <th className="text-right py-2 px-2 font-medium" title="量能比 = volume / volume_ma20">Vol×</th>
                <th className="text-right py-2 px-2 font-medium">RSI14</th>
                <th className="text-right py-2 px-2 font-medium">ADX14</th>
                <th className="text-center py-2 px-2 font-medium" title="EMA多头排列 / 突破 / 放量 / RSI / ADX / 相对强度">6条件</th>
              </tr>
            </thead>
            <tbody>
              {data.items.map((it: UptrendItem) => (
                <tr key={it.symbol}
                    className="border-b border-[#252525] last:border-b-0 hover:bg-[#252525] cursor-pointer"
                    onClick={() => onSelect?.(it.symbol)}>
                  <td className="py-2 px-2 text-xs font-mono text-gray-200">
                    {it.pass && <span className="text-green-500 mr-1">●</span>}
                    {it.symbol}
                  </td>
                  <td className="py-2 px-2 text-xs text-right tabular-nums text-gray-300">{fmtPrice(it.close)}</td>
                  <td className={`py-2 px-2 text-xs text-right tabular-nums font-semibold ${it.pct_4h >= 0 ? 'text-green-400' : 'text-red-400'}`}>
                    {pct(it.pct_4h)}
                  </td>
                  <td className={`py-2 px-2 text-xs text-right tabular-nums ${it.rel_strength >= 0 ? 'text-green-400' : 'text-red-400'}`}>
                    {pct(it.rel_strength)}
                  </td>
                  <td className={`py-2 px-2 text-xs text-right tabular-nums ${it.vol_ratio >= 1.5 ? 'text-green-400' : 'text-gray-400'}`}>
                    {num(it.vol_ratio, 2)}×
                  </td>
                  <td className={`py-2 px-2 text-xs text-right tabular-nums ${it.rsi14 > 55 ? 'text-green-400' : 'text-gray-400'}`}>
                    {num(it.rsi14, 1)}
                  </td>
                  <td className={`py-2 px-2 text-xs text-right tabular-nums ${it.adx14 > 20 ? 'text-green-400' : 'text-gray-400'}`}>
                    {num(it.adx14, 1)}
                  </td>
                  <td className="py-2 px-2 text-xs text-center tabular-nums tracking-wider"
                      title={`EMA叠加 ${it.cond_ema_stack?'✓':'·'} | 突破 ${it.cond_breakout?'✓':'·'} | 放量 ${it.cond_vol_surge?'✓':'·'} | RSI>55 ${it.cond_rsi?'✓':'·'} | ADX>20 ${it.cond_adx?'✓':'·'} | 强于BTC ${it.cond_rel_strength?'✓':'·'}`}>
                    <Cond ok={it.cond_ema_stack} />
                    <Cond ok={it.cond_breakout} />
                    <Cond ok={it.cond_vol_surge} />
                    <Cond ok={it.cond_rsi} />
                    <Cond ok={it.cond_adx} />
                    <Cond ok={it.cond_rel_strength} />
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>
    </div>
  )
}
