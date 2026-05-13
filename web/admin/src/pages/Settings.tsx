// Phase 5.2 Round 3 — admin settings page. 4 forms covering all
// hot-reloadable runtime knobs that backend Round 2.x/2.y/2.z + signal_engine
// refactor wired:
//
//   1. CB thresholds       (PUT /config/circuit-breaker-thresholds)
//        daily_loss / consec_losses / total_float / btc_panic / max_stop
//        + 4 trail stage activate/upgrade thresholds
//   2. Signal thresholds   (PUT /config/signal-thresholds)
//        OI_GROWTH_FROM_MIN / SQUARE_HOT_MULTIPLIER
//   3. Watchlist include/exclude (PUT /watchlist/{action}/{symbol})
//
// All edits go through ConfirmModal — 2-step + audit log + 1min trader pickup.

import { useState } from 'react'
import { useMutation } from '@tanstack/react-query'
import {
  updateCBThresholds, updateSignalThresholds, watchlistInclude, watchlistExclude,
  type CBThresholdsRequest, type SignalThresholdsRequest,
} from '../api/client'
import { ConfirmModal, errorMessage } from '../components/ConfirmModal'
import { colors } from '../theme/colors'

function FieldRow({ label, hint, children }: { label: string; hint?: string; children: React.ReactNode }) {
  return (
    <div className="grid grid-cols-1 sm:grid-cols-[200px_1fr] gap-1 sm:gap-3 sm:items-center py-2 sm:py-1.5">
      <label className="text-xs text-gray-400">
        {label}
        {hint && <div className="text-[10px] text-gray-600 mt-0.5">{hint}</div>}
      </label>
      {children}
    </div>
  )
}

function TextInput({ value, onChange, placeholder, disabled }: {
  value: string; onChange: (v: string) => void; placeholder?: string; disabled?: boolean
}) {
  return (
    <input
      type="text" value={value} onChange={e => onChange(e.target.value)}
      placeholder={placeholder} disabled={disabled}
      className="w-full px-3 py-2 sm:py-1.5 text-sm rounded bg-[#0f0f0f] border border-[#3d3d3d] text-gray-200 font-mono disabled:opacity-50 min-h-[40px] sm:min-h-0"
    />
  )
}

// ---- 1. CB thresholds form (5 trip + 4 trail) ----

