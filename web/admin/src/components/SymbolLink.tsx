import { useQuery } from '@tanstack/react-query'
import type { ReactNode, CSSProperties } from 'react'
import { fetchAlphaSymbols } from '../api/client'

// Binance USDⓈ-M perpetual contract page. /en/ prefix is locale-stable —
// browser auto-redirects to the user's preferred locale if different.
export function binanceFuturesUrl(symbol: string) {
  return `https://www.binance.com/en/futures/${symbol}`
}

// Shared React Query hook — single HTTP fetch dedupes across every
// SymbolLink in the tree. Refetch every 30min (Alpha list changes slowly).
function useAlphaSymbolSet() {
  return useQuery({
    queryKey: ['alpha-symbols'],
    queryFn: fetchAlphaSymbols,
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
      href="https://www.binance.com/en/alpha/landing"
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

// SymbolLink wraps a token symbol so a click opens the Binance futures page
// in a new tab. e.stopPropagation prevents row-click handlers (e.g. sidebar
// open / select) from also firing — clicking the symbol does ONE thing.
// Automatically appends an α badge if the symbol is on Binance Alpha.
export default function SymbolLink({
  symbol,
  className = '',
  style,
  children,
  showAlpha = true,
}: {
  symbol: string
  className?: string
  style?: CSSProperties
  children?: ReactNode
  showAlpha?: boolean
}) {
  const { data: alphaSet } = useAlphaSymbolSet()
  const isAlpha = showAlpha && !!alphaSet?.has(symbol)
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
      {isAlpha && <AlphaBadge />}
    </span>
  )
}
