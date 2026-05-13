// Phase 5.2 Round 3: reusable two-step confirm modal for write operations.
//
// Pattern matches the v0.1 inline halt-reset modal but factored out so the
// 6+ Round 2 write endpoints don't each duplicate ~60 lines of dialog code.
//
// Children render the operation-specific body (context, inputs, risk warning).
// onConfirm fires the mutation; isPending disables both buttons + shows
// loading state. Backdrop click and ESC close (when not pending).

import { useEffect, type ReactNode } from 'react'
import { colors } from '../theme/colors'

export type ConfirmTone = 'danger' | 'warning' | 'primary'

interface Props {
  open: boolean
  title: string
  tone?: ConfirmTone        // default 'warning' (orange) — 'danger' = halt/close, 'primary' = neutral edit
  confirmLabel?: string     // default '确认'
  cancelLabel?: string      // default '取消'
  isPending?: boolean
  error?: string | null
  onCancel: () => void
  onConfirm: () => void
  children: ReactNode
}

const toneColor = (tone: ConfirmTone): string => {
  switch (tone) {
    case 'danger':  return colors.halt
    case 'primary': return '#3b82f6'      // tailwind blue-500
    default:        return colors.warning
  }
}

export function ConfirmModal({
  open, title, tone = 'warning', confirmLabel = '确认', cancelLabel = '取消',
  isPending = false, error = null, onCancel, onConfirm, children,
}: Props) {
  useEffect(() => {
    if (!open) return
    const handler = (e: KeyboardEvent) => {
      if (e.key === 'Escape' && !isPending) onCancel()
    }
    window.addEventListener('keydown', handler)
    return () => window.removeEventListener('keydown', handler)
  }, [open, isPending, onCancel])

  if (!open) return null
  const accent = toneColor(tone)
  return (
    <div
      className="fixed inset-0 z-50 flex items-center justify-center"
      style={{ background: 'rgba(0,0,0,0.6)' }}
      onClick={() => !isPending && onCancel()}
    >
      <div
        className="bg-[#1f1f1f] border border-[#3d3d3d] rounded-lg p-6 max-w-md w-full mx-4"
        onClick={e => e.stopPropagation()}
      >
        <h3 className="text-lg font-bold mb-3" style={{ color: accent }}>{title}</h3>
        <div className="text-sm text-gray-300 mb-4">{children}</div>
        {error && (
          <div
            className="text-xs mb-3 px-3 py-2 rounded"
            style={{ background: colors.halt + '15', color: colors.halt }}
          >
            操作失败: {error}
          </div>
        )}
        <div className="flex gap-2 justify-end">
          <button
            onClick={onCancel}
            disabled={isPending}
            className="px-4 py-1.5 text-sm rounded bg-[#2d2d2d] text-gray-300 hover:bg-[#3d3d3d] disabled:opacity-50"
          >
            {cancelLabel}
          </button>
          <button
            onClick={onConfirm}
            disabled={isPending}
            className="px-4 py-1.5 text-sm rounded font-medium disabled:opacity-50"
            style={{ background: accent, color: '#fff' }}
          >
            {isPending ? '执行中...' : confirmLabel}
          </button>
        </div>
      </div>
    </div>
  )
}

export function errorMessage(err: unknown): string {
  if (!err) return ''
  if (err instanceof Error) return err.message
  // axios error response.data.error
  const e = err as { response?: { data?: { error?: string } } }
  if (e.response?.data?.error) return e.response.data.error
  return String(err)
}
