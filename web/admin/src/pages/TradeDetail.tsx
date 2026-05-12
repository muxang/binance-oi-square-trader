import { useParams, useNavigate } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import dayjs from 'dayjs'
import { fetchTradeDetail } from '../api/client'
import { pnlColor, pnlPrefix } from '../theme/colors'

function fmtP(v?: number | null, d = 4): string {
  if (v == null) return '—'
  if (v >= 1000) return v.toLocaleString('en-US', { maximumFractionDigits: 2 })
  if (v >= 1)    return v.toFixed(d)
  return v.toFixed(6)
}
function fmtTs(ms?: number | null): string {
  return ms ? dayjs(ms).format('YYYY-MM-DD HH:mm:ss') : '—'
}

const ROW = ({ label, value, mono = false }: { label: string; value: React.ReactNode; mono?: boolean }) => (
  <div className="flex justify-between py-1.5 border-b border-[#252525] last:border-0">
    <span className="text-xs text-gray-500 shrink-0 w-36">{label}</span>
    <span className={`text-xs text-right ${mono ? 'font-mono' : ''} text-gray-300`}>{value ?? '—'}</span>
  </div>
)

const Card = ({ title, children }: { title: string; children: React.ReactNode }) => (
  <div className="bg-[#1f1f1f] border border-[#2d2d2d] rounded-lg overflow-hidden">
    <div className="px-4 py-2 bg-[#252525] border-b border-[#2d2d2d]">
      <span className="text-xs font-semibold text-gray-300">{title}</span>
    </div>
    <div className="px-4 py-2">{children}</div>
  </div>
)

const STATUS_COLOR: Record<string, string> = {
  open: '#52c41a', closed: '#8c8c8c', failed: '#ff4d4f',
  entering: '#fa8c16', orphan: '#722ed1',
}

