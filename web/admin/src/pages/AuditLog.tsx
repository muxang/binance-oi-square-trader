// Phase 5.2 Round 3: public audit log viewer.
// mu A1: any visitor can inspect mu's owner operations history without auth.
// Backend GET /api/admin/audit-log paginated; action_type filter optional.

import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import dayjs from 'dayjs'
import { fetchAuditLog, type AuditLogEntry } from '../api/client'

const ACTION_ZH: Record<string, string> = {
  // Round 1 / R.1
  halt_reset:                 '解除 halt',
  manual_full_reset:          '完整重置 (halt + pnl + consec)',
  // Round 2 write endpoints
  daily_pnl_reset:            '重置今日 PnL',
  consec_reset:               '重置连亏计数',
  manual_halt:                '主动 halt',
  cb_thresholds_update:       '调熔断 / 仓位 / Trail 阈值',
  signal_thresholds_update:   '调信号阈值',
  watchlist_include:          '加入 watchlist',
  watchlist_exclude:          '排除 watchlist',
  manual_close:               '手工平仓',
  // 自动 (非 mu 操作,但 trader 自身写入 audit)
  orphan_algo_cleanup:        '孤儿 algo 清理 (trader auto)',
  config_reload:              '配置热更新 (trader auto)',
}

function pretty(v: Record<string, unknown> | null): string {
  if (!v) return '—'
  return JSON.stringify(v, null, 2)
}