function CBThresholdsCard() {
  const [form, setForm] = useState({
    daily_loss_halt_pct: '',
    consecutive_losses_halt: '',
    total_float_loss_halt_pct: '',
    btc_panic_drop_pct: '',
    max_stop_pct: '',
    trail_stage1_activate_pct: '',
    trail_stage2_upgrade_pct: '',
    trail_stage3_upgrade_pct: '',
    trail_stage4_upgrade_pct: '',
    note: '',
  })
  const [confirmOpen, setConfirmOpen] = useState(false)
  const upd = (k: keyof typeof form) => (v: string) => setForm(s => ({ ...s, [k]: v }))

  const buildPayload = (): CBThresholdsRequest => {
    const out: CBThresholdsRequest = {}
    if (form.daily_loss_halt_pct)        out.daily_loss_halt_pct        = form.daily_loss_halt_pct
    if (form.consecutive_losses_halt)    out.consecutive_losses_halt    = Number(form.consecutive_losses_halt)
    if (form.total_float_loss_halt_pct)  out.total_float_loss_halt_pct  = form.total_float_loss_halt_pct
    if (form.btc_panic_drop_pct)         out.btc_panic_drop_pct         = form.btc_panic_drop_pct
    if (form.max_stop_pct)               out.max_stop_pct               = form.max_stop_pct
    if (form.trail_stage1_activate_pct)  out.trail_stage1_activate_pct  = form.trail_stage1_activate_pct
    if (form.trail_stage2_upgrade_pct)   out.trail_stage2_upgrade_pct   = form.trail_stage2_upgrade_pct
    if (form.trail_stage3_upgrade_pct)   out.trail_stage3_upgrade_pct   = form.trail_stage3_upgrade_pct
    if (form.trail_stage4_upgrade_pct)   out.trail_stage4_upgrade_pct   = form.trail_stage4_upgrade_pct
    if (form.note)                       out.note                       = form.note
    return out
  }

  const payload = buildPayload()
  const fieldCount = Object.keys(payload).filter(k => k !== 'note').length

  const mut = useMutation({
    mutationFn: () => updateCBThresholds(payload),
    onSuccess: () => {
      setConfirmOpen(false)
      setForm({
        daily_loss_halt_pct: '', consecutive_losses_halt: '',
        total_float_loss_halt_pct: '', btc_panic_drop_pct: '', max_stop_pct: '',
        trail_stage1_activate_pct: '', trail_stage2_upgrade_pct: '',
        trail_stage3_upgrade_pct: '', trail_stage4_upgrade_pct: '',
        note: '',
      })
    },
  })

  return (
    <div className="bg-[#1f1f1f] border border-[#2d2d2d] rounded-lg p-5">
      <h2 className="text-sm font-semibold text-gray-300 mb-1">熔断 + 仓位 + Trail 阈值</h2>
      <div className="text-[11px] text-gray-600 mb-3">写 admin_overrides → config_reloader 1min 内 swap。留空=不修改;baseline 由 .env 提供。</div>
      <div className="space-y-0.5">
        <FieldRow label="DAILY_LOSS_HALT_PCT" hint="日内 PnL 跌幅熔断阈值,0-1">
          <TextInput value={form.daily_loss_halt_pct} onChange={upd('daily_loss_halt_pct')} placeholder="e.g. 0.06" />
        </FieldRow>
        <FieldRow label="CONSECUTIVE_LOSSES_HALT" hint="连亏熔断阈值 (次数)">
          <TextInput value={form.consecutive_losses_halt} onChange={upd('consecutive_losses_halt')} placeholder="e.g. 8" />
        </FieldRow>
        <FieldRow label="TOTAL_FLOAT_LOSS_HALT_PCT" hint="总浮亏熔断阈值,0-1">
          <TextInput value={form.total_float_loss_halt_pct} onChange={upd('total_float_loss_halt_pct')} placeholder="e.g. 0.12" />
        </FieldRow>
        <FieldRow label="BTC_PANIC_DROP_PCT" hint="BTC 30min 跌幅熔断,0-1">
          <TextInput value={form.btc_panic_drop_pct} onChange={upd('btc_panic_drop_pct')} placeholder="e.g. 0.03" />
        </FieldRow>
        <FieldRow label="MAX_STOP_PCT" hint="ATR 止损上限,0-1">
          <TextInput value={form.max_stop_pct} onChange={upd('max_stop_pct')} placeholder="e.g. 0.12" />
        </FieldRow>
        <div className="border-t border-[#2d2d2d] mt-3 pt-3">
          <div className="text-xs text-gray-500 mb-2">Trail stage thresholds (Round 2.z, mu 真盘 owner catch):</div>
          <FieldRow label="TRAIL_STAGE1_ACTIVATE_PCT" hint="trail S1 启动盈利% (entry-time activate)">
            <TextInput value={form.trail_stage1_activate_pct} onChange={upd('trail_stage1_activate_pct')} placeholder="e.g. 0.05" />
          </FieldRow>
          <FieldRow label="TRAIL_STAGE2_UPGRADE_PCT" hint="S1→S2 升级阈值">
            <TextInput value={form.trail_stage2_upgrade_pct} onChange={upd('trail_stage2_upgrade_pct')} placeholder="e.g. 0.20" />
          </FieldRow>
          <FieldRow label="TRAIL_STAGE3_UPGRADE_PCT" hint="S2→S3 升级 (trader-managed)">
            <TextInput value={form.trail_stage3_upgrade_pct} onChange={upd('trail_stage3_upgrade_pct')} placeholder="e.g. 0.35" />
          </FieldRow>
          <FieldRow label="TRAIL_STAGE4_UPGRADE_PCT" hint="S3→S4 升级">
            <TextInput value={form.trail_stage4_upgrade_pct} onChange={upd('trail_stage4_upgrade_pct')} placeholder="e.g. 0.65" />
          </FieldRow>
        </div>
        <div className="border-t border-[#2d2d2d] mt-3 pt-3">
          <FieldRow label="备注 (audit log)">
            <TextInput value={form.note} onChange={upd('note')} placeholder="e.g. forward 评估校准 / RCA 决策" />
          </FieldRow>
        </div>
      </div>
      <div className="mt-4 flex items-center justify-between">
        <div className="text-xs text-gray-500">将修改 <span style={{ color: colors.warning }}>{fieldCount}</span> 个 key</div>
        <button
          disabled={fieldCount === 0}
          onClick={() => setConfirmOpen(true)}
          className="px-4 py-1.5 text-sm rounded font-medium disabled:opacity-40"
          style={{ background: colors.warning, color: '#000' }}
        >
          应用修改
        </button>
      </div>

      <ConfirmModal
        open={confirmOpen}
        title="确认修改 CB / 仓位 / Trail 阈值"
        tone="warning"
        confirmLabel={`提交 ${fieldCount} 项`}
        isPending={mut.isPending}
        error={mut.isError ? errorMessage(mut.error) : null}
        onCancel={() => setConfirmOpen(false)}
        onConfirm={() => mut.mutate()}
      >
        <div className="text-xs space-y-1 mb-3 font-mono bg-[#0f0f0f] p-3 rounded border border-[#3d3d3d]">
          {Object.entries(payload).filter(([k]) => k !== 'note').map(([k, v]) => (
            <div key={k}><span className="text-gray-500">{k}</span> = <span style={{ color: colors.warning }}>{String(v)}</span></div>
          ))}
        </div>
        <div className="text-xs text-gray-500">
          trader 下次 config_reloader tick (≤1min) swap → 阈值真实生效。审计 log 含 mu 操作记录。
        </div>
      </ConfirmModal>
    </div>
  )
}

