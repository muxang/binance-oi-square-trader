// R.14 (mu 2026-05-27): 价格标记页. mu 设目标价, price_mark collector (*/1) 监测
// mark price, 命中即 status=triggered + 发 🟡 飞书, 此页顶部红条提示直到确认.
// 也可从 Market 行 🔔 快速创建 (R.14d).
import { useState, useMemo } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import dayjs from 'dayjs'
import {
  fetchPriceMarks, createPriceMark, ackPriceMark, deletePriceMark,
  type PriceMarkRow,
} from '../api/client'
import SymbolLink from '../components/SymbolLink'

export default function PriceMarks() {
  const qc = useQueryClient()
  const { data, isLoading } = useQuery({
    queryKey: ['price-marks'],
    queryFn: fetchPriceMarks,
    refetchInterval: 30_000,
  })

  const [symbol, setSymbol] = useState('')
  const [target, setTarget] = useState('')
  const [direction, setDirection] = useState<'above' | 'below'>('above')
  const [note, setNote] = useState('')

  const invalidate = () => qc.invalidateQueries({ queryKey: ['price-marks'] })
  const createMut = useMutation({
    mutationFn: () => createPriceMark({ symbol, target_price: target, direction, note }),
    onSuccess: () => { setSymbol(''); setTarget(''); setNote(''); invalidate() },
  })
  const ackMut = useMutation({ mutationFn: ackPriceMark, onSuccess: invalidate })
  const delMut = useMutation({ mutationFn: deletePriceMark, onSuccess: invalidate })

  const canCreate = symbol.trim() !== '' && Number(target) > 0
  const triggered = useMemo(
    () => (data?.items ?? []).filter(m => m.status === 'triggered' && !m.acknowledged),
    [data],
  )

  return (
    <div className="p-4 md:p-6 space-y-4">
      <div>
        <h1 className="text-lg font-bold text-white">🔔 价格标记</h1>
        <div className="text-xs text-gray-500 mt-0.5">
          {data ? `${data.total} 个标记 · ${triggered.length} 个待确认` : '加载中…'}
        </div>
      </div>

      {triggered.length > 0 && (
        <div className="bg-red-950 border border-red-600 rounded-lg p-3 text-sm text-red-200">
          🔔 <b>{triggered.length} 个标记已触发</b>:
          {triggered.map(m => ` ${m.symbol}@${m.target_price}`).join(' ·')}
        </div>
      )}

      {/* 新建标记 */}
      <div className="bg-[#1f1f1f] border border-[#2d2d2d] rounded-lg p-3 flex flex-wrap gap-2 items-end">
        <Field label="Symbol">
          <input value={symbol} onChange={e => setSymbol(e.target.value.toUpperCase())}
            placeholder="BTCUSDT"
            className="px-2 py-1.5 text-sm bg-[#252525] border border-[#3a3a3a] rounded text-white w-32 font-mono" />
        </Field>
        <Field label="方向">
          <select value={direction} onChange={e => setDirection(e.target.value as 'above' | 'below')}
            className="px-2 py-1.5 text-sm bg-[#252525] border border-[#3a3a3a] rounded text-white">
            <option value="above">↑ 突破达到</option>
            <option value="below">↓ 跌破</option>
          </select>
        </Field>
        <Field label="目标价">
          <input value={target} onChange={e => setTarget(e.target.value)}
            placeholder="0.0" inputMode="decimal"
            className="px-2 py-1.5 text-sm bg-[#252525] border border-[#3a3a3a] rounded text-white w-32 font-mono" />
        </Field>
        <Field label="备注 (可选)">
          <input value={note} onChange={e => setNote(e.target.value)}
            className="px-2 py-1.5 text-sm bg-[#252525] border border-[#3a3a3a] rounded text-white w-48" />
        </Field>
        <button onClick={() => createMut.mutate()} disabled={!canCreate || createMut.isPending}
          className="px-4 py-1.5 text-sm bg-blue-700 hover:bg-blue-600 text-white rounded disabled:opacity-40">
          {createMut.isPending ? '创建中…' : '+ 创建标记'}
        </button>
        {createMut.isError && (
          <span className="text-red-400 text-xs">
            {(createMut.error as { response?: { data?: { error?: string } } })?.response?.data?.error ?? '创建失败'}
          </span>
        )}
      </div>

      {/* 列表 */}
      <div className="bg-[#1f1f1f] border border-[#2d2d2d] rounded-lg overflow-hidden">
        {isLoading && <div className="p-6 text-gray-500 text-sm">加载中…</div>}
        {!isLoading && (data?.items.length ?? 0) === 0 && <div className="p-6 text-gray-500 text-sm">暂无标记</div>}
        {!isLoading && (data?.items.length ?? 0) > 0 && (
          <table className="w-full text-sm">
            <thead className="border-b border-[#2d2d2d] text-xs text-gray-500">
              <tr>
                <th className="text-left py-2 px-3">Symbol</th>
                <th className="text-left py-2 px-3">方向</th>
                <th className="text-right py-2 px-3">目标价</th>
                <th className="text-right py-2 px-3">当前价</th>
                <th className="text-left py-2 px-3">状态</th>
                <th className="text-left py-2 px-3">备注</th>
                <th className="text-right py-2 px-3">创建时间</th>
                <th className="text-center py-2 px-3">操作</th>
              </tr>
            </thead>
            <tbody>
              {data!.items.map(m => <Row key={m.id} m={m}
                onAck={() => ackMut.mutate(m.id)} onDel={() => delMut.mutate(m.id)}
                busy={ackMut.isPending || delMut.isPending} />)}
            </tbody>
          </table>
        )}
      </div>

      <div className="text-xs text-gray-600">
        💡 一次性触发:命中后停止监测并红条提示,确认后消除。需要重新监控就再建一条。已配置飞书时同步推送 🟡 通知。
        当前价来自持仓采价缓存,非持仓 symbol 可能显示「—」。
      </div>
    </div>
  )
}

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <label className="flex flex-col gap-1">
      <span className="text-xs text-gray-500">{label}</span>
      {children}
    </label>
  )
}

