import { BrowserRouter, Routes, Route, Navigate, NavLink } from 'react-router-dom'
import Dashboard from './pages/Dashboard'
import Positions from './pages/Positions'

function Stub({ label }: { label: string }) {
  return (
    <div className="p-8 text-gray-500 text-lg">{label} — 待 Round 3-6 实施</div>
  )
}

export default function App() {
  return (
    <BrowserRouter basename="/admin">
      <div className="min-h-screen bg-[#141414] text-gray-100 flex">
        <nav className="w-48 bg-[#1a1a1a] border-r border-[#2d2d2d] flex flex-col p-4 gap-1 shrink-0">
          <div className="text-base font-bold text-white mb-4 px-2">⚡ Trader Admin</div>
          {[
            { to: '/', label: '📊 Dashboard' },
            { to: '/positions', label: '📈 当前持仓' },
            { to: '/history', label: '📋 历史仓位' },
            { to: '/pnl', label: '💰 PnL 分析' },
            { to: '/square', label: '🔥 Square 热点' },
            { to: '/watchlist', label: '👀 候选池 OI' },
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
            <Route path="/" element={<Dashboard />} />
            <Route path="/positions" element={<Positions />} />
            <Route path="/history" element={<Stub label="历史仓位" />} />
            <Route path="/pnl" element={<Stub label="PnL 分析" />} />
            <Route path="/square" element={<Stub label="Square 热点" />} />
            <Route path="/watchlist" element={<Stub label="候选池 OI" />} />
            <Route path="/trade/:id" element={<Stub label="开仓决策详情" />} />
            <Route path="*" element={<Navigate to="/" replace />} />
          </Routes>
        </main>
      </div>
    </BrowserRouter>
  )
}