// ---- 2. Signal thresholds form ----

function SignalThresholdsCard() {
  const [form, setForm] = useState({
    oi_growth_from_min_pct: '',
    square_ratio_threshold: '',
    note: '',
  })
  const [confirmOpen, setConfirmOpen] = useState(false)
  const upd = (k: keyof typeof form) => (v: string) => setForm(s => ({ ...s, [k]: v }))

  const payload: SignalThresholdsRequest = {}
  if (form.oi_growth_from_min_pct) payload.oi_growth_from_min_pct = form.oi_growth_from_min_pct
  if (form.square_ratio_threshold) payload.square_ratio_threshold = form.square_ratio_threshold
  if (form.note)                   payload.note                   = form.note
  const fieldCount = Object.keys(payload).filter(k => k !== 'note').length

  const mut = useMutation({
    mutationFn: () => updateSignalThresholds(payload),
    onSuccess: () => {
      setConfirmOpen(false)
      setForm({ oi_growth_from_min_pct: '', square_ratio_threshold: '', note: '' })
    },
  })

  return (
    <div className="bg-[#1f1f1f] border border-[#2d2d2d] rounded-lg p-5">
      <h2 className="text-sm font-semibold text-gray-300 mb-1">信号阈值</h2>
      <div className="text-[11px] text-gray-600 mb-3">signal_engine 调阈值,1min 内生效。OI/Square 触发更紧或更松。</div>
      <FieldRow label="OI_GROWTH_FROM_MIN_PCT" hint="OI 暴涨阈值 (vs 窗口最小值)">
        <TextInput value={form.oi_growth_from_min_pct} onChange={upd('oi_growth_from_min_pct')} placeholder="e.g. 0.06" />
      </FieldRow>
      <FieldRow label="SQUARE_HOT_MULTIPLIER" hint="Square 热点 ratio 阈值,所有 3 mode">
        <TextInput value={form.square_ratio_threshold} onChange={upd('square_ratio_threshold')} placeholder="e.g. 2.0" />
      </FieldRow>
      <FieldRow label="备注">
        <TextInput value={form.note} onChange={upd('note')} placeholder="e.g. 信号收紧 — 减少假阳性" />
      </FieldRow>
      <div className="mt-4 flex items-center justify-between">
        <div className="text-xs text-gray-500">将修改 <span style={{ color: colors.warning }}>{fieldCount}</span> 个 key</div>
        <button
          disabled={fieldCount === 0}
          onClick={() => setConfirmOpen(true)}
          className="px-4 py-1.5 text-sm rounded font-medium disabled:opacity-40"
          style={{ background: colors.warning, color: '#000' }}
        >
          应用修改
        </button>
      </div>

      <ConfirmModal
        open={confirmOpen}
        title="确认修改信号阈值"
        tone="warning"
        confirmLabel={`提交 ${fieldCount} 项`}
        isPending={mut.isPending}
        error={mut.isError ? errorMessage(mut.error) : null}
        onCancel={() => setConfirmOpen(false)}
        onConfirm={() => mut.mutate()}
      >
        <div className="text-xs space-y-1 mb-3 font-mono bg-[#0f0f0f] p-3 rounded border border-[#3d3d3d]">
          {Object.entries(payload).filter(([k]) => k !== 'note').map(([k, v]) => (
            <div key={k}><span className="text-gray-500">{k}</span> = <span style={{ color: colors.warning }}>{String(v)}</span></div>
          ))}
        </div>
      </ConfirmModal>
    </div>
  )
}

