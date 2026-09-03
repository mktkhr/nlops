import { Suspense, lazy, useEffect } from 'react'
import CssBaseline from '@mui/material/CssBaseline'
import LinearProgress from '@mui/material/LinearProgress'
import { ThemeProvider } from '@mui/material/styles'
import { Navigate, Route, Routes } from 'react-router'
import { UserProvider } from '../shared/user/UserProvider'
import { Shell } from './Shell'
import { preventFocusZoom } from './no-focus-zoom'
import { theme } from './theme'

// 画面ごとに分割して読み込む。最初に開くのはアシスタントなので、
// 注文・顧客・監査のコードを初回のバンドルへ入れる必要がない。
const AssistantPage = lazy(() =>
  import('../features/assistant/AssistantPage').then((m) => ({ default: m.AssistantPage })),
)
const OrderPage = lazy(() =>
  import('../features/order/OrderPage').then((m) => ({ default: m.OrderPage })),
)
const CustomerPage = lazy(() =>
  import('../features/customer/CustomerPage').then((m) => ({ default: m.CustomerPage })),
)
const StockPage = lazy(() =>
  import('../features/stock/StockPage').then((m) => ({ default: m.StockPage })),
)
const AuditPage = lazy(() =>
  import('../features/audit/AuditPage').then((m) => ({ default: m.AuditPage })),
)

export default function App() {
  // iOS Safari の自動拡大を、入力中だけ止める (no-focus-zoom.ts 参照)。
  useEffect(preventFocusZoom, [])

  return (
    <ThemeProvider theme={theme}>
      <CssBaseline />
      <UserProvider>
        <Shell>
          <Suspense fallback={<LinearProgress />}>
            <Routes>
              <Route path="/" element={<Navigate to="/assistant" replace />} />
              <Route path="/assistant" element={<AssistantPage />} />
              <Route path="/orders" element={<OrderPage />} />
              <Route path="/customers" element={<CustomerPage />} />
              <Route path="/stock" element={<StockPage />} />
          <Route path="/audit" element={<AuditPage />} />
              <Route path="*" element={<Navigate to="/assistant" replace />} />
            </Routes>
          </Suspense>
        </Shell>
      </UserProvider>
    </ThemeProvider>
  )
}
