import { useState } from 'react'
import { useParams, useNavigate } from 'react-router-dom'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import dayjs from 'dayjs'
import { fetchTradeDetail, manualCloseTrade } from '../api/client'
import { colors, pnlColor, pnlPrefix } from '../theme/colors'
import { ConfirmModal, errorMessage } from '../components/ConfirmModal'

function fmtP(v?: number | null, d = 4): string {
  if (v == null) return '—'
  if (v >= 1000) return v.toLocaleString('en-US', { maximumFractionDigits: 2 })
  if (v >= 1)    return v.toFixed(d)
  return v.toFixed(6)
}
function fmtTs(ms?: number | null): string {
  return ms ? dayjs(ms).format('YYYY-MM-DD HH:mm:ss') : '—'
}
function fmtNum(v: unknown, d = 4): string {
  if (v == null || v === '') return '—'
  const n = Number(v)
  if (isNaN(n)) return String(v)
  return n.toFixed(d)
}

// ---- label translation maps ----

const OI_LABELS: Record<string, string> = {
  // 数值
  oi_latest:            '最新 OI (USD M)',
  current_oi:           '最新 OI (USD M)',
  oi_value_usd:         '最新 OI (USD)',
  oi_1h_pct:            'OI 1h 变化%',
  oi_baseline:          '基准 OI',
  baseline:             '基准 OI (中位数)',
  baseline_median:      '基准中位数',
  recent_avg:           '近期均值',
  ratio:                '近远比 (recent/baseline)',
  near_far_ratio:       '近远比 (recent/baseline)',
  threshold:            '触发阈值',
  threshold_ratio:      '近远比阈值',
  surge_pct:            'OI 暴涨幅度',
  surge:                'OI 暴涨幅度',
  window_min:           '检测窗口 (分钟)',
  baseline_hours:       '基准窗口 (小时)',
  baseline_window_hours:'基准窗口 (小时)',
  window_minutes:       '检测窗口 (分钟)',
  lookback_periods:     '回溯期数',
  samples:              '采样点数 N',
  data_span_hours:      '数据跨度 (小时)',
  // 布尔
  triggered:            '是否触发',
}

const SQ_LABELS: Record<string, string> = {
  // 数量
  content_count:        '当前内容数',
  cur_count:            '当前内容数',
  content_count_prev:   '前期内容数',
  min_count:            '窗口最小值',
  max_count:            '窗口最大值',
  // 增量
  delta_60min:          '60min 增量 Δᵢ',
  cur_delta:            '最新 60min 增量 Δᵢ',
  max_delta:            '近期最大增量',
  delta:                '最新增量',
  recent_growth:        '近期累计增长量',
  growth_from_min:      '较窗口最小值增长量',
  // 统计
  pos_ratio:            '二阶差分正数比 pos_ratio',
  acceleration:         '加速度 (二阶差分均值)',
  ratio:                '近远比 (recent_avg / baseline_median)',
  near_far_ratio:       '近远比 (recent_avg / baseline_median)',
  baseline_median:      '基准中位数',
  recent_avg:           '近期均值',
  samples:              '采样点数 N',
  growing_periods:      '持续增长期数',
  recent_periods_count: '近期统计期数',
  data_span_hours:      '数据跨度 (小时)',
  span_hours:           '数据跨度 (小时)',
  // 阈值
  threshold_ratio:      '近远比阈值',
  threshold_pos:        '正数比阈值',
  // 判定
  hot:                  '热点判定',
  pattern:              '热度形态',
  trend_type:           '趋势类型',
  verdict:              '判定结论',
  failed_reason:        '判定失败原因',
  // 布尔标志
  price_moved_up:       '价格同步上涨',
  low_acceleration:     '加速度不足',
}

const PATTERN_ZH: Record<string, string> = {
  burst:   '爆发型 🔥 (病毒式扩散)',
  linear:  '线性型 📈 (持续讨论)',
  decay:   '衰减型 📉 (过顶反向)',
  unknown: '未知',
}