function Row({ e, idx, expanded, onToggle }: {
  e: AuditLogEntry; idx: number; expanded: boolean; onToggle: () => void
}) {
  const zh = ACTION_ZH[e.action_type] ?? e.action_type
  return (
    <>
      <tr
        className="border-b border-[#2d2d2d] hover:bg-[#252525] cursor-pointer"
        onClick={onToggle}
      >
        <td className="px-2 sm:px-3 py-2 text-xs text-gray-500 tabular-nums hidden sm:table-cell">{idx + 1}</td>
        <td className="px-2 sm:px-3 py-2 text-xs text-gray-300 tabular-nums whitespace-nowrap">
          {dayjs(e.ts).format('MM-DD HH:mm')}
        </td>
        <td className="px-2 sm:px-3 py-2 text-xs hidden sm:table-cell"><span className="font-mono">{e.operator}</span></td>
        <td className="px-2 sm:px-3 py-2 text-xs text-gray-200">
          <span className="font-semibold">{zh}</span>
          <span className="text-gray-600 ml-1.5 font-mono text-[10px] hidden md:inline">{e.action_type}</span>
          <div className="text-gray-600 text-[10px] mt-0.5 sm:hidden">{e.operator} · {e.action_type}</div>
        </td>
        <td className="px-2 sm:px-3 py-2 text-xs text-gray-400 hidden md:table-cell">
          {e.resource_type && <span className="font-mono">{e.resource_type}</span>}
          {e.resource_id && <span className="text-gray-600 ml-1">#{e.resource_id}</span>}
        </td>
        <td className="px-2 sm:px-3 py-2 text-xs text-gray-400 truncate max-w-[120px] sm:max-w-[280px]" title={e.note}>{e.note || '—'}</td>
        <td className="px-2 sm:px-3 py-2 text-xs text-gray-600 text-right">{expanded ? '▼' : '▶'}</td>
      </tr>
      {expanded && (
        <tr className="border-b border-[#2d2d2d] bg-[#181818]">
          <td colSpan={7} className="px-2 sm:px-3 py-3">
            <div className="grid grid-cols-1 md:grid-cols-2 gap-3 text-xs">
              <div>
                <div className="text-gray-500 mb-1">previous_state</div>
                <pre className="bg-[#0f0f0f] border border-[#2d2d2d] rounded p-2 text-[11px] text-gray-300 overflow-x-auto font-mono">
                  {pretty(e.previous_state)}
                </pre>
              </div>
              <div>
                <div className="text-gray-500 mb-1">new_state</div>
                <pre className="bg-[#0f0f0f] border border-[#2d2d2d] rounded p-2 text-[11px] text-gray-300 overflow-x-auto font-mono">
                  {pretty(e.new_state)}
                </pre>
              </div>
            </div>
            <div className="mt-2 flex flex-wrap gap-2 sm:gap-4 text-[11px] text-gray-600">
              <span>resource: <span className="font-mono">{e.resource_type}/{e.resource_id || '—'}</span></span>
              {e.ip_address && <span>IP: <span className="font-mono">{e.ip_address}</span></span>}
              {e.user_agent && <span className="truncate max-w-[400px]">UA: <span className="font-mono">{e.user_agent}</span></span>}
            </div>
          </td>
        </tr>
      )}
    </>
  )
}

export default function AuditLog() {
  const [page, setPage] = useState(1)
  const [pageSize] = useState(20)
  const [action, setAction] = useState('')
  const [expanded, setExpanded] = useState<number | null>(null)

  const { data, isLoading, error } = useQuery({
    queryKey: ['audit-log', page, pageSize, action],
    queryFn: () => fetchAuditLog(page, pageSize, action || undefined),
    refetchInterval: 15_000,
  })

  const totalPages = data ? Math.max(1, Math.ceil(data.total / pageSize)) : 1

  return (
    <div className="p-4 sm:p-6 space-y-4">
      <div>
        <h1 className="text-lg font-bold text-white">📋 操作历史</h1>
        <p className="text-xs text-gray-500 mt-1">
          mu A1 公开访问 — 任何人可看 trader 真盘运维 audit。来源: <span className="font-mono">admin_audit_log</span> 表。
        </p>
      </div>

      <div className="flex items-center gap-3">
        <label className="text-xs text-gray-500">过滤 action:</label>
        <select
          value={action}
          onChange={e => { setAction(e.target.value); setPage(1) }}
          className="px-2 py-1 text-xs rounded bg-[#0f0f0f] border border-[#3d3d3d] text-gray-200"
        >
          <option value="">全部</option>
          {Object.entries(ACTION_ZH).map(([k, v]) => (
            <option key={k} value={k}>{v} ({k})</option>
          ))}
        </select>
        <div className="ml-auto text-xs text-gray-500">
          {data ? `${data.total} 条记录 · 第 ${data.page}/${totalPages} 页` : '...'}
        </div>
      </div>

      <div className="bg-[#1f1f1f] border border-[#2d2d2d] rounded-lg overflow-hidden">
        {isLoading && <div className="p-6 text-gray-500 text-sm text-center">加载中...</div>}
        {error && <div className="p-6 text-red-400 text-sm text-center">加载失败</div>}
        {data && data.items.length === 0 && (
          <div className="p-6 text-gray-600 text-sm text-center">无记录</div>
        )}
        {data && data.items.length > 0 && (
          <table className="w-full text-left">
            <thead className="bg-[#181818] border-b border-[#2d2d2d]">
              <tr className="text-xs text-gray-500">
                <th className="px-2 sm:px-3 py-2 font-normal hidden sm:table-cell">#</th>
                <th className="px-2 sm:px-3 py-2 font-normal">时间</th>
                <th className="px-2 sm:px-3 py-2 font-normal hidden sm:table-cell">操作者</th>
                <th className="px-2 sm:px-3 py-2 font-normal">操作</th>
                <th className="px-2 sm:px-3 py-2 font-normal hidden md:table-cell">资源</th>
                <th className="px-2 sm:px-3 py-2 font-normal">备注</th>
                <th className="px-2 sm:px-3 py-2 font-normal text-right"></th>
              </tr>
            </thead>
            <tbody>
              {data.items.map((e, i) => (
                <Row
                  key={e.id}
                  e={e}
                  idx={(page - 1) * pageSize + i}
                  expanded={expanded === e.id}
                  onToggle={() => setExpanded(expanded === e.id ? null : e.id)}
                />
              ))}
            </tbody>
          </table>
        )}
      </div>

      {data && totalPages > 1 && (
        <div className="flex items-center justify-center gap-3 text-xs">
          <button
            onClick={() => setPage(p => Math.max(1, p - 1))}
            disabled={page <= 1}
            className="px-3 py-1 rounded bg-[#2d2d2d] text-gray-300 disabled:opacity-30"
          >
            ← 上一页
          </button>
          <span className="text-gray-500">{page} / {totalPages}</span>
          <button
            onClick={() => setPage(p => Math.min(totalPages, p + 1))}
            disabled={page >= totalPages}
            className="px-3 py-1 rounded bg-[#2d2d2d] text-gray-300 disabled:opacity-30"
          >
            下一页 →
          </button>
        </div>
      )}
    </div>
  )
}
