// Symbol detail sidebar — opened from Market table OR Uptrend table row click.
// Renders OI/Square SNAPSHOT card at top (numbers, not just charts) so cross-
// reference with uptrend signals doesn't require Tab switching. Below: existing
// time-series charts (OI / price / square / ratios). Bottom: jump-to-OI/Square
// button for context-switch into the full Market table filtered by this symbol.
import { useContext } from 'react'
import { useQuery } from '@tanstack/react-query'
import dayjs from 'dayjs'
import {
  LineChart, Line, XAxis, YAxis, Tooltip, ResponsiveContainer,
} from 'recharts'
import { fetchSymbolDetail, fetchMarket } from '../api/client'
import { DataSourceContext } from '../context/DataSource'
import { pnlColor, pnlPrefix } from '../theme/colors'
import SymbolLink from './SymbolLink'

function pct(v: number) { return (v >= 0 ? '+' : '') + v.toFixed(2) + '%' }
function fmtOi(m: number) { return m >= 1000 ? (m / 1000).toFixed(1) + 'B' : m.toFixed(1) + 'M' }
function fmtPrice(p: number) {
  if (!p) return '—'
  if (p >= 1000) return p.toLocaleString('en-US', { maximumFractionDigits: 2 })
  if (p >= 1)    return p.toFixed(4)
  return p.toFixed(6)
}
function colorPct(v: number) { return v > 0 ? '#52c41a' : v < 0 ? '#ff4d4f' : '#8c8c8c' }
function lsColor(r: number) { return r > 0 ? (r < 1 ? '#ff4d4f' : '#52c41a') : '#555' }

