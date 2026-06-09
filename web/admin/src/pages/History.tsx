import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { useNavigate } from 'react-router-dom'
import dayjs from 'dayjs'
import {
  fetchPositionsHistory,
  type HistoryItem,
  type HistoryParams,
} from '../api/client'
import { pnlColor, pnlPrefix } from '../theme/colors'
import SymbolLink from '../components/SymbolLink'

// ---- helpers ----

function formatDuration(ms: number): string {
  const m = Math.floor(ms / 60000)
  const h = Math.floor(m / 60)
  if (h >= 48) return `${Math.floor(h / 24)}d ${h % 24}h`
  if (h >= 1)  return `${h}h ${m % 60}m`
  return `${m}m`
}

function formatPrice(p: number): string {
  if (p >= 1000) return p.toLocaleString('en-US', { minimumFractionDigits: 2, maximumFractionDigits: 2 })
  if (p >= 1)    return p.toFixed(4)
  return p.toFixed(6)
}

const EXIT_COLORS: Record<string, string> = {
  disaster:       '#ff4d4f',
  margin_call:    '#ff4d4f',
  hard_timeout:   '#fa541c',
  soft_timeout:   '#fa8c16',
  tp_stage1:      '#30bf78',
  tp_stage2:      '#30bf78',
  trailing:       '#36cfc9',
  manual:         '#8c8c8c',
}

function ExitChip({ reason }: { reason: string }) {
  const color = EXIT_COLORS[reason] ?? '#8c8c8c'
  return (
    <span
      className="text-xs px-1.5 py-0.5 rounded font-mono"
      style={{ color, background: color + '22', border: `1px solid ${color}44` }}
    >
      {reason || '—'}
    </span>
  )
}

const TH = ({ children, right }: { children: React.ReactNode; right?: boolean }) => (
  <th className={`py-2 px-2 text-xs font-medium text-gray-500 ${right ? 'text-right' : 'text-left'}`}>
    {children}
  </th>
)

function HistoryRow({ item, onClick }: { item: HistoryItem; onClick: () => void }) {
  return (
    <tr
      onClick={onClick}
      className="border-b border-[#252525] hover:bg-[#252525] cursor-pointer transition-colors"
    >
      <td className="py-2 px-2 font-mono text-sm text-white font-semibold">
        <SymbolLink symbol={item.symbol} />
      </td>
      <td className="py-2 px-2 text-xs text-gray-400">{item.direction}</td>
      <td className="py-2 px-2 text-xs text-gray-500 tabular-nums">
        {item.entry_ts_ms ? dayjs(item.entry_ts_ms).format('MM-DD HH:mm') : '—'}
      </td>
      <td className="py-2 px-2 text-xs text-gray-400 tabular-nums">
        {item.exit_ts_ms ? dayjs(item.exit_ts_ms).format('MM-DD HH:mm') : '—'}
      </td>
      <td className="py-2 px-2 text-xs text-right tabular-nums text-gray-500">
        {item.hold_duration_ms > 0 ? formatDuration(item.hold_duration_ms) : '—'}
      </td>
      <td className="py-2 px-2 text-xs text-right tabular-nums text-gray-400">{formatPrice(item.entry_price)}</td>
      <td className="py-2 px-2 text-xs text-right tabular-nums text-gray-400">
        {item.exit_price > 0 ? formatPrice(item.exit_price) : '—'}
      </td>
      <td className="py-2 px-2 text-xs text-right tabular-nums text-gray-500">
        {item.qty > 0 ? item.qty.toFixed(4) : '—'}
      </td>
      <td className="py-2 px-2 text-right">
        <span className="text-sm tabular-nums font-semibold" style={{ color: pnlColor(item.realized_pnl) }}>
          {pnlPrefix(item.realized_pnl)}{item.realized_pnl.toFixed(2)}
        </span>
      </td>
      <td className="py-2 px-2"><ExitChip reason={item.exit_reason} /></td>
      <td className="py-2 px-2 text-xs text-right tabular-nums text-gray-600">
        {item.fees > 0 ? item.fees.toFixed(3) : '—'}
      </td>
    </tr>
  )
}

// ---- Filter bar ----

const EXIT_REASONS = ['', 'tp_stage1', 'tp_stage2', 'trailing', 'disaster', 'soft_timeout', 'hard_timeout', 'margin_call', 'manual']
const RANGE_OPTS: { label: string; since: number | undefined }[] = [
  { label: '今日',   since: dayjs().startOf('day').valueOf() },
  { label: '7天',    since: dayjs().subtract(7, 'day').valueOf() },
  { label: '30天',   since: dayjs().subtract(30, 'day').valueOf() },
  { label: '全部',   since: undefined },
]

