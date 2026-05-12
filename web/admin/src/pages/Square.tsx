import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import dayjs from 'dayjs'
import { fetchSquareTrending } from '../api/client'
import { colors } from '../theme/colors'

const TH = ({ children, right }: { children: React.ReactNode; right?: boolean }) => (
  <th className={`py-2 px-3 text-xs font-medium text-gray-500 ${right ? 'text-right' : 'text-left'}`}>
    {children}
  </th>
)

export default function Square() {
  const [limit, setLimit] = useState(50)
  const [search, setSearch] = useState('')

  const { data, isLoading, error } = useQuery({
    queryKey: ['square-trending', limit],
    queryFn: () => fetchSquareTrending(limit),
    refetchInterval: 60_000,
  })

  const items = (data?.items ?? []).filter(it =>
    !search || it.symbol.includes(search.toUpperCase())
  )

  return (
    <div className="p-6 space-y-4">
      <div className="flex items-center justify-between">
        <h1 className="text-base font-semibold text-gray-200">Square 热点</h1>
        <span className="text-xs text-gray-600">{data?.total ?? 0} symbols · 60s刷新</span>
      </div>

      <div className="flex gap-2 items-center">
        <input
          className="bg-[#252525] border border-[#3d3d3d] rounded px-2 py-1 text-xs text-gray-300 w-28 focus:outline-none"
          placeholder="Symbol..."
          value={search}
          onChange={e => setSearch(e.target.value)}
        />
        <div className="flex gap-1 ml-auto">
          {[50, 100, 200].map(n => (
            <button key={n} onClick={() => setLimit(n)}
              className={`px-2 py-1 text-xs rounded ${limit === n ? 'bg-blue-700 text-white' : 'bg-[#252525] text-gray-400 hover:text-white'}`}>
              Top {n}
            </button>
          ))}
        </div>
      </div>

      {isLoading && <div className="p-8 text-gray-500 text-sm text-center">加载中...</div>}
      {error    && <div className="p-8 text-red-400 text-sm">加载失败</div>}

      {data && (
        <div className="bg-[#1f1f1f] border border-[#2d2d2d] rounded-lg overflow-hidden">
          <table className="w-full">
            <thead className="border-b border-[#2d2d2d]">
              <tr>
                <TH>#</TH>
                <TH>Symbol</TH>
                <TH right>提及数</TH>
                <TH right>24h增长</TH>
                <TH right>浏览量</TH>
                <TH>最新时间</TH>
              </tr>
            </thead>
            <tbody>
              {items.length === 0 ? (
                <tr>
                  <td colSpan={6} className="py-8 text-center text-gray-500 text-sm">暂无数据</td>
                </tr>
              ) : (
                items.map((item, idx) => {
                  const growthColor = item.growth_24h > 0
                    ? colors.up
                    : item.growth_24h < 0
                    ? colors.down
                    : '#8c8c8c'
                  return (
                    <tr key={item.symbol}
                      className="border-b border-[#252525] hover:bg-[#252525] transition-colors">
                      <td className="py-2 px-3 text-xs text-gray-600 tabular-nums w-10">{idx + 1}</td>
                      <td className="py-2 px-3 font-mono text-sm text-white font-semibold">{item.symbol}</td>
                      <td className="py-2 px-3 text-xs text-right tabular-nums text-gray-300">
                        {item.content_count.toLocaleString()}
                      </td>
                      <td className="py-2 px-3 text-xs text-right tabular-nums font-semibold"
                        style={{ color: growthColor }}>
                        {item.growth_24h > 0 ? '+' : ''}{item.growth_24h.toLocaleString()}
                      </td>
                      <td className="py-2 px-3 text-xs text-right tabular-nums text-gray-500">
                        {item.view_count > 0 ? item.view_count.toLocaleString() : '—'}
                      </td>
                      <td className="py-2 px-3 text-xs text-gray-500">
                        {item.latest_ts_ms ? dayjs(item.latest_ts_ms).format('MM-DD HH:mm') : '—'}
                      </td>
                    </tr>
                  )
                })
              )}
            </tbody>
          </table>
        </div>
      )}
    </div>
  )
}