const DECISION_ZH: Record<string, string> = {
  entered_full: '全仓入场',
  entered_half: '半仓入场',
  rejected:     '拒绝入场',
  skip:         '跳过',
}

const STATUS_ZH: Record<string, string> = {
  open:     '持仓中',
  closed:   '已平仓',
  failed:   '失败',
  entering: '入场中',
  orphan:   '孤单(异常)',
  partial:  '部分平仓',
}

const EXIT_TYPE_ZH: Record<string, string> = {
  STOP_LOSS:      '止损',
  TAKE_PROFIT_1:  '止盈1',
  TAKE_PROFIT_2:  '止盈2',
  TRAILING_STOP:  '移动止损',
  ALGO_STOP:      '条件止损',
  CIRCUIT_BREAKER:'熔断',
  MANUAL:         '手动',
  FORCED:         '强平',
}

const STATUS_COLOR: Record<string, string> = {
  open: '#52c41a', closed: '#8c8c8c', failed: '#ff4d4f',
  entering: '#fa8c16', orphan: '#722ed1',
}

// ---- shared UI ----

const ROW = ({ label, value, mono = false }: { label: string; value: React.ReactNode; mono?: boolean }) => (
  <div className="flex justify-between py-1.5 border-b border-[#252525] last:border-0">
    <span className="text-xs text-gray-500 shrink-0 w-44">{label}</span>
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

// Renders oi_data or square_data JSON blob with translated labels
function DataBlock({
  data, labelMap, numDecimals = 4,
}: {
  data: Record<string, unknown>
  labelMap: Record<string, string>
  numDecimals?: number
}) {
  return (
    <>
      {Object.entries(data).map(([k, v]) => {
        const label = labelMap[k] ?? k
        let display: React.ReactNode = '—'
        if (v == null) {
          display = '—'
        } else if (typeof v === 'boolean') {
          display = v ? '是' : '否'
        } else if (typeof v === 'number') {
          display = Number.isInteger(v) ? v.toLocaleString() : v.toFixed(numDecimals)
        } else if (typeof v === 'string') {
          display = PATTERN_ZH[v] ?? v
        } else if (Array.isArray(v)) {
          display = `[${(v as number[]).map(n => typeof n === 'number' ? n.toFixed(1) : n).join(', ')}]`
        } else {
          display = String(v)
        }
        return <ROW key={k} label={label} value={display} mono />
      })}
    </>
  )
}

// Explainer for Square hot algorithm
function SquareAlgoNote({ data }: { data: Record<string, unknown> | null }) {
  if (!data) return null
  const posRatio   = data.pos_ratio   != null ? Number(data.pos_ratio).toFixed(3)   : null
  const ratio      = (data.ratio ?? data.near_far_ratio) != null
                       ? Number(data.ratio ?? data.near_far_ratio).toFixed(3) : null
  const baseMedian = data.baseline_median != null ? Number(data.baseline_median).toFixed(0) : null
  const recentAvg  = data.recent_avg  != null ? Number(data.recent_avg).toFixed(0)  : null
  const pattern    = typeof data.pattern === 'string' ? PATTERN_ZH[data.pattern] ?? data.pattern : null
  const hot        = data.hot === true || data.hot === 'true'

  if (!posRatio && !ratio) return null

  return (
    <div className="mt-2 pt-2 border-t border-[#2d2d2d] text-xs text-gray-500 space-y-1">
      <div className="text-gray-600 font-medium mb-1">算法说明 (v0.1 自适应曲率判定)</div>
      {baseMedian && recentAvg && (
        <div>近远比 = 近期均值 / 基准中位数 = {recentAvg} / {baseMedian} = <span className="text-gray-300">{ratio}</span></div>
      )}
      {posRatio && (
        <div>二阶差分正数比 Δ'' &gt; 0 占比 = <span className="text-gray-300">{posRatio}</span>（&gt;0.6 倾向爆发）</div>
      )}
      {pattern && (
        <div>形态识别结果: <span className="text-gray-300">{pattern}</span></div>
      )}
      <div className={hot ? 'text-[#52c41a]' : 'text-gray-500'}>
        → {hot ? '爆发型确认 hot=true，信号有效' : '非爆发型，hot=false'}
      </div>
      <div className="text-gray-700 leading-relaxed pt-1">
        注: Δᵢ = c[i]−c[i−4]（60min 滑窗增量）; 二阶差分 Δ''ₖ = Δ'ₖ−Δ'ₖ₋₁ 判断加速度;
        仅爆发型触发 hot=true。阈值均为 v0.1 经验值，Phase 2 forward 跑数据后校准。
      </div>
    </div>
  )
}

export default function TradeDetail() {
  const { id } = useParams<{ id: string }>()
  const navigate = useNavigate()
  const qc = useQueryClient()
  const [closeOpen, setCloseOpen] = useState(false)
  const [reason, setReason] = useState('')

  const { data: d, isLoading, error } = useQuery({
    queryKey: ['trade-detail', id],
    queryFn: () => fetchTradeDetail(Number(id)),
    refetchInterval: 10_000,
  })

  const closeMut = useMutation({
    mutationFn: (r: string) => manualCloseTrade(Number(id), r),
    onSuccess: () => {
      setCloseOpen(false); setReason('')
      qc.invalidateQueries({ queryKey: ['trade-detail', id] })
    },
  })

  if (isLoading) return <div className="p-8 text-gray-500 text-sm text-center">加载中...</div>
  if (error || !d)  return <div className="p-8 text-red-400 text-sm text-center">加载失败</div>

  const sc = STATUS_COLOR[d.status] ?? '#8c8c8c'
  const statusZh = STATUS_ZH[d.status] ?? d.status
  const decisionZh = d.signal?.decision ? (DECISION_ZH[d.signal.decision] ?? d.signal.decision) : null
  const isClosable = d.status === 'open' || d.status === 'partial'
  const unrealized = d.position && d.entry_price && d.position.current_qty
    ? (Number(d.position.highest_price ?? 0) - d.entry_price) * d.position.current_qty
    : 0

  return (
    <div className="p-6 space-y-4">
      {/* Header */}
      <div className="flex items-center gap-3 flex-wrap">
        <button onClick={() => navigate(-1)}
          className="text-xs text-gray-500 hover:text-white px-2 py-1 rounded bg-[#252525]">
          ← 返回
        </button>
        <span className="font-mono text-lg text-white font-bold">{d.symbol}</span>
        <span className="text-xs px-2 py-0.5 rounded font-mono"
          style={{ color: sc, background: sc + '22' }}>{statusZh}</span>
        <span className="text-xs text-gray-600">#{d.trade_id} · {d.data_source === 'mainnet' ? '真盘' : '测试网'}</span>
        <span className="text-xs text-gray-500 ml-auto">
          {d.leverage}倍杠杆 · 保证金 {d.margin.toFixed(2)} USDT
        </span>
        {isClosable && (
          <button
            onClick={() => setCloseOpen(true)}
            className="text-xs px-3 py-1 rounded font-medium"
            style={{ background: colors.halt + '22', color: colors.halt, border: `1px solid ${colors.halt}66` }}
            title="手工平仓 — 二次确认 (exit_manager 1min 内执行)"
          >
            🚨 手工平仓
          </button>
        )}
      </div>

      {/* Two-column grid */}
      <div className="grid grid-cols-2 gap-4 items-start">

        {/* ── 左列: A 信号 ── */}
        <Card title="A. 信号触发 & 决策">
          {d.signal ? (
            <>
              <ROW label="信号时间" value={fmtTs(d.signal.ts_ms)} />
              <ROW label="OI 暴涨触发" value={
                <span style={{ color: d.signal.oi_triggered ? '#52c41a' : '#8c8c8c' }}>
                  {d.signal.oi_triggered ? '✓ 触发' : '✗ 未触发'}
                </span>
              } />
              {d.signal.oi_data && (
                <div className="pl-2 border-l-2 border-[#3a3a3a] my-1">
                  <DataBlock data={d.signal.oi_data as Record<string, unknown>} labelMap={OI_LABELS} />
                </div>
              )}

              <ROW label="Square 热点" value={
                <span style={{ color: d.signal.square_hot ? '#fa8c16' : '#8c8c8c' }}>
                  {d.signal.square_hot ? '✓ 热点 🔥' : '✗ 非热点'}
                </span>
              } />
              {d.signal.square_data && (
                <div className="pl-2 border-l-2 border-[#3a3a3a] my-1">
                  <DataBlock data={d.signal.square_data as Record<string, unknown>} labelMap={SQ_LABELS} />
                  <SquareAlgoNote data={d.signal.square_data as Record<string, unknown>} />
                </div>
              )}

              <ROW label="入场决策" value={
                <span style={{ color: d.signal.decision === 'rejected' ? '#ff4d4f' : '#52c41a' }}>
                  {decisionZh}
                </span>
              } />
              {d.signal.rejection_reason && (
                <ROW label="拒绝原因" value={d.signal.rejection_reason} />
              )}
            </>
          ) : <div className="text-xs text-gray-600 py-2">无信号数据</div>}
        </Card>

        {/* ── 右列: B + C + D ── */}
        <div className="space-y-4">
          <Card title="B. 开仓执行">
            <ROW label="开仓时间"   value={fmtTs(d.entry_ts_ms)} />
            <ROW label="开仓价"     value={fmtP(d.entry_price)} mono />
            <ROW label="名义价值"   value={d.notional?.toFixed(2) + ' USDT'} mono />
            {d.initial_atr           != null && <ROW label="ATR (入场时)"      value={fmtP(d.initial_atr, 6)} mono />}
            {d.initial_stop_loss     != null && <ROW label="初始止损价"         value={fmtP(d.initial_stop_loss)} mono />}
            {d.initial_take_profit_1 != null && <ROW label="初始止盈1 (TP1)"   value={fmtP(d.initial_take_profit_1)} mono />}
            {d.initial_take_profit_2 != null && <ROW label="初始止盈2 (TP2)"   value={fmtP(d.initial_take_profit_2)} mono />}
            {d.api_errors.length > 0 && (
              <div className="mt-3 pt-2 border-t border-[#2d2d2d]">
                <div className="text-xs text-red-400 mb-2">API 错误 ({d.api_errors.length} 条)</div>
                {d.api_errors.map((e, i) => (
                  <div key={i} className="py-1.5 border-b border-[#252525] last:border-0">
                    <div className="flex gap-2 text-xs">
                      <span className="text-gray-500 shrink-0">{dayjs(e.ts_ms).format('HH:mm:ss')}</span>
                      <span className="text-red-300 font-mono shrink-0">
                        {e.error_code ? `错误码 ${e.error_code}` : `HTTP ${e.http_code}`}
                      </span>
                      <span className="text-gray-400 font-mono truncate">{e.endpoint}</span>
                    </div>
                    {e.message && <div className="text-xs text-gray-500 mt-0.5">{e.message}</div>}
                  </div>
                ))}
              </div>
            )}
          </Card>

          {d.position && (
            <Card title="C. 持仓状态">
              <ROW label="当前持仓数量"      value={fmtNum(d.position.current_qty, 4)} mono />
              <ROW label="历史最高价"        value={fmtP(d.position.highest_price)} mono />
              <ROW label="移动止损"          value={
                d.position.trailing_stop_active
                  ? `已激活 @ ${fmtP(d.position.trailing_stop_price)}`
                  : '未激活'
              } />
              <ROW label="止盈1 (TP1) 完成" value={d.position.tp_stage1_done ? '✓ 已完成' : '—'} />
              <ROW label="止盈2 (TP2) 完成" value={d.position.tp_stage2_done ? '✓ 已完成' : '—'} />
              {d.position.entry_oi != null && (
                <ROW label="入场时 OI" value={d.position.entry_oi.toFixed(2) + ' M USD'} mono />
              )}
              <ROW label="最后检查时间" value={fmtTs(d.position.last_check_ts_ms)} />
            </Card>
          )}

          <Card title="D. 平仓记录">
            {d.exits.length > 0 && (
              <div className="mb-3">
                <div className="text-xs text-gray-500 mb-1">平仓事件 ({d.exits.length} 笔)</div>
                {d.exits.map((ex, i) => (
                  <div key={i}
                    className="flex gap-3 py-1.5 border-b border-[#252525] last:border-0 text-xs items-center">
                    <span className="text-gray-500 w-16 shrink-0">{dayjs(ex.ts_ms).format('HH:mm:ss')}</span>
                    <span className="text-gray-400 font-mono w-20 shrink-0">
                      {EXIT_TYPE_ZH[ex.type] ?? ex.type}
                    </span>
                    <span className="text-gray-400 tabular-nums w-16 shrink-0">{ex.qty.toFixed(4)}</span>
                    <span className="text-gray-400 tabular-nums w-20 shrink-0">{fmtP(ex.price)}</span>
                    <span className="tabular-nums" style={{ color: pnlColor(ex.pnl) }}>
                      {pnlPrefix(ex.pnl)}{ex.pnl.toFixed(2)}
                    </span>
                  </div>
                ))}
              </div>
            )}
            <ROW label="平仓时间"   value={fmtTs(d.exit_ts_ms)} />
            <ROW label="平仓价"     value={fmtP(d.exit_price)} mono />
            <ROW label="平仓原因"   value={d.exit_reason ?? '—'} />
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

      </div>

      <ConfirmModal
        open={closeOpen}
        title="🚨 手工平仓"
        tone="danger"
        confirmLabel="确认平仓"
        isPending={closeMut.isPending}
        error={closeMut.isError ? errorMessage(closeMut.error) : null}
        onCancel={() => { setCloseOpen(false); setReason('') }}
        onConfirm={() => closeMut.mutate(reason)}
      >
        <div className="space-y-1 mb-3 text-xs">
          <div><span className="text-gray-500">Trade:</span> <span className="font-mono">{d.symbol} #{d.trade_id}</span></div>
          {d.entry_price != null && (
            <div><span className="text-gray-500">入场价:</span> {fmtP(d.entry_price)}</div>
          )}
          {d.position && (
            <div><span className="text-gray-500">未实现 PnL:</span> <span style={{ color: pnlColor(unrealized) }}>{pnlPrefix(unrealized)}{unrealized.toFixed(2)} USDT</span></div>
          )}
        </div>
        <div
          className="text-xs px-3 py-2 rounded mb-3"
          style={{ background: colors.halt + '15', color: colors.halt, border: `1px solid ${colors.halt}44` }}
        >
          <b>风险:</b> trade 状态置 closing + exit_reason=manual_close。exit_manager 下次 cron tick (≤1min) cancel 所有 algo + 市价 SELL 平仓。
        </div>
        <label className="block text-xs text-gray-500 mb-1">平仓原因 (必填):</label>
        <input
          type="text" value={reason} onChange={e => setReason(e.target.value)}
          placeholder="e.g. RCA: 形态破位 / 出差 / 风险事件"
          className="w-full px-3 py-1.5 text-sm rounded bg-[#0f0f0f] border border-[#3d3d3d] text-gray-200"
          disabled={closeMut.isPending}
        />
      </ConfirmModal>
    </div>
  )
}