export default function History() {
  const navigate  = useNavigate()
  const [symbol,     setSymbol]     = useState('')
  const [reason,     setReason]     = useState('')
  const [rangeIdx,   setRangeIdx]   = useState(3)   // default: 全部
  const [pnlDir,     setPnlDir]     = useState<'' | 'profit' | 'loss'>('')
  const [pageSize,   setPageSize]   = useState(20)
  const [page,       setPage]       = useState(1)

  const params: HistoryParams = {
    symbol:      symbol || undefined,
    exit_reason: reason || undefined,
    since:       RANGE_OPTS[rangeIdx].since,
    pnl_dir:     pnlDir || undefined,
    page,
    page_size:   pageSize,
  }

  const { data, isLoading, error } = useQuery({
    queryKey: ['history', params],
    queryFn: () => fetchPositionsHistory(params),
    placeholderData: (prev) => prev,
  })

  const totalPages = data ? Math.max(1, Math.ceil(data.total / pageSize)) : 1
  const resetPage  = () => setPage(1)

  return (
    <div className="p-6 space-y-4">
      <div className="flex items-center justify-between">
        <h1 className="text-base font-semibold text-gray-200">历史仓位</h1>
        {data && <span className="text-xs text-gray-600">共 {data.total} 笔</span>}
      </div>

      {/* Filter bar */}
      <div className="flex flex-wrap gap-2 items-center">
        <input
          className="bg-[#252525] border border-[#3d3d3d] rounded px-2 py-1 text-xs text-gray-300 w-28 focus:outline-none"
          placeholder="Symbol..."
          value={symbol}
          onChange={e => { setSymbol(e.target.value.toUpperCase()); resetPage() }}
        />
        <select
          className="bg-[#252525] border border-[#3d3d3d] rounded px-2 py-1 text-xs text-gray-300 focus:outline-none"
          value={reason}
          onChange={e => { setReason(e.target.value); resetPage() }}
        >
          {EXIT_REASONS.map(r => (
            <option key={r} value={r}>{r || '全部原因'}</option>
          ))}
        </select>
        <div className="flex gap-1">
          {RANGE_OPTS.map((o, i) => (
            <button
              key={i}
              onClick={() => { setRangeIdx(i); resetPage() }}
              className={`px-2 py-1 text-xs rounded ${rangeIdx === i ? 'bg-blue-700 text-white' : 'bg-[#252525] text-gray-400 hover:text-white'}`}
            >
              {o.label}
            </button>
          ))}
        </div>
        <div className="flex gap-1">
          {(['', 'profit', 'loss'] as const).map(d => (
            <button
              key={d}
              onClick={() => { setPnlDir(d); resetPage() }}
              className={`px-2 py-1 text-xs rounded ${pnlDir === d ? 'bg-blue-700 text-white' : 'bg-[#252525] text-gray-400 hover:text-white'}`}
            >
              {d === '' ? '全部' : d === 'profit' ? '盈利' : '亏损'}
            </button>
          ))}
        </div>
        <select
          className="bg-[#252525] border border-[#3d3d3d] rounded px-2 py-1 text-xs text-gray-300 focus:outline-none ml-auto"
          value={pageSize}
          onChange={e => { setPageSize(Number(e.target.value)); resetPage() }}
        >
          <option value={20}>20/页</option>
          <option value={50}>50/页</option>
        </select>
      </div>

      {isLoading && <div className="text-gray-500 text-sm py-8 text-center">加载中...</div>}
      {error    && <div className="text-red-400 text-sm py-8 text-center">加载失败: {String(error)}</div>}

      {data && (
        <div className="bg-[#1f1f1f] border border-[#2d2d2d] rounded-lg overflow-hidden">
          <table className="w-full">
            <thead className="border-b border-[#2d2d2d]">
              <tr>
                <TH>Symbol</TH>
                <TH>方向</TH>
                <TH>开仓时间</TH>
                <TH>平仓时间</TH>
                <TH right>持仓时长</TH>
                <TH right>开仓价</TH>
                <TH right>平仓价</TH>
                <TH right>数量</TH>
                <TH right>Realized PnL</TH>
                <TH>平仓原因</TH>
                <TH right>Fees</TH>
              </tr>
            </thead>
            <tbody>
              {data.items.length === 0 ? (
                <tr><td colSpan={11} className="py-8 text-center text-gray-500 text-sm">暂无数据</td></tr>
              ) : (
                data.items.map(item => (
                  <HistoryRow
                    key={item.trade_id}
                    item={item}
                    onClick={() => navigate(`/trade/${item.trade_id}`)}
                  />
                ))
              )}
            </tbody>
          </table>

          {/* Pagination */}
          <div className="flex items-center justify-between px-4 py-2 border-t border-[#2d2d2d]">
            <span className="text-xs text-gray-600">
              第 {page} / {totalPages} 页
            </span>
            <div className="flex gap-1">
              <button
                onClick={() => setPage(p => Math.max(1, p - 1))}
                disabled={page <= 1}
                className="px-2 py-1 text-xs rounded bg-[#252525] text-gray-400 disabled:opacity-30 hover:text-white"
              >
                ‹ 上页
              </button>
              <button
                onClick={() => setPage(p => Math.min(totalPages, p + 1))}
                disabled={page >= totalPages}
                className="px-2 py-1 text-xs rounded bg-[#252525] text-gray-400 disabled:opacity-30 hover:text-white"
              >
                下页 ›
              </button>
            </div>
          </div>
        </div>
      )}
    </div>
  )
}
