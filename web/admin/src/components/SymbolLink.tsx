import type { ReactNode, CSSProperties } from 'react'

// Binance USDⓈ-M perpetual contract page. /en/ prefix is locale-stable —
// browser auto-redirects to the user's preferred locale if different.
export function binanceFuturesUrl(symbol: string) {
  return `https://www.binance.com/en/futures/${symbol}`
}

// SymbolLink wraps a token symbol so a click opens the Binance futures page
// in a new tab. e.stopPropagation prevents row-click handlers (e.g. sidebar
// open / select) from also firing — clicking the symbol does ONE thing.
export default function SymbolLink({
  symbol,
  className = '',
  style,
  children,
}: {
  symbol: string
  className?: string
  style?: CSSProperties
  children?: ReactNode
}) {
  return (
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
  )
}
