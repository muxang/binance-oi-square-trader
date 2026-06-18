import { useQuery } from '@tanstack/react-query'
import type { ReactNode, CSSProperties } from 'react'
import { fetchAlphaSymbols, fetchStockSymbols } from '../api/client'

// Binance USDⓈ-M perpetual contract page. /zh-CN/ for native Chinese UI.
export function binanceFuturesUrl(symbol: string) {
  return `https://www.binance.com/zh-CN/futures/${symbol}`
}

// Shared React Query hooks — single HTTP fetch dedupes across every
// SymbolLink in the tree. Refetch every 30min (these lists change slowly).
function useAlphaSymbolSet() {
  return useQuery({
    queryKey: ['alpha-symbols'],
    queryFn: fetchAlphaSymbols,
    staleTime: 30 * 60 * 1000,
    refetchInterval: 30 * 60 * 1000,
    select: (d) => new Set(d.symbols),
  })
}

function useStockSymbolSet() {
  return useQuery({
    queryKey: ['stock-symbols'],
    queryFn: fetchStockSymbols,
    staleTime: 30 * 60 * 1000,
    refetchInterval: 30 * 60 * 1000,
    select: (d) => new Set(d.symbols),
  })
}

// AlphaBadge renders inline next to symbol when it's on Binance Alpha.
// Tooltip explains; click stops propagation so row-click doesn't fire.
function AlphaBadge() {
  return (
    <a
      href="https://www.binance.com/zh-CN/alpha/landing"
      target="_blank"
      rel="noopener noreferrer"
      onClick={(e) => e.stopPropagation()}
      title="此 token 在币安 Alpha 名单中 — 点击访问 Alpha 页面"
      className="ml-1 inline-flex items-center justify-center w-4 h-4 rounded text-[10px] font-bold
                 bg-gradient-to-br from-yellow-500 to-orange-600 text-black
                 hover:from-yellow-400 hover:to-orange-500 transition-colors"
    >α</a>
  )
}

// StockBadge — Binance Futures stock-backed perpetual (underlyingType=EQUITY).
// E.g. TSLAUSDT / NVDAUSDT / COINUSDT track underlying stock price 24/7 via
// Binance Futures. Distinguished visually so users don't confuse them with
// crypto perps when scanning the uptrend list.
function StockBadge({ symbol }: { symbol: string }) {
  // Strip USDT suffix for tooltip readability (TSLAUSDT → TSLA)
  const base = symbol.endsWith('USDT') ? symbol.slice(0, -4) : symbol
  return (
    <span
      title={`股票合约 (Binance Futures): ${base} 跟随股价的永续合约`}
      className="ml-1 inline-flex items-center justify-center px-1 h-4 rounded text-[10px] font-bold
                 bg-gradient-to-br from-sky-600 to-indigo-700 text-white
                 select-none"
    >📈</span>
  )
}

// SymbolLink wraps a token symbol so a click opens the Binance futures page
// in a new tab. e.stopPropagation prevents row-click handlers (e.g. sidebar
// open / select) from also firing — clicking the symbol does ONE thing.
// Automatically appends α (Alpha) and 📈 (stock-backed) badges as applicable.
export default function SymbolLink({
  symbol,
  className = '',
  style,
  children,
  showAlpha = true,
  showStock = true,
}: {
  symbol: string
  className?: string
  style?: CSSProperties
  children?: ReactNode
  showAlpha?: boolean
  showStock?: boolean
}) {
  const { data: alphaSet } = useAlphaSymbolSet()
  const { data: stockSet } = useStockSymbolSet()
  const isAlpha = showAlpha && !!alphaSet?.has(symbol)
  const isStock = showStock && !!stockSet?.has(symbol)
  return (
    <span className="inline-flex items-center">
      <a
        href={binanceFuturesUrl(symbol)}
        target="_blank"
        rel="noopener noreferrer"
        onClick={(e) => e.stopPropagation()}
        title={`在币安打开 ${symbol} 永续合约 ↗`}
        style={style}
        className={`hover:text-blue-400 hover:underline cursor-pointer ${className}`}
      >
        {children ?? symbol}
      </a>
      {isStock && <StockBadge symbol={symbol} />}
      {isAlpha && <AlphaBadge />}
    </span>
  )
}
