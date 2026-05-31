import { BrowserRouter, Routes, Route, Navigate, NavLink, Link } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { fetchPriceMarks } from './api/client'
import Dashboard from './pages/Dashboard'
import Positions from './pages/Positions'
import History from './pages/History'
import Pnl from './pages/Pnl'
import Market from './pages/Market'
import Square from './pages/Square'
import TradeDetail from './pages/TradeDetail'
import Settings from './pages/Settings'
import AuditLog from './pages/AuditLog'
import Mapping from './pages/Mapping'
import PriceMarks from './pages/PriceMarks'
import { DataSourceProvider, useDataSource } from './context/DataSource'
import type { DataSource } from './api/client'

function DataSourceToggle() {
  const { dataSource, setDataSource } = useDataSource()
  const opts: { key: DataSource; label: string }[] = [
    { key: 'testnet', label: '测试网' },
    { key: 'mainnet', label: '真盘' },
    { key: 'all',     label: '全部' },
  ]
  return (
    <div className="flex items-center gap-1 ml-auto mr-3">
      <span className="text-xs text-gray-600 mr-1">数据源:</span>
      {opts.map(o => (
        <button
          key={o.key}
          onClick={() => setDataSource(o.key)}
          className={`px-2 py-0.5 text-xs rounded ${
            dataSource === o.key
              ? 'bg-blue-700 text-white'
              : 'bg-[#252525] text-gray-500 hover:text-white'
          }`}
        >
          {o.label}
        </button>
      ))}
    </div>
  )
}

const NAV_LINKS = [
  { to: '/',          label: '📊 Dashboard',    short: '📊' },
  { to: '/positions', label: '📈 当前持仓',     short: '📈' },
  { to: '/history',   label: '📋 历史仓位',     short: '📋' },
  { to: '/pnl',       label: '💰 PnL 分析',     short: '💰' },
  { to: '/square',    label: '🔥 Square 热点',  short: '🔥' },
  { to: '/market',    label: '🌐 市场扫描',     short: '🌐' },
  { to: '/mapping',   label: '🔗 符号映射',     short: '🔗' },
  { to: '/marks',     label: '🔔 价格标记',     short: '🔔' },
  { to: '/settings',  label: '⚙️ 设置',         short: '⚙️' },
  { to: '/audit',     label: '📋 操作历史',     short: '🧾' },
]

// R.14d: 全站价格标记触发横幅. 轮询 unacked_triggered, >0 时全站顶部红条提示,
// 点击跳 /marks 确认。30s 刷新 (与 collector */1 节奏匹配)。
function TriggeredBanner() {
  const { data } = useQuery({ queryKey: ['price-marks'], queryFn: fetchPriceMarks, refetchInterval: 30_000 })
  const n = data?.unacked_triggered ?? 0
  if (n === 0) return null
  return (
    <Link to="/marks"
      className="block bg-red-700 hover:bg-red-600 text-white text-sm font-semibold text-center py-1.5 animate-pulse">
      🔔 {n} 个价格标记已触发 — 点击查看并确认 →
    </Link>
  )
}

function AppLayout() {
  return (
    <BrowserRouter basename="/admin">
      <div className="min-h-screen bg-[#141414] text-gray-100 flex flex-col">
        <TriggeredBanner />
        {/* Top bar: data_source toggle */}
        <div className="h-8 bg-[#111] border-b border-[#2d2d2d] flex items-center px-3 sm:px-4">
          <DataSourceToggle />
        </div>

        <div className="flex flex-1 overflow-hidden flex-col md:flex-row">
          {/* Desktop sidebar — md+ only */}
          <nav className="hidden md:flex w-48 bg-[#1a1a1a] border-r border-[#2d2d2d] flex-col p-4 gap-1 shrink-0">
            <div className="text-base font-bold text-white mb-4 px-2">⚡ Trader Admin</div>
            {NAV_LINKS.map(({ to, label }) => (
              <NavLink
                key={to} to={to} end={to === '/'}
                className={({ isActive }) =>
                  `px-3 py-2 rounded text-sm transition-colors ${
                    isActive ? 'bg-blue-700 text-white' : 'text-gray-400 hover:text-white hover:bg-gray-800'
                  }`
                }
              >
                {label}
              </NavLink>
            ))}
          </nav>

          {/* Mobile top nav strip — md hidden. Scrollable so 8 items fit on phones. */}
          <nav className="md:hidden border-b border-[#2d2d2d] bg-[#1a1a1a] overflow-x-auto whitespace-nowrap">
            <div className="flex gap-1 px-2 py-2">
              {NAV_LINKS.map(({ to, label, short }) => (
                <NavLink
                  key={to} to={to} end={to === '/'}
                  className={({ isActive }) =>
                    `inline-flex px-3 py-2 rounded text-xs min-h-[40px] items-center ${
                      isActive ? 'bg-blue-700 text-white' : 'text-gray-400 bg-[#252525]'
                    }`
                  }
                  title={label}
                >
                  <span className="mr-1">{short}</span>
                  <span>{label.replace(/^[^\s]+\s/, '')}</span>
                </NavLink>
              ))}
            </div>
          </nav>

          <main className="flex-1 overflow-auto">
            <Routes>
              <Route path="/"          element={<Dashboard />} />
              <Route path="/positions" element={<Positions />} />
              <Route path="/history"   element={<History />} />
              <Route path="/pnl"       element={<Pnl />} />
              <Route path="/square"    element={<Square />} />
              <Route path="/market"    element={<Market />} />
              <Route path="/mapping"   element={<Mapping />} />
              <Route path="/marks"     element={<PriceMarks />} />
              <Route path="/settings"  element={<Settings />} />
              <Route path="/audit"     element={<AuditLog />} />
              <Route path="/trade/:id" element={<TradeDetail />} />
              <Route path="*"          element={<Navigate to="/" replace />} />
            </Routes>
          </main>
        </div>
      </div>
    </BrowserRouter>
  )
}

export default function App() {
  return (
    <DataSourceProvider>
      <AppLayout />
    </DataSourceProvider>
  )
}
