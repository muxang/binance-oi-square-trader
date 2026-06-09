import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { fetchMarket, type MarketItem, type MarketScope, type MarketSort, type SortOrder } from '../api/client'
import { colors, pnlColor } from '../theme/colors'
import MarkPriceModal from '../components/MarkPriceModal'
import UptrendPanel from '../components/UptrendPanel'
import SymbolLink from '../components/SymbolLink'
import SymbolSidebar from '../components/SymbolSidebar'

function pct(v: number) { return (v >= 0 ? '+' : '') + v.toFixed(2) + '%' }
function fmtOi(m: number) { return m >= 1000 ? (m / 1000).toFixed(1) + 'B' : m.toFixed(1) + 'M' }
function fmtPrice(p: number) {
  if (!p) return '—'
  if (p >= 1000) return p.toLocaleString('en-US', { maximumFractionDigits: 2 })
  if (p >= 1)    return p.toFixed(4)
  return p.toFixed(6)
}

function SortTH({
  children, right, sortKey, current, order, onSort,
}: {
  children: React.ReactNode
  right?: boolean
  sortKey?: MarketSort
  current: MarketSort
  order: SortOrder
  onSort: (k: MarketSort) => void
}) {
  const active = sortKey && current === sortKey
  // ▼ = desc (大→小, 当前默认), ▲ = asc (小→大). 点同一列翻转,点新列重置为 desc.
  const arrow = active ? (order === 'asc' ? ' ▲' : ' ▼') : ''
  return (
    <th
      onClick={sortKey ? () => onSort(sortKey) : undefined}
      className={`py-2 px-2 text-xs font-medium select-none
        ${right ? 'text-right' : 'text-left'}
        ${sortKey ? 'cursor-pointer hover:text-gray-300' : ''}
        ${active ? 'text-blue-400' : 'text-gray-500'}`}
    >
      {children}{arrow}
    </th>
  )
}


type ViewMode = 'market' | 'uptrend'  // R.23: uptrend discovery tab

