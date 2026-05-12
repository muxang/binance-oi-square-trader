import { useState, useContext } from 'react'
import { useQuery } from '@tanstack/react-query'
import dayjs from 'dayjs'
import { fetchMarket, fetchSymbolDetail, type MarketItem, type MarketScope, type MarketSort } from '../api/client'
import { DataSourceContext } from '../context/DataSource'
import { colors, pnlColor, pnlPrefix } from '../theme/colors'
import {
  LineChart, Line, XAxis, YAxis, Tooltip, ResponsiveContainer,
} from 'recharts'

function pct(v: number) { return (v >= 0 ? '+' : '') + v.toFixed(2) + '%' }
function fmtOi(m: number) { return m >= 1000 ? (m / 1000).toFixed(1) + 'B' : m.toFixed(1) + 'M' }
function fmtPrice(p: number) {
  if (!p) return '—'
  if (p >= 1000) return p.toLocaleString('en-US', { maximumFractionDigits: 2 })
  if (p >= 1)    return p.toFixed(4)
  return p.toFixed(6)
}

function SortTH({
  children, right, sortKey, current, onSort,
}: {
  children: React.ReactNode
  right?: boolean
  sortKey?: MarketSort
  current: MarketSort
  onSort: (k: MarketSort) => void
}) {
  const active = sortKey && current === sortKey
  return (
    <th
      onClick={sortKey ? () => onSort(sortKey) : undefined}
      className={`py-2 px-2 text-xs font-medium select-none
        ${right ? 'text-right' : 'text-left'}
        ${sortKey ? 'cursor-pointer hover:text-gray-300' : ''}
        ${active ? 'text-blue-400' : 'text-gray-500'}`}
    >
      {children}{active ? ' ▲' : ''}
    </th>
  )
}

function SymbolSidebar({ symbol, onClose }: { symbol: string; onClose: () => void }) {
  const { dataSource } = useContext(DataSourceContext)
  const { data, isLoading } = useQuery({
    queryKey: ['symbol-detail', symbol, dataSource],
    queryFn: () => fetchSymbolDetail(symbol, 24, dataSource),
  })
  const ttStyle = { background: '#252525', border: '1px solid #3d3d3d', fontSize: 11 }

  return (
    <div className="w-80 bg-[#1a1a1a] border-l border-[#2d2d2d] flex flex-col shrink-0">
      <div className="flex items-center justify-between p-4 border-b border-[#2d2d2d]">
        <div>
          <div className="font-mono font-bold text-white">{symbol}</div>
          {data && (
            <div className="text-xs mt-0.5">
              <span className="text-gray-400">{fmtPrice(data.current_price)}</span>
              <span className="ml-2" style={{ color: pnlColor(data.price_24h_pct) }}>
                {pct(data.price_24h_pct)}
              </span>
            </div>
          )}
        </div>
        <button onClick={onClose} className="text-gray-500 hover:text-white text-lg px-2">✕</button>
      </div>

      {isLoading && <div className="p-4 text-gray-500 text-xs">加载中...</div>}

      {data && (
        <div className="flex-1 overflow-y-auto space-y-4 p-3">
          {data.oi_series.length > 0 && (
            <div>
              <div className="text-xs text-gray-500 mb-1">OI (24h, USD M)</div>
              <ResponsiveContainer width="100%" height={100}>
                <LineChart data={data.oi_series}>
                  <XAxis dataKey="ts_ms" hide />
                  <YAxis hide domain={['auto','auto']} />
                  <Tooltip contentStyle={ttStyle}
                    labelFormatter={(v) => dayjs(v).format('MM-DD HH:mm')}
                    formatter={(v: number) => [v.toFixed(2) + 'M', 'OI']} />
                  <Line type="monotone" dataKey="oi_usd_m" stroke="#4096ff" dot={false} strokeWidth={1.5} />
                </LineChart>
              </ResponsiveContainer>
            </div>
          )}

          {data.price_series.length > 0 && (
            <div>
              <div className="text-xs text-gray-500 mb-1">价格 (24h, 15m K)</div>
              <ResponsiveContainer width="100%" height={100}>
                <LineChart data={data.price_series}>
                  <XAxis dataKey="ts_ms" hide />
                  <YAxis hide domain={['auto','auto']} />
                  <Tooltip contentStyle={ttStyle}
                    labelFormatter={(v) => dayjs(v).format('MM-DD HH:mm')}
                    formatter={(v: number) => [fmtPrice(v), '价格']} />
                  <Line type="monotone" dataKey="close" stroke="#52c41a" dot={false} strokeWidth={1.5} />
                </LineChart>
              </ResponsiveContainer>
            </div>
          )}

          {data.square_series.length > 0 && (
            <div>
              <div className="text-xs text-gray-500 mb-1">Square 话题热度 (24h 累计新帖)</div>
              <ResponsiveContainer width="100%" height={80}>
                <LineChart data={data.square_series}>
                  <XAxis dataKey="ts_ms" hide />
                  <YAxis hide domain={[0, 'auto']} />
                  <Tooltip contentStyle={ttStyle}
                    labelFormatter={(v) => dayjs(v).format('MM-DD HH:mm')}
                    formatter={(v: number) => [v.toLocaleString(), '累计新帖']} />
                  <Line type="monotone" dataKey="mentions" stroke="#fa8c16" dot={false} strokeWidth={1.5} />
                </LineChart>
              </ResponsiveContainer>
            </div>
          )}

          {data.square_posts.length > 0 && (
            <div>
              <div className="text-xs text-gray-500 mb-1">Square 帖子 (24h, {data.square_posts.length})</div>
              <div className="space-y-1.5">
                {data.square_posts.slice(0, 5).map((p, i) => (
                  <div key={i} className="bg-[#252525] rounded p-2 text-xs">
                    <div className="text-gray-400 truncate">{p.title || p.content?.slice(0, 60) || '—'}</div>
                    <div className="text-gray-600 mt-0.5">{dayjs(p.ts_ms).format('HH:mm')} · 👁 {p.views}</div>
                  </div>
                ))}
              </div>
            </div>
          )}

          {data.trades.length > 0 && (
            <div>
              <div className="text-xs text-gray-500 mb-1">历史交易 ({data.trades.length})</div>
              {data.trades.map(t => (
                <div key={t.trade_id} className="flex justify-between text-xs py-1 border-b border-[#252525]">
                  <span className="text-gray-500">{t.exit_ts_ms ? dayjs(t.exit_ts_ms).format('MM-DD') : '开仓中'}</span>
                  <span className="font-mono text-gray-400">{t.exit_reason || t.status}</span>
                  <span style={{ color: pnlColor(t.realized_pnl) }}>
                    {pnlPrefix(t.realized_pnl)}{t.realized_pnl.toFixed(2)}
                  </span>
                </div>
              ))}
            </div>
          )}

          {data.trades.length === 0 && data.square_posts.length === 0 && data.oi_series.length === 0 && (
            <div className="text-gray-600 text-xs">暂无数据</div>
          )}
        </div>
      )}
    </div>
  )
}