export default function SymbolSidebar({
  symbol, onClose, onJumpToMarket,
}: {
  symbol: string
  onClose: () => void
  onJumpToMarket?: (sym: string) => void
}) {
  const { dataSource } = useContext(DataSourceContext)
  const { data, isLoading } = useQuery({
    queryKey: ['symbol-detail', symbol, dataSource],
    queryFn: () => fetchSymbolDetail(symbol, 24, dataSource),
  })
  // Cross-reference OI/Square snapshot from the warm market cache (R.19).
  // Substring search ≈ exact-symbol match since full symbols are unique substrings.
  const { data: marketData } = useQuery({
    queryKey: ['symbol-market-snapshot', symbol],
    queryFn: () => fetchMarket({ search: symbol, size: 50 }),
    enabled: !!symbol,
    staleTime: 60_000,
  })
  const m = marketData?.items.find(it => it.symbol === symbol)

  const ttStyle      = { background: '#252525', border: '1px solid #3d3d3d', fontSize: 11, color: '#e5e7eb' }
  const ttLabelStyle = { color: '#e5e7eb' }
  const ttItemStyle  = { color: '#e5e7eb' }

  return (
    <div className="w-80 bg-[#1a1a1a] border-l border-[#2d2d2d] flex flex-col shrink-0">
      <div className="flex items-center justify-between p-4 border-b border-[#2d2d2d]">
        <div>
          <div className="font-mono font-bold text-white">
            <SymbolLink symbol={symbol} />
          </div>
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

      {/* OI/Square SNAPSHOT card — visible immediately, no chart loading needed. */}
      {m && (
        <div className="mx-3 mt-3 bg-[#252525] border border-[#2d2d2d] rounded p-2.5 text-xs">
          <div className="flex items-center justify-between mb-2">
            <span className="text-gray-500 font-semibold">OI / Square 快照</span>
            {onJumpToMarket && (
              <button onClick={() => onJumpToMarket(symbol)}
                      className="text-[10px] text-blue-400 hover:text-blue-300 hover:underline"
                      title="切到 OI/Square Tab 并过滤至此 symbol,可与同期其他币对比">
                完整列表 →
              </button>
            )}
          </div>
          <div className="grid grid-cols-2 gap-x-3 gap-y-1.5">
            <div>
              <div className="text-[10px] text-gray-600">OI 价值</div>
              <div className="tabular-nums text-gray-300">{m.oi_usd_m > 0 ? fmtOi(m.oi_usd_m) : '—'}</div>
            </div>
            <div>
              <div className="text-[10px] text-gray-600">流动市值</div>
              <div className="tabular-nums text-gray-300">{m.cmcap_usd_m > 0 ? fmtOi(m.cmcap_usd_m) : '—'}</div>
            </div>
            <div>
              <div className="text-[10px] text-gray-600">OI 1h</div>
              <div className="tabular-nums font-semibold" style={{ color: colorPct(m.oi_1h_pct) }}>
                {m.oi_1h_pct !== 0 ? pct(m.oi_1h_pct) : '—'}
              </div>
            </div>
            <div>
              <div className="text-[10px] text-gray-600">OI 24h</div>
              <div className="tabular-nums font-semibold" style={{ color: colorPct(m.oi_24h_pct) }}>
                {m.oi_24h_pct !== 0 ? pct(m.oi_24h_pct) : '—'}
              </div>
            </div>
            <div>
              <div className="text-[10px] text-gray-600">Square 24h 提及</div>
              <div className="tabular-nums text-gray-300">{m.square_mentions > 0 ? m.square_mentions.toLocaleString() : '—'}</div>
            </div>
            <div>
              <div className="text-[10px] text-gray-600">Square 24h%</div>
              <div className="tabular-nums" style={{ color: m.square_mentions > 0 ? colorPct(m.square_24h_pct) : '#555' }}>
                {m.square_mentions > 0 && m.square_24h_pct !== 0 ? pct(m.square_24h_pct) : m.square_mentions > 0 ? '新' : '—'}
              </div>
            </div>
            <div>
              <div className="text-[10px] text-gray-600">账户多空</div>
              <div className="tabular-nums" style={{ color: lsColor(m.acct_ls_ratio) }}>
                {m.acct_ls_ratio > 0 ? m.acct_ls_ratio.toFixed(2) : '—'}
              </div>
            </div>
            <div>
              <div className="text-[10px] text-gray-600">持仓多空</div>
              <div className="tabular-nums" style={{ color: lsColor(m.pos_ls_ratio) }}>
                {m.pos_ls_ratio > 0 ? m.pos_ls_ratio.toFixed(2) : '—'}
              </div>
            </div>
            <div className="col-span-2">
              <div className="text-[10px] text-gray-600">OI / 流动市值 (≥50% 高风险)</div>
              <div className="tabular-nums"
                   style={{ color: m.mcap_ratio_pct > 0 ? (m.mcap_ratio_pct >= 50 ? '#f5a623' : '#8c8c8c') : '#555' }}>
                {m.mcap_ratio_pct > 0 ? m.mcap_ratio_pct.toFixed(2) + '%' : '—'}
              </div>
            </div>
          </div>
        </div>
      )}

      {data && (
        <div className="flex-1 overflow-y-auto space-y-4 p-3">
          {data.oi_series.length > 0 && (
            <div>
              <div className="text-xs text-gray-500 mb-1">OI (24h, USD M)</div>
              <ResponsiveContainer width="100%" height={100}>
                <LineChart data={data.oi_series}>
                  <XAxis dataKey="ts_ms" hide />
                  <YAxis hide domain={['auto','auto']} />
                  <Tooltip contentStyle={ttStyle} labelStyle={ttLabelStyle} itemStyle={ttItemStyle}
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
                  <Tooltip contentStyle={ttStyle} labelStyle={ttLabelStyle} itemStyle={ttItemStyle}
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
                  <Tooltip contentStyle={ttStyle} labelStyle={ttLabelStyle} itemStyle={ttItemStyle}
                    labelFormatter={(v) => dayjs(v).format('MM-DD HH:mm')}
                    formatter={(v: number) => [v.toLocaleString(), '累计新帖']} />
                  <Line type="monotone" dataKey="mentions" stroke="#fa8c16" dot={false} strokeWidth={1.5} />
                </LineChart>
              </ResponsiveContainer>
            </div>
          )}

          {data.ratios_series.some(p => p.acct_ratio > 0 || p.pos_ratio > 0) && (
            <div>
              <div className="text-xs text-gray-500 mb-1">大户多空比 (24h, &lt;1 空头主导 / &gt;1 多头主导)</div>
              <ResponsiveContainer width="100%" height={100}>
                <LineChart data={data.ratios_series}>
                  <XAxis dataKey="ts_ms" hide />
                  <YAxis hide domain={['auto','auto']} />
                  <Tooltip contentStyle={ttStyle} labelStyle={ttLabelStyle} itemStyle={ttItemStyle}
                    labelFormatter={(v) => dayjs(v).format('MM-DD HH:mm')}
                    formatter={(v: number, name: string) => [v > 0 ? v.toFixed(3) : '—', name === 'acct_ratio' ? '账户多空' : '持仓多空']} />
                  <Line type="monotone" dataKey="acct_ratio" stroke="#4096ff" dot={false} strokeWidth={1.5} connectNulls />
                  <Line type="monotone" dataKey="pos_ratio"  stroke="#fa8c16" dot={false} strokeWidth={1.5} connectNulls />
                </LineChart>
              </ResponsiveContainer>
            </div>
          )}

          {data.ratios_series.some(p => p.mcap_pct > 0) && (
            <div>
              <div className="text-xs text-gray-500 mb-1">持仓市值占比 (24h, ≥50% 高风险)</div>
              <ResponsiveContainer width="100%" height={80}>
                <LineChart data={data.ratios_series}>
                  <XAxis dataKey="ts_ms" hide />
                  <YAxis hide domain={[0, 'auto']} />
                  <Tooltip contentStyle={ttStyle} labelStyle={ttLabelStyle} itemStyle={ttItemStyle}
                    labelFormatter={(v) => dayjs(v).format('MM-DD HH:mm')}
                    formatter={(v: number) => [v > 0 ? v.toFixed(2) + '%' : '—', '市值占比']} />
                  <Line type="monotone" dataKey="mcap_pct" stroke="#f5a623" dot={false} strokeWidth={1.5} connectNulls />
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
