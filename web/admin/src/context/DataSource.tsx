import { createContext, useState, useContext } from 'react'
import type { DataSource } from '../api/client'

interface DataSourceCtx {
  dataSource: DataSource
  setDataSource: (ds: DataSource) => void
}

export const DataSourceContext = createContext<DataSourceCtx>({
  dataSource: 'mainnet',
  setDataSource: () => {},
})

export function DataSourceProvider({ children }: { children: React.ReactNode }) {
  const stored = (localStorage.getItem('data_source') ?? 'mainnet') as DataSource
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
