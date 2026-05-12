import { useQuery } from '@tanstack/react-query'
import { useNavigate } from 'react-router-dom'
import dayjs from 'dayjs'
import { fetchPositionsOpen, type OpenPosition, type RecentClosedTrade } from '../api/client'
import { colors, pnlColor, pnlPrefix } from '../theme/colors'

function formatDuration(ms: number): string {
  const totalMin = Math.floor(ms / 60000)
  const h = Math.floor(totalMin / 60)
  const m = totalMin % 60
  if (h >= 48) return `${Math.floor(h / 24)}d ${h % 24}h`
  if (h >= 1) return `${h}h ${m}m`
  return `${m}m`
}

function formatPrice(p: number): string {
  if (p >= 1000) return p.toLocaleString('en-US', { minimumFractionDigits: 2, maximumFractionDigits: 2 })
  if (p >= 1) return p.toFixed(4)
  return p.toFixed(6)
}

const TH = ({ children, right }: { children: React.ReactNode; right?: boolean }) => (
  <th className={`py-2 px-3 text-xs font-medium text-gray-500 ${right ? 'text-right' : 'text-left'}`}>
    {children}
  </th>
)

function PositionRow({ p, onClick }: { p: OpenPosition; onClick: () => void }) {
  const marginDanger = p.margin_ratio > 0.8
  return (
    <tr
      onClick={onClick}
      className="border-b border-[#252525] hover:bg-[#252525] cursor-pointer transition-colors"
    >
      <td className="py-2.5 px-3 font-mono text-sm text-white font-semibold">{p.symbol}</td>
      <td className="py-2.5 px-3 text-xs text-gray-400">{p.direction}</td>
      <td className="py-2.5 px-3 text-xs text-gray-400 tabular-nums">
        {p.entry_ts_ms ? dayjs(p.entry_ts_ms).format('MM-DD HH:mm') : '—'}
      </td>
      <td className="py-2.5 px-3 text-xs text-right tabular-nums">{formatPrice(p.entry_price)}</td>
      <td className="py-2.5 px-3 text-xs text-right tabular-nums font-medium">
        {p.current_price > 0 ? formatPrice(p.current_price) : '—'}
      </td>
      <td className="py-2.5 px-3 text-xs text-right text-gray-400">{formatDuration(p.hold_duration_ms)}</td>
      <td className="py-2.5 px-3 text-right">
        <span className="text-sm font-semibold tabular-nums" style={{ color: pnlColor(p.unrealized_pnl) }}>
          {pnlPrefix(p.unrealized_pnl)}{p.unrealized_pnl.toFixed(2)}
        </span>
        <span className="text-xs ml-1.5" style={{ color: pnlColor(p.unrealized_pnl) }}>
          {pnlPrefix(p.unrealized_pnl_pct)}{p.unrealized_pnl_pct.toFixed(1)}%
        </span>
      </td>
      <td className="py-2.5 px-3 text-right">
        <span
          className="text-xs font-mono px-1.5 py-0.5 rounded"
          style={{
            color: marginDanger ? colors.halt : '#d0d0d0',
            background: marginDanger ? colors.halt + '22' : 'transparent',
          }}
        >
          {(p.margin_ratio * 100).toFixed(1)}%
        </span>
      </td>
    </tr>
  )
}

function RecentRow({ t }: { t: RecentClosedTrade }) {
  return (
    <tr className="border-b border-[#252525]">
      <td className="py-2 px-3 font-mono text-sm text-gray-400">{t.symbol}</td>
      <td className="py-2 px-3 text-xs text-gray-500">
        {t.exit_ts_ms ? dayjs(t.exit_ts_ms).format('MM-DD HH:mm') : '—'}
      </td>
      <td className="py-2 px-3 text-xs text-right tabular-nums text-gray-500">{formatPrice(t.entry_price)}</td>
      <td className="py-2 px-3 text-xs text-right tabular-nums text-gray-500">{t.exit_price > 0 ? formatPrice(t.exit_price) : '—'}</td>
      <td className="py-2 px-3 text-right">
        <span className="text-sm tabular-nums" style={{ color: pnlColor(t.realized_pnl) }}>
          {pnlPrefix(t.realized_pnl)}{t.realized_pnl.toFixed(2)}
        </span>
      </td>
      <td className="py-2 px-3 text-xs text-gray-500">{t.exit_reason || '—'}</td>
    </tr>
  )
}

export default function Positions() {
  const navigate = useNavigate()
  const { data, isLoading, error } = useQuery({
    queryKey: ['positions-open'],
    queryFn: fetchPositionsOpen,
    refetchInterval: 5_000,
  })

  if (isLoading) return <div className="p-8 text-gray-500 text-sm">加载中...</div>
  if (error || !data) return <div className="p-8 text-red-400 text-sm">加载失败: {String(error)}</div>

  const { positions, recent } = data

  return (
    <div className="p-6 space-y-5">
      <div className="flex items-center justify-between">
        <h1 className="text-base font-semibold text-gray-200">当前持仓</h1>
        <span className="text-xs text-gray-600">{positions.length} 笔 · 5s 刷新</span>
      </div>

      {positions.length > 0 ? (
        <div className="bg-[#1f1f1f] border border-[#2d2d2d] rounded-lg overflow-hidden">
          <table className="w-full">
            <thead className="border-b border-[#2d2d2d]">
              <tr>
                <TH>Symbol</TH>
                <TH>方向</TH>
                <TH>开仓时间 (BJT)</TH>
                <TH right>开仓价</TH>
                <TH right>当前价</TH>
                <TH right>持仓时长</TH>
                <TH right>Unrealized PnL</TH>
                <TH right>Margin Ratio</TH>
              </tr>
            </thead>
            <tbody>
              {positions.map(p => (
                <PositionRow
                  key={p.trade_id}
                  p={p}
                  onClick={() => navigate(`/trade/${p.trade_id}`)}
                />
              ))}
            </tbody>
          </table>
        </div>
      ) : (
        <div className="bg-[#1f1f1f] border border-[#2d2d2d] rounded-lg p-6">
          <div className="text-gray-500 text-sm mb-4">当前无持仓</div>
          {recent.length > 0 && (
            <>
              <div className="text-xs text-gray-600 mb-2">过去 24h 最近 {recent.length} 笔</div>
              <table className="w-full">
                <thead className="border-b border-[#2d2d2d]">
                  <tr>
                    <TH>Symbol</TH>
                    <TH>平仓时间</TH>
                    <TH right>开仓价</TH>
                    <TH right>平仓价</TH>
                    <TH right>Realized PnL</TH>
                    <TH>平仓原因</TH>
                  </tr>
                </thead>
                <tbody>
                  {recent.map(t => <RecentRow key={t.trade_id} t={t} />)}
                </tbody>
              </table>
            </>
          )}
        </div>
      )}
    </div>
  )
}
