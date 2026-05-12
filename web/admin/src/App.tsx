import { BrowserRouter, Routes, Route, Navigate, NavLink } from 'react-router-dom'
import Dashboard from './pages/Dashboard'
import Positions from './pages/Positions'
import History from './pages/History'
import Pnl from './pages/Pnl'
import Market from './pages/Market'
import Square from './pages/Square'
import TradeDetail from './pages/TradeDetail'
import { DataSourceProvider, useDataSource } from './context/DataSource'
import type { DataSource } from './api/client'

function DataSourceToggle() {
  const { dataSource, setDataSource } = useDataSource()
  const opts: { key: DataSource; label: string }[] = [
    { key: 'mainnet', label: '真盘' },
    { key: 'all',     label: '含测试' },
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

function AppLayout() {
  return (
    <BrowserRouter basename="/admin">
      <div className="min-h-screen bg-[#141414] text-gray-100 flex flex-col">
        {/* Top bar: data_source toggle */}
        <div className="h-8 bg-[#111] border-b border-[#2d2d2d] flex items-center px-4">
          <DataSourceToggle />
        </div>

        <div className="flex flex-1 overflow-hidden">
          <nav className="w-48 bg-[#1a1a1a] border-r border-[#2d2d2d] flex flex-col p-4 gap-1 shrink-0">
            <div className="text-base font-bold text-white mb-4 px-2">⚡ Trader Admin</div>
            {[
              { to: '/',          label: '📊 Dashboard' },
              { to: '/positions', label: '📈 当前持仓' },
              { to: '/history',   label: '📋 历史仓位' },
              { to: '/pnl',       label: '💰 PnL 分析' },
              { to: '/square',    label: '🔥 Square 热点' },
              { to: '/market',    label: '🌐 市场扫描' },
            ].map(({ to, label }) => (
              <NavLink
                key={to}
                to={to}
                end={to === '/'}
                className={({ isActive }) =>
                  `px-3 py-2 rounded text-sm transition-colors ${
                    isActive
                      ? 'bg-blue-700 text-white'
                      : 'text-gray-400 hover:text-white hover:bg-gray-800'
                  }`
                }
              >
                {label}
              </NavLink>
            ))}
          </nav>

          <main className="flex-1 overflow-auto">
            <Routes>
              <Route path="/"          element={<Dashboard />} />
              <Route path="/positions" element={<Positions />} />
              <Route path="/history"   element={<History />} />
              <Route path="/pnl"       element={<Pnl />} />
              <Route path="/square"    element={<Square />} />
              <Route path="/market"    element={<Market />} />
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