function Row({ m, onAck, onDel, busy }: {
  m: PriceMarkRow; onAck: () => void; onDel: () => void; busy: boolean
}) {
  const unacked = m.status === 'triggered' && !m.acknowledged
  return (
    <tr className={`border-b border-[#252525] hover:bg-[#252525] ${unacked ? 'bg-red-950/40' : ''}`}>
      <td className="py-2 px-3 font-mono text-white">
        <SymbolLink symbol={m.symbol} />
      </td>
      <td className="py-2 px-3">
        <span className={m.direction === 'above' ? 'text-green-400' : 'text-red-400'}>
          {m.direction === 'above' ? '↑ 突破' : '↓ 跌破'}
        </span>
      </td>
      <td className="py-2 px-3 text-right tabular-nums font-mono text-gray-200">{m.target_price}</td>
      <td className="py-2 px-3 text-right tabular-nums font-mono text-gray-400">{m.current_price || '—'}</td>
      <td className="py-2 px-3">
        {m.status === 'triggered'
          ? <span className={`text-xs px-1.5 py-0.5 rounded ${m.acknowledged ? 'bg-gray-700 text-gray-300' : 'bg-red-900 text-red-200'}`}>
              {m.acknowledged ? '已确认' : '🔔 已触发'}{m.triggered_price && ` @${m.triggered_price}`}
            </span>
          : <span className="text-xs px-1.5 py-0.5 rounded bg-blue-900 text-blue-300">监测中</span>}
      </td>
      <td className="py-2 px-3 text-xs text-gray-500 max-w-[12rem] truncate" title={m.note}>{m.note || '—'}</td>
      <td className="py-2 px-3 text-right text-xs text-gray-600">{dayjs(m.created_at_ms).format('MM-DD HH:mm')}</td>
      <td className="py-2 px-3 text-center whitespace-nowrap">
        {unacked && (
          <button onClick={onAck} disabled={busy}
            className="px-2 py-1 text-xs bg-green-800 hover:bg-green-700 text-white rounded mr-1 disabled:opacity-40">确认</button>
        )}
        <button onClick={onDel} disabled={busy}
          className="px-2 py-1 text-xs bg-[#3a3a3a] hover:bg-red-800 text-gray-300 rounded disabled:opacity-40">删除</button>
      </td>
    </tr>
  )
}
