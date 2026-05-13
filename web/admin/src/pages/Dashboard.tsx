import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import dayjs from 'dayjs'
import relativeTime from 'dayjs/plugin/relativeTime'
import 'dayjs/locale/zh-cn'
import { fetchDashboard, resetCircuitBreaker, type CollectorStatus } from '../api/client'
import { colors, pnlColor, pnlPrefix, haltColor } from '../theme/colors'

dayjs.extend(relativeTime)
dayjs.locale('zh-cn')

function MetricCard({ label, value, sub, valueColor }: {
  label: string
  value: string | number
  sub?: string
  valueColor?: string
}) {
  return (
    <div className="bg-[#1f1f1f] border border-[#2d2d2d] rounded-lg p-4">
      <div className="text-xs text-gray-500 mb-2">{label}</div>
      <div className="text-2xl font-bold tabular-nums" style={{ color: valueColor ?? '#f0f0f0' }}>
        {value}
      </div>
      {sub && <div className="text-xs text-gray-600 mt-1">{sub}</div>}
    </div>
  )
}

const COLLECTOR_NAMES: Record<string, string> = {
  algo_polling:    '条件单监控',
  btc_regime:      'BTC行情监控',
  decision_engine: '入场决策',
  exit_manager:    '出场管理',
  klines:          'K线/ATR采集',
  oi:              '持仓量采集',
  position_manager:'持仓对账',
  position_price:  '持仓价格',
  signal_engine:   '信号扫描',
  square_feed:     'Square推文',
  square_hashtag:  'Square话题',
  watchlist:       '候选池更新',
}

function CollectorRow({ c }: { c: CollectorStatus }) {
  // last_tick_seconds gauge may be 0 if not yet implemented in trader metrics;
  // fall back to inferring status from success_rate_5min.
  const lastTick = c.last_tick_seconds > 0
    ? dayjs.unix(c.last_tick_seconds).fromNow()
    : c.success_rate_5min >= 0 ? '运行中' : '无数据'

  const rate = c.success_rate_5min >= 0
    ? `${(c.success_rate_5min * 100).toFixed(0)}%`
    : '—'
  const dotColor =
    c.success_rate_5min === 1 ? colors.normal
    : c.success_rate_5min >= 0 ? colors.warning
    : colors.muted
  const rateColor =
    c.success_rate_5min >= 0 && c.success_rate_5min < 0.8 ? colors.warning : '#d0d0d0'

  const displayName = COLLECTOR_NAMES[c.name] ?? c.name

  return (
    <div className="flex items-center justify-between py-1.5 text-sm border-b border-[#252525] last:border-0">
      <div className="flex items-center gap-2 min-w-0">
        <span className="w-2 h-2 rounded-full shrink-0" style={{ backgroundColor: dotColor }} />
        <span className="text-gray-300 text-xs">{displayName}</span>
        <span className="text-gray-600 text-xs font-mono hidden sm:inline">({c.name})</span>
      </div>
      <div className="flex gap-6 text-right shrink-0">
        <span className="text-gray-500 text-xs w-16 text-right">{lastTick}</span>
        <span className="text-xs w-10 text-right" style={{ color: rateColor }}>{rate}</span>
      </div>
    </div>
  )
}

