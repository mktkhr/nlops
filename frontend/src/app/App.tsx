import AppBar from '@mui/material/AppBar'
import Box from '@mui/material/Box'
import Container from '@mui/material/Container'
import CssBaseline from '@mui/material/CssBaseline'
import MenuItem from '@mui/material/MenuItem'
import Tab from '@mui/material/Tab'
import Tabs from '@mui/material/Tabs'
import TextField from '@mui/material/TextField'
import Toolbar from '@mui/material/Toolbar'
import Typography from '@mui/material/Typography'
import { ThemeProvider } from '@mui/material/styles'
import { NavLink, Navigate, Route, Routes, useLocation } from 'react-router'
import { AssistantPage } from '../features/assistant/AssistantPage'
import { CustomerPage } from '../features/customer/CustomerPage'
import { AuditPage } from '../features/audit/AuditPage'
import { OrderPage } from '../features/order/OrderPage'
import { UserProvider } from '../shared/user/UserProvider'
import { useUser } from '../shared/user/user-context'
import { theme } from './theme'

const NAV = [
  { to: '/assistant', label: 'アシスタント' },
  { to: '/orders', label: '注文' },
  { to: '/customers', label: '顧客' },
  { to: '/audit', label: '監査ログ' },
]

export default function App() {
  return (
    <ThemeProvider theme={theme}>
      <CssBaseline />
      <UserProvider>
        <Shell />
      </UserProvider>
    </ThemeProvider>
  )
}

function Shell() {
  const { pathname } = useLocation()
  const active = NAV.find((n) => pathname.startsWith(n.to))?.to ?? NAV[0].to

  return (
    <Box sx={{ minHeight: '100vh' }}>
      <AppBar position="static" color="default" elevation={0} sx={{ borderBottom: 1, borderColor: 'divider' }}>
        <Toolbar sx={{ gap: 3 }}>
          <Typography variant="h6" sx={{ fontWeight: 700, letterSpacing: 0.5 }}>
            nlops
          </Typography>
          <Tabs value={active} sx={{ flexGrow: 1, minHeight: 48 }}>
            {NAV.map((n) => (
              <Tab key={n.to} value={n.to} label={n.label} component={NavLink} to={n.to} />
            ))}
          </Tabs>
          <UserSwitcher />
        </Toolbar>
      </AppBar>

      <Container maxWidth="lg" sx={{ py: 3 }}>
        <Routes>
          <Route path="/" element={<Navigate to="/assistant" replace />} />
          <Route path="/assistant" element={<AssistantPage />} />
          <Route path="/orders" element={<OrderPage />} />
          <Route path="/customers" element={<CustomerPage />} />
          <Route path="/audit" element={<AuditPage />} />
          <Route path="*" element={<Navigate to="/assistant" replace />} />
        </Routes>
      </Container>
    </Box>
  )
}

/** 権限差を確かめるためのユーザー切り替え。PoC 用であり認証ではない。 */
function UserSwitcher() {
  const { users, current, setCurrent } = useUser()
  if (users.length === 0) return null
  return (
    <TextField
      select
      size="small"
      label="実行ユーザー"
      value={current?.userId ?? ''}
      onChange={(e) => setCurrent(e.target.value)}
      sx={{ minWidth: 200 }}
    >
      {users.map((u) => (
        <MenuItem key={u.userId} value={u.userId}>
          {u.name}
          <Typography variant="caption" color="text.secondary" sx={{ ml: 1 }}>
            {u.role}
            {u.region ? ` / ${u.region}` : ''}
          </Typography>
        </MenuItem>
      ))}
    </TextField>
  )
}
