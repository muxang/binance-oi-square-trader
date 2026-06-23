// R.35: 7-day pass-count排行 + 分布直方图. 单独组件 — UptrendPanel 在
// view==='leaderboard' 时挂载. 后端 /api/admin/market/uptrend/leaderboard
// 已聚合 + 可选 ?exclude_stocks=1 服务端过滤(保证直方图与列表一致).
// R.35a: 7d 次数列点击切换升/降序; 加 📈 股票开关.
import { useQuery } from '@tanstack/react-query'
import { useMemo, useState } from 'react'
import { fetchUptrendLeaderboard, fetchStockSymbols } from '../api/client'
import SymbolLink from './SymbolLink'

type SortDir = 'desc' | 'asc'

export default function UptrendLeaderboard({ onSelect }: { onSelect?: (sym: string) => void }) {
  const [sortDir, setSortDir] = useState<SortDir>('desc')
  // R.35a: 与主面板一致,默认显示股票(去掉 toggle 时回到不过滤态).
  const [showStocks, setShowStocks] = useState(true)

  // 服务端过滤 — 确保 histogram 与列表反映同一数据切片. excludeStocks=true 时
  // 后端读 admin:stock:symbols:v1 (R.31) 过滤再返回.
  const { data, isLoading, isFetching } = useQuery({
    queryKey: ['uptrend-leaderboard', showStocks],
    queryFn: () => fetchUptrendLeaderboard(100, !showStocks),
    refetchInterval: 60_000,
    placeholderData: (prev) => prev,
  })

  // R.31 stock SET — 仅用来显示总股票数提示(实际过滤在后端).
  const { data: stockData } = useQuery({
    queryKey: ['stock-symbols'],
    queryFn: fetchStockSymbols,
    staleTime: 30 * 60 * 1000,
    refetchInterval: 30 * 60 * 1000,
  })
  const stockTotal = stockData?.symbols.length ?? 0

  // 排序方向:后端默认 desc; asc 时前端反转(数据已在内存).
  const sortedLeaderboard = useMemo(() => {
    if (!data?.leaderboard) return []
    if (sortDir === 'desc') return data.leaderboard
    // ASC: 反转后端的 desc 顺序; 同 count 时 symbol 字母 asc 仍然成立.
    return [...data.leaderboard].sort((a, b) => {
      if (a.count !== b.count) return a.count - b.count
      return a.symbol.localeCompare(b.symbol)
    })
  }, [data, sortDir])

  const topCount = useMemo(
    () => data?.leaderboard.reduce((m, it) => Math.max(m, it.count), 0) ?? 0,
    [data],
  )
  const maxBucket = useMemo(
    () => data?.histogram.reduce((m, b) => Math.max(m, b.count), 0) ?? 0,
    [data],
  )

  return (
    <div className="space-y-3">
      <div className="flex items-center gap-2 flex-wrap">
        <span className="text-xs text-gray-400">
          过去 7 天通过 finalSignal 的小时数排行 ·
          {data
            ? ` ${data.total_symbols} 个 symbol / ${data.total_passes} 次通过 · ${isFetching ? '刷新中' : '60s 刷新'}`
            : isLoading ? ' 加载中…' : ' —'}
        </span>
        <label className="flex items-center gap-1 text-xs text-gray-400 cursor-pointer select-none ml-auto"
               title={`Binance Futures 股票合约 (underlyingType=EQUITY), 共 ${stockTotal} 个 · 取消勾选后直方图也会重算`}>
          <input type="checkbox" checked={showStocks} onChange={e => setShowStocks(e.target.checked)}
            className="accent-blue-600" />
          📈 股票{stockTotal > 0 ? ` (${stockTotal})` : ''}
        </label>
      </div>

      {/* 分布直方图 */}
      <div className="bg-[#1f1f1f] border border-[#2d2d2d] rounded-lg p-3">
        <div className="text-[11px] text-gray-500 mb-2">
          📊 提示次数分布(按 symbol 数量,7 天窗口{showStocks ? '' : ',已排除股票'})
        </div>
        {!data || data.histogram.length === 0
          ? <div className="text-gray-600 text-xs">无数据</div>
          : (
            <div className="space-y-1.5">
              {data.histogram.map(b => {
                const pct = maxBucket > 0 ? (b.count / maxBucket) * 100 : 0
                return (
                  <div key={b.label} className="flex items-center gap-2 text-xs">
                    <div className="w-16 text-right text-gray-400 tabular-nums">
                      {b.label} 次
                    </div>
                    <div className="flex-1 h-5 bg-[#252525] rounded overflow-hidden relative">
                      <div
                        className={`h-full ${
                          b.min <= 1 ? 'bg-gray-600'
                          : b.min <= 3 ? 'bg-blue-700/60'
                          : b.min <= 7 ? 'bg-cyan-700/60'
                          : b.min <= 14 ? 'bg-green-700/60'
                          : b.min <= 30 ? 'bg-yellow-700/60'
                          : 'bg-purple-700/60'
                        }`}
                        style={{ width: `${pct}%` }}
                      />
                      {b.count > 0 && (
                        <span className="absolute inset-0 flex items-center px-2 text-[11px] tabular-nums text-gray-200">
                          {b.count}
                        </span>
                      )}
                    </div>
                    <div className="w-12 text-right text-gray-500 tabular-nums text-[10px]">
                      {data.total_symbols > 0 ? Math.round((b.count / data.total_symbols) * 100) + '%' : '—'}
                    </div>
                  </div>
                )
              })}
            </div>
          )}
      </div>

      {/* 排行榜 */}
      <div className="bg-[#1f1f1f] border border-[#2d2d2d] rounded-lg overflow-x-auto">
        {isLoading && <div className="p-8 text-gray-500 text-sm text-center">加载中…</div>}
        {data && sortedLeaderboard.length === 0 && !isLoading && (
          <div className="p-8 text-gray-500 text-sm text-center">
            过去 7 天没有任何 symbol 通过(数据可能尚未积累)
          </div>
        )}
        {data && sortedLeaderboard.length > 0 && (
          <table className="w-full text-xs">
            <thead className="border-b border-[#2d2d2d]">
              <tr className="text-gray-500">
                <th className="text-right py-2 px-2 font-medium w-12">#</th>
                <th className="text-left  py-2 px-2 font-medium">Symbol</th>
                <th
                  className="text-right py-2 px-2 font-medium w-24 cursor-pointer hover:text-gray-300 select-none"
                  title="点击切换升/降序"
                  onClick={() => setSortDir(d => d === 'desc' ? 'asc' : 'desc')}>
                  7d 次数 {sortDir === 'desc' ? '↓' : '↑'}
                </th>
                <th className="text-left  py-2 px-2 font-medium">相对强度</th>
                <th className="text-right py-2 px-2 font-medium w-16" title="占榜首百分比">vs Top</th>
              </tr>
            </thead>
            <tbody>
              {sortedLeaderboard.map((it, i) => {
                // 序号始终是当前排序后的位次,不是固定的"全榜第 N 名"
                const rank = sortDir === 'desc' ? i + 1 : sortedLeaderboard.length - i
                const pct = topCount > 0 ? (it.count / topCount) * 100 : 0
                return (
                  <tr key={it.symbol}
                      className="group border-b border-[#252525] last:border-b-0 hover:bg-[#252525] cursor-pointer"
                      onClick={() => onSelect?.(it.symbol)}>
                    <td className="py-2 px-2 text-right tabular-nums text-gray-500 font-mono">
                      {rank}
                    </td>
                    <td className="py-2 px-2 font-mono text-gray-200 whitespace-nowrap">
                      <SymbolLink symbol={it.symbol} />
                    </td>
                    <td className="py-2 px-2 text-right tabular-nums font-semibold text-purple-300">
                      {it.count}
                    </td>
                    <td className="py-2 px-2">
                      <div className="h-2 bg-[#252525] rounded overflow-hidden w-full max-w-[300px]">
                        <div
                          className={`h-full ${
                            it.count >= 30 ? 'bg-purple-500'
                            : it.count >= 15 ? 'bg-yellow-500'
                            : it.count >= 8 ? 'bg-green-500'
                            : it.count >= 4 ? 'bg-cyan-600'
                            : 'bg-blue-700'
                          }`}
                          style={{ width: `${pct}%` }}
                        />
                      </div>
                    </td>
                    <td className="py-2 px-2 text-right tabular-nums text-gray-500 text-[10px]">
                      {pct.toFixed(0)}%
                    </td>
                  </tr>
                )
              })}
            </tbody>
            <tfoot>
              <tr className="text-[10px] text-gray-600 border-t border-[#2d2d2d]">
                <td colSpan={5} className="py-1.5 px-2">
                  💡 行点击 → OI/Square 详情侧栏 · 7d 次数列可点击切换升/降序 · 显示前 100 名
                </td>
              </tr>
            </tfoot>
          </table>
        )}
      </div>
    </div>
  )
}
