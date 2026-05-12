import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import {
  ComposedChart, BarChart, PieChart,
  Bar, Line, Pie, Cell,
  XAxis, YAxis, CartesianGrid, Tooltip, ResponsiveContainer,
} from 'recharts'
import {
  fetchPnlCumulative, fetchPnlBySymbol, fetchPnlByExitReason, fetchPnlStats,
  type RangeKey,
} from '../api/client'
import { colors, pnlColor, pnlPrefix } from '../theme/colors'

const TABS = ['累计曲线', 'Symbol排名', '平仓原因', '胜率统计'] as const
const RANGES: { key: RangeKey; label: string }[] = [
  { key: 'today', label: '今日' },
  { key: 'week',  label: '本周' },
  { key: 'month', label: '本月' },
  { key: 'all',   label: '累计' },
]

const EXIT_PALETTE: Record<string, string> = {
  disaster: '#ff4d4f', margin_call: '#ff4d4f', hard_timeout: '#fa541c',
  soft_timeout: '#fa8c16', tp_stage1: '#30bf78', tp_stage2: '#30bf78',
  trailing: '#36cfc9', manual: '#8c8c8c',
}
const PIE_FALLBACK = '#595959'

function StatCard({ label, value, sub }: { label: string; value: string; sub?: string }) {
  return (
    <div className="bg-[#252525] rounded-lg p-4">
      <div className="text-xs text-gray-500 mb-1">{label}</div>
      <div className="text-xl font-semibold text-gray-100">{value}</div>
      {sub && <div className="text-xs text-gray-600 mt-0.5">{sub}</div>}
    </div>
  )
}

function formatHoldMs(ms: number): string {
  const m = Math.floor(ms / 60000)
  const h = Math.floor(m / 60)
  if (h >= 24) return `${Math.floor(h / 24)}d ${h % 24}h`
  if (h >= 1)  return `${h}h ${m % 60}m`
  return `${m}m`
}