// ---- 3. Watchlist include/exclude ----

function WatchlistCard() {
  const [symbol, setSymbol] = useState('')
  const [reason, setReason] = useState('')
  const [pendingAction, setPendingAction] = useState<null | 'include' | 'exclude'>(null)

  const includeMut = useMutation({
    mutationFn: (s: string) => watchlistInclude(s, reason),
    onSuccess: () => { setSymbol(''); setReason(''); setPendingAction(null) },
  })
  const excludeMut = useMutation({
    mutationFn: (s: string) => watchlistExclude(s, reason),
    onSuccess: () => { setSymbol(''); setReason(''); setPendingAction(null) },
  })

  const activeMut = pendingAction === 'include' ? includeMut : excludeMut
  const isOpen = pendingAction !== null
  const canSubmit = symbol.trim() !== '' && reason.trim() !== ''

  return (
    <div className="bg-[#1f1f1f] border border-[#2d2d2d] rounded-lg p-5">
      <h2 className="text-sm font-semibold text-gray-300 mb-1">Watchlist include/exclude</h2>
      <div className="text-[11px] text-gray-600 mb-3">override 默认 watchlist 评估。trader watchlist_collector 下次 cron tick (≤1h) 应用。</div>
      <FieldRow label="Symbol (大写)" hint="e.g. BTCUSDT / SAPIENUSDT">
        <TextInput value={symbol} onChange={v => setSymbol(v.toUpperCase())} placeholder="SYMBOLUSDT" />
      </FieldRow>
      <FieldRow label="原因 (必填)" hint="audit log + watchlist_overrides.reason">
        <TextInput value={reason} onChange={setReason} placeholder="e.g. mu watch / 低流动性排除" />
      </FieldRow>
      <div className="mt-4 flex gap-2 justify-end">
        <button
          disabled={!canSubmit}
          onClick={() => setPendingAction('exclude')}
          className="px-4 py-1.5 text-sm rounded font-medium disabled:opacity-40"
          style={{ background: colors.halt + '22', color: colors.halt, border: `1px solid ${colors.halt}66` }}
        >
          ✗ 排除
        </button>
        <button
          disabled={!canSubmit}
          onClick={() => setPendingAction('include')}
          className="px-4 py-1.5 text-sm rounded font-medium disabled:opacity-40"
          style={{ background: colors.normal + '22', color: colors.normal, border: `1px solid ${colors.normal}66` }}
        >
          ✓ 加入
        </button>
      </div>

      <ConfirmModal
        open={isOpen}
        title={pendingAction === 'include' ? '加入 watchlist' : '排除 watchlist'}
        tone={pendingAction === 'exclude' ? 'danger' : 'primary'}
        confirmLabel="确认"
        isPending={activeMut.isPending}
        error={activeMut.isError ? errorMessage(activeMut.error) : null}
        onCancel={() => setPendingAction(null)}
        onConfirm={() => activeMut.mutate(symbol)}
      >
        <div className="text-xs space-y-1 mb-3 font-mono bg-[#0f0f0f] p-3 rounded border border-[#3d3d3d]">
          <div><span className="text-gray-500">symbol:</span> {symbol}</div>
          <div><span className="text-gray-500">action:</span> {pendingAction}</div>
          <div><span className="text-gray-500">reason:</span> {reason}</div>
        </div>
      </ConfirmModal>
    </div>
  )
}

// ---- Page composition ----

export default function Settings() {
  return (
    <div className="p-4 sm:p-6 space-y-4 sm:space-y-5 max-w-4xl">
      <div>
        <h1 className="text-lg font-bold text-white">⚙️ 设置 (Phase 5.2 Round 3)</h1>
        <p className="text-xs text-gray-500 mt-1">
          12 wired runtime keys hot-reload via config_reloader 1min cron。所有修改 audit log 公开可查。
        </p>
      </div>
      <CBThresholdsCard />
      <SignalThresholdsCard />
      <WatchlistCard />
    </div>
  )
}
