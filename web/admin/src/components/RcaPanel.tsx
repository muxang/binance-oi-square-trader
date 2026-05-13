// Phase 5.2 Round 4 Part 2: pending halt RCA panel for Dashboard.
// Shows unacknowledged halt_rca rows with full context_json + 3 ack actions
// (resolved / investigating / ignored). mu's mobile flow lands here from
// the Feishu 🔴 critical deep link.

import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import dayjs from 'dayjs'
import {
  ackHaltRCA, fetchHaltRCAUnack, type HaltRCAEntry, type RcaAction,
} from '../api/client'
import { ConfirmModal, errorMessage } from './ConfirmModal'
import { colors } from '../theme/colors'

const ACTION_LABEL: Record<RcaAction, string> = {
  resolved:       '✅ 已解决',
  investigating:  '🔍 调查中',
  ignored:        '⚪ 忽略',
}

const HALT_TYPE_ZH: Record<string, string> = {
  local_only_orphan:               '本地孤儿持仓 (Binance 已无持仓)',
  binance_only_unknown:            'Binance 孤儿 (本地无 trade)',
  drift_exceeded:                  '持仓 qty 偏差 > 5%',
  circuit_breaker_daily_loss:      '熔断: 日内 PnL 跌破阈值',
  circuit_breaker_consec_losses:   '熔断: 连亏次数超阈值',
  circuit_breaker_btc_crash:       '熔断: BTC 30min 跌幅',
  circuit_breaker_total_float_loss:'熔断: 总浮亏跌破阈值',
  circuit_breaker_api_error:       '熔断: API 错误率超阈值',
}

function RcaCard({ rca, onAck }: { rca: HaltRCAEntry; onAck: (action: RcaAction, note: string) => void }) {
  const [pending, setPending] = useState<null | RcaAction>(null)
  const [note, setNote] = useState('')

  const haltLabel = HALT_TYPE_ZH[rca.halt_type] ?? rca.halt_type
  const triggered = dayjs(rca.triggered_at).format('YYYY-MM-DD HH:mm:ss')

  return (
    <div className="border border-[#3d3d3d] rounded p-3 sm:p-4 bg-[#181818]">
      <div className="flex flex-wrap items-center gap-2 mb-2">
        <span className="text-sm font-semibold" style={{ color: colors.halt }}>{haltLabel}</span>
        <span className="text-xs font-mono text-gray-600">#{rca.id} · {rca.halt_type}</span>
        <span className="text-xs text-gray-500 ml-auto">{triggered}</span>
      </div>
      <pre className="bg-[#0f0f0f] border border-[#2d2d2d] rounded p-2 text-[11px] text-gray-300 overflow-x-auto font-mono mb-3">
        {JSON.stringify(rca.context_json, null, 2)}
      </pre>
      <div className="flex flex-wrap gap-2">
        {(Object.keys(ACTION_LABEL) as RcaAction[]).map(a => (
          <button
            key={a}
            onClick={() => { setPending(a); setNote('') }}
            className="text-xs px-3 py-1.5 rounded bg-[#2d2d2d] text-gray-200 hover:bg-[#3d3d3d] min-h-[40px] sm:min-h-0"
          >
            {ACTION_LABEL[a]}
          </button>
        ))}
      </div>

      <ConfirmModal
        open={pending !== null}
        title={`${pending ? ACTION_LABEL[pending] : ''} RCA #${rca.id}`}
        tone="warning"
        confirmLabel="确认 ack"
        onCancel={() => setPending(null)}
        onConfirm={() => { if (pending) { onAck(pending, note); setPending(null) } }}
      >
        <div className="text-xs space-y-2 mb-3">
          <div><span className="text-gray-500">halt_type:</span> {haltLabel}</div>
          <div><span className="text-gray-500">action:</span> {pending}</div>
          <div className="text-gray-500">
            写 admin_audit_log + halt_rca.mu_acknowledged=true。<b>不影响</b> 当前 halt 状态(用 Dashboard halt-reset 按钮单独清除)。
          </div>
        </div>
        <label className="block text-xs text-gray-500 mb-1">备注 (可选):</label>
        <input
          type="text" value={note} onChange={e => setNote(e.target.value)}
          placeholder="e.g. trail close race;F1 fix 已部署"
          className="w-full px-3 py-1.5 text-sm rounded bg-[#0f0f0f] border border-[#3d3d3d] text-gray-200"
        />
      </ConfirmModal>
    </div>
  )
}

export function RcaPanel() {
  const qc = useQueryClient()
  const { data, isLoading } = useQuery({
    queryKey: ['rca-unack'],
    queryFn: fetchHaltRCAUnack,
    refetchInterval: 15_000,
  })

  const ackMut = useMutation({
    mutationFn: ({ id, action, note }: { id: number; action: RcaAction; note: string }) =>
      ackHaltRCA(id, action, note),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['rca-unack'] })
      qc.invalidateQueries({ queryKey: ['audit-log'] })
    },
  })

  if (isLoading) return null
  if (!data || data.total === 0) return null

  return (
    <div className="bg-[#1f1f1f] border border-[#2d2d2d] rounded-lg p-4 sm:p-5">
      <div className="flex items-center justify-between mb-3">
        <h2 className="text-sm font-semibold text-gray-300">
          📋 待 ack 的 halt RCA <span className="text-xs text-gray-600 ml-1">({data.total})</span>
        </h2>
      </div>
      {ackMut.isError && (
        <div
          className="text-xs mb-3 px-3 py-2 rounded"
          style={{ background: colors.halt + '15', color: colors.halt }}
        >
          ack 失败: {errorMessage(ackMut.error)}
        </div>
      )}
      <div className="space-y-3">
        {data.items.map(rca => (
          <RcaCard
            key={rca.id}
            rca={rca}
            onAck={(action, note) => ackMut.mutate({ id: rca.id, action, note })}
          />
        ))}
      </div>
    </div>
  )
}