export default function Market() {
  const [view,     setView]    = useState<ViewMode>('market')
  const [scope,    setScope]   = useState<MarketScope>('all')
  const [sortBy,   setSortBy]  = useState<MarketSort>('oi_1h_pct')
  const [order,    setOrder]   = useState<SortOrder>('desc')
  const [search,   setSearch]  = useState('')
  const [page,     setPage]    = useState(1)
  const [size,     setSize]    = useState(50)
  const [selected, setSelected] = useState<string | null>(null)
  const [markFor,  setMarkFor]  = useState<{ symbol: string; price: number } | null>(null)

  const { data, isLoading } = useQuery({
    queryKey: ['market', scope, sortBy, order, search, page, size],
    queryFn: () => fetchMarket({ scope, sort: sortBy, order, search: search || undefined, page, size }),
    refetchInterval: 30_000,
    placeholderData: (prev) => prev,
  })

  const totalPages = data ? Math.max(1, Math.ceil(data.total / size)) : 1

  // 点同一列 → toggle asc/desc;点新列 → 重置为 desc。
  function handleSort(k: MarketSort) {
    if (k === sortBy) {
      setOrder(prev => prev === 'desc' ? 'asc' : 'desc')
    } else {
      setSortBy(k)
      setOrder('desc')
    }
    setPage(1)
  }

  return (
    <div className="flex h-full">
      <div className="flex-1 flex flex-col min-w-0 p-6 space-y-3">
        <div className="flex items-center justify-between flex-wrap gap-2">
          <h1 className="text-base font-semibold text-gray-200">市场扫描</h1>
          <div className="flex items-center gap-2">
            <div className="flex gap-1 text-xs border border-[#3d3d3d] rounded p-0.5">
              {(['market','uptrend'] as ViewMode[]).map(v => (
                <button key={v} onClick={() => setView(v)}
                  className={`px-2 py-1 rounded ${view === v ? 'bg-blue-700 text-white' : 'text-gray-400 hover:text-white'}`}>
                  {v === 'market' ? 'OI/Square' : '🚀 上涨趋势'}
                </button>
              ))}
            </div>
            {view === 'market' && (
              <div className="flex gap-1 text-xs">
                {(['all','watchlist','positions'] as MarketScope[]).map(s => (
                  <button key={s} onClick={() => { setScope(s); setPage(1) }}
                    className={`px-2 py-1 rounded ${scope === s ? 'bg-blue-700 text-white' : 'bg-[#252525] text-gray-400 hover:text-white'}`}>
                    {s === 'all' ? '全市场' : s === 'watchlist' ? '候选池' : '持仓'}
                  </button>
                ))}
              </div>
            )}
            {view === 'market' && data && <span className="text-xs text-gray-600">{data.total} symbols · 30s刷新</span>}
          </div>
        </div>

        {view === 'uptrend' && <UptrendPanel onSelect={setSelected} />}

        {view === 'market' && (<>
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
                    <SortTH sortKey="symbol"             current={sortBy} order={order} onSort={handleSort}>Symbol</SortTH>
                    <SortTH right sortKey="cmcap_usd"     current={sortBy} order={order} onSort={handleSort}>流动市值</SortTH>
                    <SortTH right sortKey="oi_usd"        current={sortBy} order={order} onSort={handleSort}>OI (USD)</SortTH>
                    <SortTH right sortKey="oi_1h_pct"     current={sortBy} order={order} onSort={handleSort}>OI 1h%</SortTH>
                    <SortTH right sortKey="oi_24h_pct"    current={sortBy} order={order} onSort={handleSort}>OI 24h%</SortTH>
                    <SortTH right sortKey="current_price" current={sortBy} order={order} onSort={handleSort}>当前价</SortTH>
                    <SortTH right sortKey="price_24h_pct" current={sortBy} order={order} onSort={handleSort}>24h涨跌</SortTH>
                    <SortTH right sortKey="square"        current={sortBy} order={order} onSort={handleSort}>Square提及</SortTH>
                    <SortTH right sortKey="square_24h_pct" current={sortBy} order={order} onSort={handleSort}>Square 24h%</SortTH>
                    <SortTH right sortKey="acct_ls"       current={sortBy} order={order} onSort={handleSort}>账户多空</SortTH>
                    <SortTH right sortKey="pos_ls"        current={sortBy} order={order} onSort={handleSort}>持仓多空</SortTH>
                    <SortTH right sortKey="mcap_pct"      current={sortBy} order={order} onSort={handleSort}>市值占比</SortTH>
                    <SortTH current={sortBy} order={order} onSort={handleSort}>标记</SortTH>
                    <SortTH current={sortBy} order={order} onSort={handleSort}>🔔</SortTH>
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
                        <td className="py-2 px-2 font-mono text-sm text-white font-semibold">
                          <SymbolLink symbol={item.symbol} />
                        </td>
                        {/* R.11.B1+: 流动市值 = circulating_supply × current_price */}
                        <td className="py-2 px-2 text-xs text-right tabular-nums text-gray-400">
                          {item.cmcap_usd_m > 0 ? fmtOi(item.cmcap_usd_m) : '—'}
                        </td>
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
                        {/* R.11.B1: 大户多空比 - 红色 <1 (空头主导), 绿色 >1 (多头主导) */}
                        <td className="py-2 px-2 text-xs text-right tabular-nums"
                          style={{ color: item.acct_ls_ratio > 0
                            ? (item.acct_ls_ratio < 1 ? colors.down : colors.up)
                            : '#555' }}>
                          {item.acct_ls_ratio > 0 ? item.acct_ls_ratio.toFixed(2) : '—'}
                        </td>
                        <td className="py-2 px-2 text-xs text-right tabular-nums"
                          style={{ color: item.pos_ls_ratio > 0
                            ? (item.pos_ls_ratio < 1 ? colors.down : colors.up)
                            : '#555' }}>
                          {item.pos_ls_ratio > 0 ? item.pos_ls_ratio.toFixed(2) : '—'}
                        </td>
                        {/* R.11.B1: 市值占比 - 黄色警示 ≥50% (contract-monitor.js 阈值) */}
                        <td className="py-2 px-2 text-xs text-right tabular-nums"
                          style={{ color: item.mcap_ratio_pct > 0
                            ? (item.mcap_ratio_pct >= 50 ? '#f5a623' : '#8c8c8c')
                            : '#555' }}>
                          {item.mcap_ratio_pct > 0 ? item.mcap_ratio_pct.toFixed(2) + '%' : '—'}
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
                        <td className="py-2 px-2">
                          <button
                            onClick={(e) => { e.stopPropagation(); setMarkFor({ symbol: item.symbol, price: item.current_price }) }}
                            className="text-xs px-1.5 py-0.5 rounded bg-[#252525] text-gray-400 hover:text-white hover:bg-blue-800"
                            title="标记目标价">🔔</button>
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
        </>)}
      </div>

      {selected && (
        <SymbolSidebar
          symbol={selected}
          onClose={() => setSelected(null)}
          onJumpToMarket={(sym) => {
            setView('market')
            setSearch(sym)
            setPage(1)
            setSelected(null)
          }}
        />
      )}
      {markFor && <MarkPriceModal symbol={markFor.symbol} currentPrice={markFor.price} onClose={() => setMarkFor(null)} />}
    </div>
  )
}