export default function Market() {
  const [scope,    setScope]   = useState<MarketScope>('all')
  const [sortBy,   setSortBy]  = useState<MarketSort>('oi_1h_pct')
  const [search,   setSearch]  = useState('')
  const [page,     setPage]    = useState(1)
  const [size,     setSize]    = useState(50)
  const [selected, setSelected] = useState<string | null>(null)

  const { data, isLoading } = useQuery({
    queryKey: ['market', scope, sortBy, search, page, size],
    queryFn: () => fetchMarket({ scope, sort: sortBy, search: search || undefined, page, size }),
    refetchInterval: 30_000,
    placeholderData: (prev) => prev,
  })

  const totalPages = data ? Math.max(1, Math.ceil(data.total / size)) : 1

  function handleSort(k: MarketSort) {
    setSortBy(k)
    setPage(1)
  }

  return (
    <div className="flex h-full">
      <div className="flex-1 flex flex-col min-w-0 p-6 space-y-3">
        <div className="flex items-center justify-between flex-wrap gap-2">
          <h1 className="text-base font-semibold text-gray-200">市场扫描</h1>
          <div className="flex items-center gap-2">
            <div className="flex gap-1 text-xs">
              {(['all','watchlist','positions'] as MarketScope[]).map(s => (
                <button key={s} onClick={() => { setScope(s); setPage(1) }}
                  className={`px-2 py-1 rounded ${scope === s ? 'bg-blue-700 text-white' : 'bg-[#252525] text-gray-400 hover:text-white'}`}>
                  {s === 'all' ? '全市场' : s === 'watchlist' ? '候选池' : '持仓'}
                </button>
              ))}
            </div>
            {data && <span className="text-xs text-gray-600">{data.total} symbols · 30s刷新</span>}
          </div>
        </div>

        <div className="flex gap-2 flex-wrap items-center">
          <input className="bg-[#252525] border border-[#3d3d3d] rounded px-2 py-1 text-xs text-gray-300 w-28 focus:outline-none"
            placeholder="Symbol..." value={search}
            onChange={e => { setSearch(e.target.value.toUpperCase()); setPage(1) }} />
          <select value={size} onChange={e => { setSize(Number(e.target.value)); setPage(1) }}
            className="bg-[#252525] border border-[#3d3d3d] rounded px-2 py-1 text-xs text-gray-300 focus:outline-none ml-auto">
            <option value={50}>50/页</option>
            <option value={100}>100/页</option>
          </select>
        </div>

        <div className="bg-[#1f1f1f] border border-[#2d2d2d] rounded-lg overflow-hidden flex-1">
          {isLoading && <div className="p-8 text-gray-500 text-sm text-center">加载中...</div>}
          {data && (
            <>
              <table className="w-full">
                <thead className="border-b border-[#2d2d2d]">
                  <tr>
                    <SortTH current={sortBy} onSort={handleSort}>Symbol</SortTH>
                    <SortTH right sortKey="oi_usd"        current={sortBy} onSort={handleSort}>OI (USD)</SortTH>
                    <SortTH right sortKey="oi_1h_pct"     current={sortBy} onSort={handleSort}>OI 1h%</SortTH>
                    <SortTH right sortKey="oi_24h_pct"    current={sortBy} onSort={handleSort}>OI 24h%</SortTH>
                    <SortTH right current={sortBy} onSort={handleSort}>当前价</SortTH>
                    <SortTH right sortKey="price_24h_pct" current={sortBy} onSort={handleSort}>24h涨跌</SortTH>
                    <SortTH right sortKey="square"        current={sortBy} onSort={handleSort}>Square提及</SortTH>
                    <SortTH right current={sortBy} onSort={handleSort}>Square 24h%</SortTH>
                    <SortTH current={sortBy} onSort={handleSort}>标记</SortTH>
                  </tr>
                </thead>
                <tbody>
                  {data.items.map((item: MarketItem) => {
                    const sqGrowthColor = item.square_24h_pct > 0 ? colors.up
                      : item.square_24h_pct < 0 ? colors.down : '#8c8c8c'
                    return (
                      <tr key={item.symbol}
                        onClick={() => setSelected(selected === item.symbol ? null : item.symbol)}
                        className={`border-b border-[#252525] cursor-pointer transition-colors ${selected === item.symbol ? 'bg-[#1e2a3a]' : 'hover:bg-[#252525]'}`}>
                        <td className="py-2 px-2 font-mono text-sm text-white font-semibold">{item.symbol}</td>
                        <td className="py-2 px-2 text-xs text-right tabular-nums text-gray-400">{fmtOi(item.oi_usd_m)}</td>
                        <td className="py-2 px-2 text-xs text-right tabular-nums font-semibold"
                          style={{ color: item.oi_1h_pct > 0 ? colors.up : item.oi_1h_pct < 0 ? colors.down : '#8c8c8c' }}>
                          {pct(item.oi_1h_pct)}
                        </td>
                        <td className="py-2 px-2 text-xs text-right tabular-nums"
                          style={{ color: pnlColor(item.oi_24h_pct) }}>
                          {pct(item.oi_24h_pct)}
                        </td>
                        <td className="py-2 px-2 text-xs text-right tabular-nums text-gray-400">
                          {fmtPrice(item.current_price)}
                        </td>
                        <td className="py-2 px-2 text-xs text-right tabular-nums"
                          style={{ color: item.price_24h_pct !== 0 ? pnlColor(item.price_24h_pct) : '#8c8c8c' }}>
                          {item.price_24h_pct !== 0 ? pct(item.price_24h_pct) : '—'}
                        </td>
                        <td className="py-2 px-2 text-xs text-right tabular-nums text-gray-500">
                          {item.square_mentions > 0 ? item.square_mentions.toLocaleString() : '—'}
                        </td>
                        <td className="py-2 px-2 text-xs text-right tabular-nums font-semibold"
                          style={{ color: item.square_mentions > 0 ? sqGrowthColor : '#555' }}>
                          {item.square_mentions > 0 && item.square_24h_pct !== 0
                            ? pct(item.square_24h_pct)
                            : item.square_mentions > 0 ? '新' : '—'}
                        </td>
                        <td className="py-2 px-2">
                          {item.in_open_position && (
                            <span className="text-xs px-1.5 py-0.5 rounded mr-1"
                              style={{ color: colors.up, background: colors.up + '22' }}>持仓</span>
                          )}
                          {item.in_watchlist && (
                            <span className="text-xs px-1.5 py-0.5 rounded"
                              style={{ color: colors.normal, background: colors.normal + '22' }}>候选</span>
                          )}
                        </td>
                      </tr>
                    )
                  })}
                </tbody>
              </table>
              <div className="flex items-center justify-between px-4 py-2 border-t border-[#2d2d2d]">
                <span className="text-xs text-gray-600">第 {page} / {totalPages} 页</span>
                <div className="flex gap-1">
                  <button onClick={() => setPage(p => Math.max(1, p - 1))} disabled={page <= 1}
                    className="px-2 py-1 text-xs rounded bg-[#252525] text-gray-400 disabled:opacity-30 hover:text-white">
                    ‹ 上页
                  </button>
                  <button onClick={() => setPage(p => Math.min(totalPages, p + 1))} disabled={page >= totalPages}
                    className="px-2 py-1 text-xs rounded bg-[#252525] text-gray-400 disabled:opacity-30 hover:text-white">
                    下页 ›
                  </button>
                </div>
              </div>
            </>
          )}
        </div>
      </div>

      {selected && <SymbolSidebar symbol={selected} onClose={() => setSelected(null)} />}
    </div>
  )
}
