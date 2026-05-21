// R.11 Q2 (mu 2026-05-21 request): CoinGecko 符号映射编辑页.
// auto heuristic 给的 mapping 大多对, 但 micro-cap 总有 ~5% 错的 (BB → bitboard
// 而非 BounceBit 等). 此页让 mu 直接搜索 / 排序 / 编辑.
//
// 编辑后 立刻 DELETE coingecko_market_cache 对应 row, 下次 6h supply tick 重新
// 用新 id 拉.
import { useState, useMemo } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import dayjs from 'dayjs'
import { fetchMappingList, updateMapping, autoFixMappings, type MappingAutoFixResponse } from '../api/client'

export default function Mapping() {
  const qc = useQueryClient()
  const { data, isLoading } = useQuery({
    queryKey: ['coingecko-mapping'],
    queryFn: fetchMappingList,
  })
  const [search, setSearch] = useState('')
  const [showOnly, setShowOnly] = useState<'all' | 'no_mcap' | 'watchlist' | 'positions'>('all')
  const [editing, setEditing] = useState<string | null>(null)
  const [draft, setDraft] = useState('')
  const [autoFixResult, setAutoFixResult] = useState<MappingAutoFixResponse | null>(null)

  const autoFixMut = useMutation({
    mutationFn: () => autoFixMappings(30),
    onSuccess: (res) => {
      setAutoFixResult(res)
      qc.invalidateQueries({ queryKey: ['coingecko-mapping'] })
    },
  })

  const updateMut = useMutation({
    mutationFn: ({ symbol, id }: { symbol: string; id: string }) => updateMapping(symbol, id),
    onSuccess: () => {
      setEditing(null)
      setDraft('')
      qc.invalidateQueries({ queryKey: ['coingecko-mapping'] })
    },
  })

  const filtered = useMemo(() => {
    if (!data) return []
    const q = search.trim().toUpperCase()
    return data.items.filter(r => {
      if (q && !r.binance_symbol.includes(q) && !r.coingecko_id.toUpperCase().includes(q)) return false
      switch (showOnly) {
        case 'no_mcap':    return r.market_cap_usd_m === 0
        case 'watchlist':  return r.in_watchlist
        case 'positions':  return r.in_open_position
      }
      return true
    })
  }, [data, search, showOnly])

  return (
    <div className="p-4 md:p-6 space-y-4">
      <div className="flex items-center justify-between flex-wrap gap-2">
        <div>
          <h1 className="text-lg font-bold text-white">🔗 CoinGecko 符号映射</h1>
          <div className="text-xs text-gray-500 mt-0.5">
            {data ? `${data.total} 个映射 · ${data.items.filter(r => r.market_cap_usd_m > 0).length} 个有市值数据` : '加载中…'}
          </div>
        </div>
        <div className="flex gap-2 items-center">
          <input
            type="text" value={search} onChange={e => setSearch(e.target.value)}
            placeholder="搜索 symbol 或 coingecko id"
            className="px-3 py-1.5 text-sm bg-[#252525] border border-[#3a3a3a] rounded text-white w-64 max-w-full"
          />
          <button
            onClick={() => {
              if (confirm('扫描 OI/市值占比 > 30% 的 mapping, 用 CoinGecko /search 自动找市值最高的 canonical token. 确定继续?')) {
                setAutoFixResult(null)
                autoFixMut.mutate()
              }
            }}
            disabled={autoFixMut.isPending}
            className="px-3 py-1.5 text-sm bg-amber-700 hover:bg-amber-600 text-white rounded disabled:opacity-50 whitespace-nowrap"
            title="批量自动修正异常映射 (CoinGecko /search canonical)">
            {autoFixMut.isPending ? '⏳ 修正中...' : '🔧 自动修正异常'}
          </button>
        </div>
      </div>

      {autoFixResult && (
        <div className="bg-[#1f1f1f] border border-amber-700 rounded-lg p-3 text-xs">
          <div className="font-semibold text-amber-400 mb-2">
            自动修正结果:扫描 {autoFixResult.scanned} 个超阈 ({autoFixResult.threshold_pct}%) mapping, 修正 {autoFixResult.fixed} 个
          </div>
          {autoFixResult.items.length > 0 && (
            <div className="max-h-60 overflow-y-auto space-y-1">
              {autoFixResult.items.map(i => (
                <div key={i.symbol} className="flex gap-2 font-mono">
                  <span className="text-gray-400 w-32">{i.symbol}</span>
                  <span className="text-red-400 w-32 truncate" title={i.old_id}>{i.old_id}</span>
                  <span className="text-gray-600">→</span>
                  <span className={`w-32 truncate ${i.status === 'fixed' ? 'text-green-400' : 'text-gray-500'}`} title={i.new_id || '(none)'}>
                    {i.new_id || '—'}
                  </span>
                  <span className="text-gray-500 w-20 text-right">{i.old_ratio_pct.toFixed(1)}%</span>
                  <span className="text-gray-600">{i.status}</span>
                </div>
              ))}
            </div>
          )}
          <button onClick={() => setAutoFixResult(null)} className="mt-2 text-gray-500 hover:text-white">关闭 ✕</button>
        </div>
      )}

      <div className="flex gap-2 text-xs">
        {([
          { k: 'all',       label: `全部 (${data?.items.length ?? 0})` },
          { k: 'no_mcap',   label: `无市值 (${data?.items.filter(r => r.market_cap_usd_m === 0).length ?? 0})` },
          { k: 'watchlist', label: `候选池 (${data?.items.filter(r => r.in_watchlist).length ?? 0})` },
          { k: 'positions', label: `持仓中 (${data?.items.filter(r => r.in_open_position).length ?? 0})` },
        ] as const).map(t => (
          <button key={t.k} onClick={() => setShowOnly(t.k)}
            className={`px-3 py-1 rounded ${showOnly === t.k ? 'bg-blue-700 text-white' : 'bg-[#252525] text-gray-400 hover:text-white'}`}>
            {t.label}
          </button>
        ))}
      </div>

      <div className="bg-[#1f1f1f] border border-[#2d2d2d] rounded-lg overflow-hidden">
        {isLoading && <div className="p-6 text-gray-500 text-sm">加载中…</div>}
        {!isLoading && filtered.length === 0 && <div className="p-6 text-gray-500 text-sm">无数据</div>}
        {!isLoading && filtered.length > 0 && (
          <table className="w-full text-sm">
            <thead className="border-b border-[#2d2d2d] text-xs text-gray-500">
              <tr>
                <th className="text-left py-2 px-3">Binance Symbol</th>
                <th className="text-left py-2 px-3">CoinGecko ID</th>
                <th className="text-right py-2 px-3">市值 (USD M)</th>
                <th className="text-right py-2 px-3">流通量</th>
                <th className="text-left py-2 px-3">标记</th>
                <th className="text-right py-2 px-3">刷新时间</th>
                <th className="text-center py-2 px-3">操作</th>
              </tr>
            </thead>
            <tbody>
              {filtered.map(r => (
                <tr key={r.binance_symbol} className="border-b border-[#252525] hover:bg-[#252525]">
                  <td className="py-2 px-3 font-mono text-white">{r.binance_symbol}</td>
                  <td className="py-2 px-3 font-mono text-xs">
                    {editing === r.binance_symbol ? (
                      <input
                        type="text" value={draft} onChange={e => setDraft(e.target.value)}
                        placeholder="e.g. bitcoin"
                        className="px-2 py-1 bg-[#1a1a1a] border border-blue-700 rounded text-white w-40 font-mono text-xs"
                        autoFocus
                      />
                    ) : (
                      <span className={r.market_cap_usd_m === 0 ? 'text-yellow-500' : 'text-gray-300'}>{r.coingecko_id}</span>
                    )}
                  </td>
                  <td className="py-2 px-3 text-right tabular-nums text-xs">
                    {r.market_cap_usd_m > 0
                      ? r.market_cap_usd_m >= 1000
                        ? (r.market_cap_usd_m / 1000).toFixed(2) + 'B'
                        : r.market_cap_usd_m.toFixed(1) + 'M'
                      : <span className="text-red-400">无数据</span>}
                  </td>
                  <td className="py-2 px-3 text-right tabular-nums text-xs text-gray-500">
                    {r.circulating_supply > 0 ? r.circulating_supply.toExponential(2) : '—'}
                  </td>
                  <td className="py-2 px-3">
                    {r.in_open_position && <span className="text-xs px-1.5 py-0.5 rounded mr-1 bg-green-900 text-green-300">持仓</span>}
                    {r.in_watchlist && <span className="text-xs px-1.5 py-0.5 rounded bg-blue-900 text-blue-300">候选</span>}
                  </td>
                  <td className="py-2 px-3 text-right text-xs text-gray-600">
                    {r.last_refreshed_ms > 0 ? dayjs(r.last_refreshed_ms).format('MM-DD HH:mm') : '—'}
                  </td>
                  <td className="py-2 px-3 text-center">
                    {editing === r.binance_symbol ? (
                      <div className="flex gap-1 justify-center">
                        <button
                          onClick={() => draft.trim() && updateMut.mutate({ symbol: r.binance_symbol, id: draft.trim() })}
                          disabled={updateMut.isPending || !draft.trim()}
                          className="px-2 py-1 text-xs bg-blue-700 text-white rounded disabled:opacity-50">
                          保存
                        </button>
                        <button
                          onClick={() => { setEditing(null); setDraft('') }}
                          className="px-2 py-1 text-xs bg-[#3a3a3a] text-gray-300 rounded">
                          取消
                        </button>
                      </div>
                    ) : (
                      <button
                        onClick={() => { setEditing(r.binance_symbol); setDraft(r.coingecko_id) }}
                        className="px-2 py-1 text-xs bg-[#252525] text-gray-400 rounded hover:text-white">
                        编辑
                      </button>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>

      {updateMut.isError && (
        <div className="text-red-400 text-xs">
          更新失败:{(updateMut.error as Error)?.message ?? 'unknown'}
        </div>
      )}

      <div className="text-xs text-gray-600">
        💡 编辑 mapping 后,旧 cache 立即清空,下一次 6h supply collector 用新 id 重拉。如果改的 symbol
        是非 watchlist,数据会在 0/6/12/18 BJT 自动刷新。如急用可重启 trader 触发立即拉取。
      </div>
    </div>
  )
}
