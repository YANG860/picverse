import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import '@fontsource-variable/outfit'
import './index.css'
import App from './App.tsx'

// 全局查询默认值：staleTime 内视为新鲜、切回标签页/窗口聚焦不自动 refetch——
// 否则静置后切回会全量重拉导致瀑布流重排（用户感知为“页面自己刷新”）
const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      staleTime: 60_000,
      gcTime: 10 * 60_000,
      refetchOnWindowFocus: false,
      retry: 1,
    },
  },
})

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <App />
    </QueryClientProvider>
  </StrictMode>,
)
