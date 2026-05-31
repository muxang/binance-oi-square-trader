import { createContext, useState, useContext } from 'react'
import type { DataSource } from '../api/client'

interface DataSourceCtx {
  dataSource: DataSource
  setDataSource: (ds: DataSource) => void
}

export const DataSourceContext = createContext<DataSourceCtx>({
  dataSource: 'testnet',
  setDataSource: () => {},
})

// R.18 fix: bump version when default changes so old browsers reset their
// stale 'mainnet' localStorage. R.17 D2 backfilled all trades to testnet
// but old localStorage still says mainnet → UI shows empty history.
const STORAGE_VERSION = 'r18'

function readInitialDataSource(): DataSource {
  if (localStorage.getItem('data_source_v') !== STORAGE_VERSION) {
    localStorage.removeItem('data_source')
    localStorage.setItem('data_source_v', STORAGE_VERSION)
  }
  return (localStorage.getItem('data_source') ?? 'testnet') as DataSource
}

export function DataSourceProvider({ children }: { children: React.ReactNode }) {
  const stored = readInitialDataSource()
  const [dataSource, _setDataSource] = useState<DataSource>(stored)

  const setDataSource = (ds: DataSource) => {
    localStorage.setItem('data_source', ds)
    _setDataSource(ds)
  }

  return (
    <DataSourceContext.Provider value={{ dataSource, setDataSource }}>
      {children}
    </DataSourceContext.Provider>
  )
}

export function useDataSource() {
  return useContext(DataSourceContext)
}