export default function Pnl() {
  const [tab,   setTab]   = useState(0)
  const [range, setRange] = useState<RangeKey>('all')

  const cumQ   = useQuery({ queryKey: ['pnl-cumulative', range], queryFn: () => fetchPnlCumulative(range) })
  const symQ   = useQuery({ queryKey: ['pnl-by-symbol',  range], queryFn: () => fetchPnlBySymbol(range) })
  const exitQ  = useQuery({ queryKey: ['pnl-by-exit',    range], queryFn: () => fetchPnlByExitReason(range) })
  const statsQ = useQuery({ queryKey: ['pnl-stats',      range], queryFn: () => fetchPnlStats(range) })

  const cum   = cumQ.data   ?? []
  const sym   = symQ.data   ?? []
  const exits = exitQ.data  ?? []
  const stats = statsQ.data

  const ttStyle = { background: '#252525', border: '1px solid #3d3d3d', fontSize: 12 }

  return (
    <div className="p-6 space-y-4">
      <div className="flex items-center justify-between">
        <h1 className="text-base font-semibold text-gray-200">PnL 分析</h1>
        <div className="flex gap-1">
          {RANGES.map(({ key, label }) => (
            <button key={key} onClick={() => setRange(key)}
              className={`px-2 py-1 text-xs rounded ${range === key ? 'bg-blue-700 text-white' : 'bg-[#252525] text-gray-400 hover:text-white'}`}>
              {label}
            </button>
          ))}
        </div>
      </div>

      <div className="flex gap-0 border-b border-[#2d2d2d]">
        {TABS.map((t, i) => (
          <button key={i} onClick={() => setTab(i)}
            className={`px-4 py-2 text-sm ${tab === i ? 'text-white border-b-2 border-blue-500 -mb-px' : 'text-gray-500 hover:text-gray-300'}`}>
            {t}
          </button>
        ))}
      </div>

      {/* Tab 0 — 累计曲线 */}
      {tab === 0 && (
        <div className="bg-[#1f1f1f] border border-[#2d2d2d] rounded-lg p-4">
          <div className="text-xs text-gray-500 mb-3">日度 PnL（柱）+ 累计 PnL（线）USDT</div>
          {cum.length === 0 ? (
            <div className="h-64 flex items-center justify-center text-gray-600 text-sm">暂无已平仓数据</div>
          ) : (
            <ResponsiveContainer width="100%" height={280}>
              <ComposedChart data={cum}>
                <CartesianGrid strokeDasharray="3 3" stroke="#2d2d2d" />
                <XAxis dataKey="date" tick={{ fontSize: 10, fill: '#8c8c8c' }}
                  tickFormatter={d => d.slice(5)} />
                <YAxis tick={{ fontSize: 10, fill: '#8c8c8c' }} />
                <Tooltip contentStyle={ttStyle}
                  formatter={(v: number, n: string) => [v.toFixed(2), n === 'daily_pnl' ? '日度' : '累计']} />
                <Bar dataKey="daily_pnl" name="daily_pnl">
                  {cum.map((p, i) => (
                    <Cell key={i} fill={p.daily_pnl >= 0 ? colors.up : colors.down} />
                  ))}
                </Bar>
                <Line type="monotone" dataKey="cumulative" name="cumulative"
                  stroke="#4096ff" dot={false} strokeWidth={2} />
              </ComposedChart>
            </ResponsiveContainer>
          )}
        </div>
      )}

      {/* Tab 1 — Symbol排名 */}
      {tab === 1 && (
        <div className="bg-[#1f1f1f] border border-[#2d2d2d] rounded-lg p-4">
          {sym.length === 0 ? (
            <div className="h-64 flex items-center justify-center text-gray-600 text-sm">暂无数据</div>
          ) : (
            <ResponsiveContainer width="100%" height={Math.max(200, sym.length * 40)}>
              <BarChart layout="vertical" data={sym}>
                <CartesianGrid strokeDasharray="3 3" stroke="#2d2d2d" horizontal={false} />
                <YAxis type="category" dataKey="symbol" tick={{ fontSize: 11, fill: '#8c8c8c' }} width={100} />
                <XAxis type="number" tick={{ fontSize: 10, fill: '#8c8c8c' }}
                  tickFormatter={v => v.toFixed(1)} />
                <Tooltip contentStyle={ttStyle}
                  formatter={(v: number) => [v.toFixed(2) + ' USDT', 'PnL']} />
                <Bar dataKey="realized_pnl">
                  {sym.map((s, i) => (
                    <Cell key={i} fill={s.realized_pnl >= 0 ? colors.up : colors.down} />
                  ))}
                </Bar>
              </BarChart>
            </ResponsiveContainer>
          )}
        </div>
      )}

      {/* Tab 2 — 平仓原因分布 */}
      {tab === 2 && (
        <div className="bg-[#1f1f1f] border border-[#2d2d2d] rounded-lg p-4 flex gap-6">
          {exits.length === 0 ? (
            <div className="flex-1 h-64 flex items-center justify-center text-gray-600 text-sm">暂无数据</div>
          ) : (
            <>
              <ResponsiveContainer width="50%" height={240}>
                <PieChart>
                  <Pie data={exits} dataKey="count" nameKey="exit_reason"
                    cx="50%" cy="50%" outerRadius={90} fontSize={10}
                    label={({ exit_reason, percent }) => `${exit_reason} ${(percent * 100).toFixed(0)}%`}
                    labelLine={false}>
                    {exits.map((e, i) => (
                      <Cell key={i} fill={EXIT_PALETTE[e.exit_reason] ?? PIE_FALLBACK} />
                    ))}
                  </Pie>
                  <Tooltip contentStyle={ttStyle} />
                </PieChart>
              </ResponsiveContainer>
              <div className="flex-1 space-y-2 self-center">
                {exits.map((e, i) => (
                  <div key={i} className="flex justify-between items-center text-xs gap-3">
                    <span className="font-mono" style={{ color: EXIT_PALETTE[e.exit_reason] ?? '#8c8c8c' }}>
                      {e.exit_reason}
                    </span>
                    <span className="text-gray-500">{e.count} 笔</span>
                    <span style={{ color: pnlColor(e.realized_pnl) }}>
                      {pnlPrefix(e.realized_pnl)}{e.realized_pnl.toFixed(2)}
                    </span>
                  </div>
                ))}
              </div>
            </>
          )}
        </div>
      )}

      {/* Tab 3 — 胜率统计 */}
      {tab === 3 && (
        !stats ? (
          <div className="text-gray-500 text-sm">加载中...</div>
        ) : (
          <div className="space-y-3">
            <div className="grid grid-cols-4 gap-3">
              <StatCard label="总交易数" value={String(stats.total_trades)} />
              <StatCard label="胜率" value={stats.win_rate.toFixed(1) + '%'}
                sub={`${stats.win_count} 盈 / ${stats.loss_count} 亏`} />
              <StatCard label="盈亏比 (PF)" value={stats.profit_factor.toFixed(2)} />
              <StatCard label="总 PnL"
                value={(pnlPrefix(stats.total_pnl)) + stats.total_pnl.toFixed(2) + ' U'} />
            </div>
            <div className="grid grid-cols-4 gap-3">
              <StatCard label="平均 PnL" value={pnlPrefix(stats.avg_pnl) + stats.avg_pnl.toFixed(2) + ' U'} />
              <StatCard label="平均盈利" value={'+' + stats.avg_win_pnl.toFixed(2) + ' U'} />
              <StatCard label="平均亏损" value={stats.avg_loss_pnl.toFixed(2) + ' U'} />
              <StatCard label="平均持仓" value={formatHoldMs(stats.avg_hold_ms)} />
            </div>
          </div>
        )
      )}
    </div>
  )
}
