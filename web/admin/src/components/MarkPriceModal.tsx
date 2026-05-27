// R.14d (mu 2026-05-27): 从 Market 行 🔔 快速创建价格标记的小弹窗.
// 方向按「目标价 vs 该行当前价」自动判定 (target ≥ 现价 → above 突破, 否则 below
// 跌破), 无需手选 — 这是相比标准页的便利点. 当前价直接来自 Market 行, 准确.
import { useState } from 'react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { createPriceMark } from '../api/client'

export default function MarkPriceModal({
  symbol, currentPrice, onClose,
}: { symbol: string; currentPrice: number; onClose: () => void }) {
  const qc = useQueryClient()
  const [target, setTarget] = useState('')
  const [note, setNote] = useState('')

  const targetNum = Number(target)
  const valid = target.trim() !== '' && targetNum > 0
  // 自动方向:目标 ≥ 现价 → 等突破; < 现价 → 等跌破。现价缺失 (0) 时默认 above。
  const direction: 'above' | 'below' = !currentPrice || targetNum >= currentPrice ? 'above' : 'below'

  const createMut = useMutation({
    mutationFn: () => createPriceMark({
      symbol, target_price: target, direction, note,
      current_price: currentPrice ? String(currentPrice) : undefined,
    }),
    onSuccess: () => { qc.invalidateQueries({ queryKey: ['price-marks'] }); onClose() },
  })

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/60" onClick={onClose}>
      <div className="bg-[#1f1f1f] border border-[#3a3a3a] rounded-lg p-5 w-80 space-y-3"
        onClick={e => e.stopPropagation()}>
        <div className="flex items-center justify-between">
          <h2 className="text-white font-semibold">🔔 标记价格 <span className="font-mono text-blue-400">{symbol}</span></h2>
          <button onClick={onClose} className="text-gray-500 hover:text-white text-lg">✕</button>
        </div>
        <div className="text-xs text-gray-500">
          当前价 <span className="font-mono text-gray-300">{currentPrice ? currentPrice : '—'}</span>
        </div>
        <label className="block">
          <span className="text-xs text-gray-500">目标价</span>
          <input value={target} onChange={e => setTarget(e.target.value)} autoFocus
            placeholder="0.0" inputMode="decimal"
            className="mt-1 w-full px-2 py-1.5 text-sm bg-[#252525] border border-[#3a3a3a] rounded text-white font-mono" />
        </label>
        {valid && (
          <div className="text-xs">
            方向:<span className={direction === 'above' ? 'text-green-400' : 'text-red-400'}>
              {direction === 'above' ? '↑ 突破/达到 目标价时通知' : '↓ 跌破 目标价时通知'}
            </span>
          </div>
        )}
        <label className="block">
          <span className="text-xs text-gray-500">备注 (可选)</span>
          <input value={note} onChange={e => setNote(e.target.value)}
            className="mt-1 w-full px-2 py-1.5 text-sm bg-[#252525] border border-[#3a3a3a] rounded text-white" />
        </label>
        {createMut.isError && (
          <div className="text-red-400 text-xs">
            {(createMut.error as { response?: { data?: { error?: string } } })?.response?.data?.error ?? '创建失败'}
          </div>
        )}
        <button onClick={() => createMut.mutate()} disabled={!valid || createMut.isPending}
          className="w-full px-4 py-2 text-sm bg-blue-700 hover:bg-blue-600 text-white rounded disabled:opacity-40">
          {createMut.isPending ? '创建中…' : '创建标记'}
        </button>
      </div>
    </div>
  )
}