export default function Dashboard() {
  const qc = useQueryClient()
  const [confirmOpen, setConfirmOpen] = useState(false)
  const [resetNote, setResetNote] = useState('')
  const { data, isLoading, error, dataUpdatedAt } = useQuery({
    queryKey: ['dashboard'],
    queryFn: fetchDashboard,
    refetchInterval: 5_000,
  })

  const resetMut = useMutation({
    mutationFn: (note: string) => resetCircuitBreaker(note),
    onSuccess: () => {
      setConfirmOpen(false)
      setResetNote('')
      qc.invalidateQueries({ queryKey: ['dashboard'] })
    },
  })

  if (isLoading) {
    return <div className="p-8 text-gray-500 text-sm">加载中...</div>
  }
  if (error || !data) {
    return (
      <div className="p-8 text-red-400 text-sm">
        加载失败: {error instanceof Error ? error.message : String(error)}
      </div>
    )
  }

  const lastUpdate = dataUpdatedAt ? dayjs(dataUpdatedAt).format('HH:mm:ss') : '—'

  return (
    <div className="p-6 space-y-5">
      {/* 顶栏: 状态 + 余额 + 今日PnL */}
      <div className="flex items-center gap-4 bg-[#1f1f1f] border border-[#2d2d2d] rounded-lg px-6 py-4">
        <span className="text-xl font-bold" style={{ color: haltColor(data.halt_status) }}>
          {data.halt_status === 'NORMAL' ? '🟢' : '🔴'}&nbsp;{data.halt_status}
        </span>
        {data.halt_reason && (
          <span
            className="text-xs px-2 py-0.5 rounded"
            style={{ background: colors.halt + '22', color: colors.halt, border: `1px solid ${colors.halt}44` }}
          >
            {data.halt_reason}
          </span>
        )}
        {data.halt_status === 'HALTED' && (
          <button
            onClick={() => setConfirmOpen(true)}
            className="text-xs px-3 py-1 rounded font-medium transition-colors"
            style={{
              background: colors.warning + '22',
              color: colors.warning,
              border: `1px solid ${colors.warning}66`,
            }}
            title="手动解除 halt — 二次确认"
          >
            🔓 手动解除 halt
          </button>
        )}
        <div className="ml-auto flex items-baseline gap-1">
          <span className="text-2xl font-bold tabular-nums">{data.balance_usdt.toFixed(2)}</span>
          <span className="text-sm text-gray-500">USDT</span>
        </div>
        <div className="flex items-baseline gap-1">
          <span className="text-xl font-bold tabular-nums" style={{ color: pnlColor(data.daily_pnl) }}>
            {pnlPrefix(data.daily_pnl)}{data.daily_pnl.toFixed(2)}
          </span>
          <span className="text-xs text-gray-500">今日PnL</span>
        </div>
        <span className="text-xs text-gray-700 ml-2">{lastUpdate}</span>
      </div>

      {/* 5 指标卡片 */}
      <div className="grid grid-cols-5 gap-4">
        <MetricCard
          label="账户余额 USDT"
          value={data.balance_usdt.toFixed(2)}
        />
        <MetricCard
          label="今日 PnL"
          value={`${pnlPrefix(data.daily_pnl)}${data.daily_pnl.toFixed(2)}`}
          valueColor={pnlColor(data.daily_pnl)}
        />
        <MetricCard
          label="当前持仓"
          value={data.open_positions}
          sub="笔"
        />
        <MetricCard
          label="连续亏损"
          value={data.consecutive_losses}
          sub="次"
          valueColor={data.consecutive_losses >= 3 ? colors.warning : undefined}
        />
        <MetricCard
          label="BTC 30min 跌幅"
          value={`${data.btc_30m_drop_pct.toFixed(2)}%`}
          valueColor={
            data.btc_30m_drop_pct > 3 ? colors.halt
            : data.btc_30m_drop_pct > 1.5 ? colors.warning
            : undefined
          }
        />
      </div>

      {/* Collector 状态 */}
      <div className="bg-[#1f1f1f] border border-[#2d2d2d] rounded-lg p-5">
        <div className="flex items-center justify-between mb-3">
          <h2 className="text-sm font-semibold text-gray-300">Collector 状态</h2>
          <span className="text-xs text-gray-600">{data.collectors.length} 个</span>
        </div>
        {data.collectors.length === 0 ? (
          <div className="text-gray-600 text-sm py-4 text-center">
            暂无 collector 数据（Prometheus 未连接或 trader 未启动）
          </div>
        ) : (
          <>
            <div className="flex justify-between text-xs text-gray-700 mb-1 px-4">
              <span>名称</span>
              <div className="flex gap-6">
                <span className="w-28 text-right">最后成功</span>
                <span className="w-10 text-right">5min 成功率</span>
              </div>
            </div>
            {data.collectors.map(c => <CollectorRow key={c.name} c={c} />)}
          </>
        )}
      </div>

      {/* 手动解除 halt 二次确认对话框 */}
      {confirmOpen && (
        <div
          className="fixed inset-0 z-50 flex items-center justify-center"
          style={{ background: 'rgba(0,0,0,0.6)' }}
          onClick={() => !resetMut.isPending && setConfirmOpen(false)}
        >
          <div
            className="bg-[#1f1f1f] border border-[#3d3d3d] rounded-lg p-6 max-w-md w-full mx-4"
            onClick={e => e.stopPropagation()}
          >
            <h3 className="text-lg font-bold mb-3" style={{ color: colors.warning }}>
              ⚠️ 手动解除 trader halt
            </h3>
            <div className="text-sm text-gray-300 space-y-2 mb-4">
              <div><span className="text-gray-500">当前 halt 原因:</span> <span style={{ color: colors.halt }}>{data.halt_reason ?? '(unknown)'}</span></div>
              <div><span className="text-gray-500">今日 PnL:</span> <span style={{ color: pnlColor(data.daily_pnl) }}>{pnlPrefix(data.daily_pnl)}{data.daily_pnl.toFixed(2)} USDT</span></div>
              <div><span className="text-gray-500">连续亏损:</span> <span>{data.consecutive_losses} 次</span></div>
            </div>
            <div
              className="text-xs px-3 py-2 rounded mb-4"
              style={{ background: colors.warning + '15', color: colors.warning, border: `1px solid ${colors.warning}44` }}
            >
              <b>风险提示:</b> 解除后 trader 立即恢复入场, 真实资金继续暴露。仅在你 informed 决策的前提下确认。
            </div>
            <label className="block text-xs text-gray-500 mb-1">备注 (可选, audit log):</label>
            <input
              type="text"
              value={resetNote}
              onChange={e => setResetNote(e.target.value)}
              placeholder="e.g. RCA 完成 + Round R.1 5x 部署"
              className="w-full mb-4 px-3 py-1.5 text-sm rounded bg-[#0f0f0f] border border-[#3d3d3d] text-gray-200"
              disabled={resetMut.isPending}
            />
            {resetMut.isError && (
              <div className="text-xs mb-3 px-3 py-2 rounded" style={{ background: colors.halt + '15', color: colors.halt }}>
                解除失败: {resetMut.error instanceof Error ? resetMut.error.message : String(resetMut.error)}
              </div>
            )}
            <div className="flex gap-2 justify-end">
              <button
                onClick={() => setConfirmOpen(false)}
                disabled={resetMut.isPending}
                className="px-4 py-1.5 text-sm rounded bg-[#2d2d2d] text-gray-300 hover:bg-[#3d3d3d] disabled:opacity-50"
              >
                取消
              </button>
              <button
                onClick={() => resetMut.mutate(resetNote)}
                disabled={resetMut.isPending}
                className="px-4 py-1.5 text-sm rounded font-medium disabled:opacity-50"
                style={{ background: colors.halt, color: '#fff' }}
              >
                {resetMut.isPending ? '解除中...' : '确认解除 halt'}
              </button>
            </div>
          </div>
        </div>
      )}
    </div>
  )
}
