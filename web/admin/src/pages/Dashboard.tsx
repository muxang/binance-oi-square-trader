import { useQuery } from '@tanstack/react-query'
import dayjs from 'dayjs'
import relativeTime from 'dayjs/plugin/relativeTime'
import 'dayjs/locale/zh-cn'
import { fetchDashboard, type CollectorStatus } from '../api/client'
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

function CollectorRow({ c }: { c: CollectorStatus }) {
  const lastTick = c.last_tick_seconds > 0
    ? dayjs.unix(c.last_tick_seconds).fromNow()
    : '未运行'
  const rate = c.success_rate_5min >= 0
    ? `${(c.success_rate_5min * 100).toFixed(0)}%`
    : '—'
  const dotColor =
    c.status === 'active' ? colors.normal
    : c.status === 'stale' ? colors.warning
    : colors.muted
  const rateColor =
    c.success_rate_5min >= 0 && c.success_rate_5min < 0.8 ? colors.warning : '#d0d0d0'

  return (
    <div className="flex items-center justify-between py-1.5 text-sm border-b border-[#252525] last:border-0">
      <div className="flex items-center gap-2">
        <span className="w-2 h-2 rounded-full shrink-0" style={{ backgroundColor: dotColor }} />
        <span className="text-gray-300 font-mono text-xs">{c.name}</span>
      </div>
      <div className="flex gap-6 text-right">
        <span className="text-gray-500 text-xs w-28 text-right">{lastTick}</span>
        <span className="text-xs w-10 text-right" style={{ color: rateColor }}>{rate}</span>
      </div>
    </div>
  )
}

export default function Dashboard() {
  const { data, isLoading, error, dataUpdatedAt } = useQuery({
    queryKey: ['dashboard'],
    queryFn: fetchDashboard,
    refetchInterval: 5_000,
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
    </div>
  )
}