export default function TradeDetail() {
  const { id } = useParams<{ id: string }>()
  const navigate = useNavigate()

  const { data: d, isLoading, error } = useQuery({
    queryKey: ['trade-detail', id],
    queryFn: () => fetchTradeDetail(Number(id)),
    refetchInterval: 10_000,
  })

  if (isLoading) return <div className="p-8 text-gray-500 text-sm text-center">加载中...</div>
  if (error || !d)  return <div className="p-8 text-red-400 text-sm text-center">加载失败</div>

  const sc = STATUS_COLOR[d.status] ?? '#8c8c8c'

  return (
    <div className="p-6 space-y-4 max-w-3xl">
      {/* Header */}
      <div className="flex items-center gap-3 flex-wrap">
        <button onClick={() => navigate(-1)}
          className="text-xs text-gray-500 hover:text-white px-2 py-1 rounded bg-[#252525]">
          ← 返回
        </button>
        <span className="font-mono text-lg text-white font-bold">{d.symbol}</span>
        <span className="text-xs px-2 py-0.5 rounded font-mono"
          style={{ color: sc, background: sc + '22' }}>{d.status}</span>
        <span className="text-xs text-gray-600">#{d.trade_id} · {d.data_source}</span>
        <span className="text-xs text-gray-500 ml-auto">
          {d.leverage}x · margin {d.margin.toFixed(2)} USDT
        </span>
      </div>

      {/* A: Signal & Decision */}
      <Card title="A. 信号触发 & 决策">
        {d.signal ? (
          <>
            <ROW label="信号时间" value={fmtTs(d.signal.ts_ms)} />
            <ROW label="OI 暴涨" value={d.signal.oi_triggered ? '✓ 触发' : '✗ 未触发'} />
            <ROW label="Square 热点" value={d.signal.square_hot ? '✓ 热点' : '✗ 非热点'} />
            {d.signal.oi_data && Object.entries(d.signal.oi_data).map(([k, v]) => (
              <ROW key={k} label={k} value={typeof v === 'number' ? (v as number).toFixed(4) : String(v)} mono />
            ))}
            {d.signal.square_data && Object.entries(d.signal.square_data).map(([k, v]) => (
              <ROW key={k} label={k} value={String(v)} mono />
            ))}
            <ROW label="决策" value={
              <span style={{ color: d.signal.decision === 'rejected' ? '#ff4d4f' : '#52c41a' }}>
                {d.signal.decision}
              </span>
            } />
            {d.signal.rejection_reason && <ROW label="拒绝原因" value={d.signal.rejection_reason} />}
          </>
        ) : <div className="text-xs text-gray-600 py-2">无信号数据</div>}
      </Card>

      {/* B: Entry Execution */}
      <Card title="B. 开仓执行">
        <ROW label="开仓时间" value={fmtTs(d.entry_ts_ms)} />
        <ROW label="开仓价" value={fmtP(d.entry_price)} mono />
        <ROW label="Notional" value={d.notional?.toFixed(2) + ' USDT'} mono />
        {d.initial_atr         != null && <ROW label="ATR"   value={fmtP(d.initial_atr, 6)} mono />}
        {d.initial_stop_loss   != null && <ROW label="止损价" value={fmtP(d.initial_stop_loss)} mono />}
        {d.initial_take_profit_1 != null && <ROW label="TP1" value={fmtP(d.initial_take_profit_1)} mono />}
        {d.initial_take_profit_2 != null && <ROW label="TP2" value={fmtP(d.initial_take_profit_2)} mono />}
        {d.api_errors.length > 0 && (
          <div className="mt-3 pt-2 border-t border-[#2d2d2d]">
            <div className="text-xs text-red-400 mb-2">API 错误 ({d.api_errors.length})</div>
            {d.api_errors.map((e, i) => (
              <div key={i} className="py-1.5 border-b border-[#252525] last:border-0">
                <div className="flex gap-2 text-xs">
                  <span className="text-gray-500 shrink-0">{dayjs(e.ts_ms).format('HH:mm:ss')}</span>
                  <span className="text-red-300 font-mono shrink-0">{e.error_code}</span>
                  <span className="text-gray-400 font-mono truncate">{e.endpoint}</span>
                </div>
                {e.message && <div className="text-xs text-gray-500 mt-0.5 pl-0">{e.message}</div>}
              </div>
            ))}
          </div>
        )}
      </Card>

      {/* C: Position State (only when exists) */}
      {d.position && (
        <Card title="C. 持仓状态">
          <ROW label="当前数量" value={d.position.current_qty.toFixed(4)} mono />
          <ROW label="最高价" value={fmtP(d.position.highest_price)} mono />
          <ROW label="Trailing 止损" value={
            d.position.trailing_stop_active
              ? `激活 @ ${fmtP(d.position.trailing_stop_price)}`
              : '未激活'
          } />
          <ROW label="TP1 完成" value={d.position.tp_stage1_done ? '✓' : '—'} />
          <ROW label="TP2 完成" value={d.position.tp_stage2_done ? '✓' : '—'} />
          {d.position.entry_oi != null && (
            <ROW label="入场 OI" value={d.position.entry_oi.toFixed(2) + ' M'} mono />
          )}
          <ROW label="最后检查" value={fmtTs(d.position.last_check_ts_ms)} />
        </Card>
      )}

      {/* D: Close Record */}
      <Card title="D. 平仓记录">
        {d.exits.length > 0 && (
          <div className="mb-3">
            <div className="text-xs text-gray-500 mb-1">平仓事件 ({d.exits.length})</div>
            {d.exits.map((ex, i) => (
              <div key={i}
                className="flex gap-3 py-1.5 border-b border-[#252525] last:border-0 text-xs items-center">
                <span className="text-gray-500 w-16 shrink-0">{dayjs(ex.ts_ms).format('HH:mm:ss')}</span>
                <span className="text-gray-400 font-mono w-20 shrink-0">{ex.type}</span>
                <span className="text-gray-400 tabular-nums w-16 shrink-0">{ex.qty.toFixed(4)}</span>
                <span className="text-gray-400 tabular-nums w-20 shrink-0">{fmtP(ex.price)}</span>
                <span className="tabular-nums" style={{ color: pnlColor(ex.pnl) }}>
                  {pnlPrefix(ex.pnl)}{ex.pnl.toFixed(2)}
                </span>
              </div>
            ))}
          </div>
        )}
        <ROW label="平仓时间" value={fmtTs(d.exit_ts_ms)} />
        <ROW label="平仓价" value={fmtP(d.exit_price)} mono />
        <ROW label="平仓原因" value={d.exit_reason} />
        <ROW label="已实现 PnL" value={
          d.realized_pnl != null
            ? <span style={{ color: pnlColor(d.realized_pnl) }}>
                {pnlPrefix(d.realized_pnl)}{d.realized_pnl.toFixed(2)} USDT
              </span>
            : null
        } />
        {d.fees != null && <ROW label="手续费" value={d.fees.toFixed(4) + ' USDT'} mono />}
      </Card>
    </div>
  )
}
