import { ReactNode } from 'react'
import { Sidebar } from './Sidebar'

interface LayoutProps {
  children: ReactNode
  closeControl: ReactNode
}

export function Layout({ children, closeControl }: LayoutProps) {
  return (
    <div className="flex h-screen bg-[var(--color-bg-base)]">
      <Sidebar />
      <div className="flex min-w-0 flex-1 flex-col overflow-hidden">
        <div className="flex h-8 shrink-0 items-center justify-end px-1">
          {closeControl}
        </div>
        <main className="min-h-0 min-w-0 flex-1 overflow-auto px-4 pb-4 pt-1">
          {children}
        </main>
      </div>
    </div>
  )
}
