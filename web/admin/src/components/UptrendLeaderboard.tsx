// R.35: 7-day pass-count排行 + 分布直方图. 单独组件 — UptrendPanel 在
// view==='leaderboard' 时挂载. 后端 /api/admin/market/uptrend/leaderboard
// 已聚合,前端只负责渲染.
import { useQuery } from '@tanstack/react-query'
import { useMemo } from 'react'
import { fetchUptrendLeaderboard } from '../api/client'
import SymbolLink from './SymbolLink'

export default function UptrendLeaderboard({ onSelect }: { onSelect?: (sym: string) => void }) {
  const { data, isLoading, isFetching } = useQuery({
    queryKey: ['uptrend-leaderboard'],
    queryFn: () => fetchUptrendLeaderboard(100),
    refetchInterval: 60_000,
    placeholderData: (prev) => prev,
  })

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
      </div>

      {/* 分布直方图 */}
      <div className="bg-[#1f1f1f] border border-[#2d2d2d] rounded-lg p-3">
        <div className="text-[11px] text-gray-500 mb-2">
          📊 提示次数分布(按 symbol 数量,7 天窗口)
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
        {data && data.leaderboard.length === 0 && !isLoading && (
          <div className="p-8 text-gray-500 text-sm text-center">
            过去 7 天没有任何 symbol 通过(数据可能尚未积累)
          </div>
        )}
        {data && data.leaderboard.length > 0 && (
          <table className="w-full text-xs">
            <thead className="border-b border-[#2d2d2d]">
              <tr className="text-gray-500">
                <th className="text-right py-2 px-2 font-medium w-12">#</th>
                <th className="text-left  py-2 px-2 font-medium">Symbol</th>
                <th className="text-right py-2 px-2 font-medium w-20" title="7 天内通过 finalSignal 的不同小时数">7d 次数</th>
                <th className="text-left  py-2 px-2 font-medium">相对强度</th>
                <th className="text-right py-2 px-2 font-medium w-16" title="占榜首百分比">vs Top</th>
              </tr>
            </thead>
            <tbody>
              {data.leaderboard.map((it, i) => {
                const pct = topCount > 0 ? (it.count / topCount) * 100 : 0
                return (
                  <tr key={it.symbol}
                      className="group border-b border-[#252525] last:border-b-0 hover:bg-[#252525] cursor-pointer"
                      onClick={() => onSelect?.(it.symbol)}>
                    <td className="py-2 px-2 text-right tabular-nums text-gray-500 font-mono">
                      {i + 1}
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
                  💡 行点击 → OI/Square 详情侧栏 · 仅显示前 100 名 · 数据来自每 5 分钟扫描的累积 ZSET
                </td>
              </tr>
            </tfoot>
          </table>
        )}
      </div>
    </div>
  )
}
