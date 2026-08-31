import { useSearchParams } from 'react-router'
import Alert from '@mui/material/Alert'
import Box from '@mui/material/Box'
import LinearProgress from '@mui/material/LinearProgress'
import MenuItem from '@mui/material/MenuItem'
import Paper from '@mui/material/Paper'
import Stack from '@mui/material/Stack'
import Table from '@mui/material/Table'
import TableBody from '@mui/material/TableBody'
import TableCell from '@mui/material/TableCell'
import TableContainer from '@mui/material/TableContainer'
import TableHead from '@mui/material/TableHead'
import TableRow from '@mui/material/TableRow'
import TextField from '@mui/material/TextField'
import Typography from '@mui/material/Typography'
import { fetchOrders } from '../../shared/api/client'
import type { Order } from '../../shared/api/client'
import { useResource } from '../../shared/api/useResource'
import { PageHeader } from '../../shared/ui/PageHeader'
import { useUser } from '../../shared/user/user-context'

const STATUSES = ['PLACED', 'CONFIRMED', 'SHIPPED', 'DELIVERED', 'CANCELLED']

export function OrderPage() {
  const { current, error: userError } = useUser()
  // フィルタは URL に置く。LLM が返した画面の状態をそのまま反映でき、
  // URL を共有すれば同じ絞り込みを再現できる。
  const [params, setParams] = useSearchParams()
  const status = params.get('status') ?? ''
  const customerName = params.get('customer_name') ?? ''
  const customerId = params.get('customer_id') ?? ''

  const setFilter = (key: string, value: string) => {
    const next = new URLSearchParams(params)
    if (value) next.set(key, value)
    else next.delete(key)
    setParams(next, { replace: true })
  }

  const userId = current?.userId ?? ''
  const { items, error, loading } = useResource<Order>(
    `${userId}|${status}|${customerName}|${customerId}`,
    () =>
      userId
        ? fetchOrders(userId, {
            status,
            customer_name: customerName,
            customer_id: customerId,
          })
        : Promise.resolve({ items: [] }),
  )

  return (
    <Box>
      <PageHeader
        title="注文"
        description="表示される範囲は実行ユーザーの権限に従います。"
      />
      <Paper variant="outlined" sx={{ p: 2, mb: 2 }}>
        <Stack direction="row" spacing={2} sx={{ flexWrap: 'wrap', gap: 2 }}>
          <TextField
            select
            size="small"
            label="状態"
            value={status}
            onChange={(e) => setFilter('status', e.target.value)}
            sx={{ minWidth: 180 }}
          >
            <MenuItem value="">すべて</MenuItem>
            {STATUSES.map((s) => (
              <MenuItem key={s} value={s}>
                {s}
              </MenuItem>
            ))}
          </TextField>
          <TextField
            size="small"
            label="顧客名"
            placeholder="田中"
            value={customerName}
            onChange={(e) => setFilter('customer_name', e.target.value)}
            sx={{ minWidth: 180 }}
          />
          <TextField
            size="small"
            label="顧客ID"
            placeholder="C001"
            value={customerId}
            onChange={(e) => setFilter('customer_id', e.target.value)}
            sx={{ minWidth: 180 }}
          />
        </Stack>
      </Paper>

      {(userError || error) && (
        <Alert severity={userError ? 'error' : 'warning'} sx={{ mb: 2 }}>
          {userError || error}
        </Alert>
      )}

      <Paper variant="outlined">
        {loading && <LinearProgress />}
        <TableContainer>
          <Table size="small">
            <TableHead>
              <TableRow>
                <TableCell>注文ID</TableCell>
                <TableCell>顧客</TableCell>
                <TableCell>状態</TableCell>
                <TableCell>受注日</TableCell>
                <TableCell align="right">金額</TableCell>
              </TableRow>
            </TableHead>
            <TableBody>
              {items.map((o) => (
                <TableRow key={o.orderId} hover>
                  <TableCell sx={{ fontFamily: 'monospace' }}>{o.orderId}</TableCell>
                  <TableCell>
                    {o.customerName || o.customerId}
                    <Typography variant="caption" color="text.secondary" sx={{ ml: 1 }}>
                      {o.customerId}
                    </Typography>
                  </TableCell>
                  <TableCell>{o.status}</TableCell>
                  <TableCell>{o.orderedAt}</TableCell>
                  <TableCell align="right">{o.totalAmount.toLocaleString()} 円</TableCell>
                </TableRow>
              ))}
              {!loading && items.length === 0 && !error && (
                <TableRow>
                  <TableCell colSpan={5}>
                    <Typography variant="body2" color="text.secondary" sx={{ py: 2 }}>
                      該当する注文はありません。
                    </Typography>
                  </TableCell>
                </TableRow>
              )}
            </TableBody>
          </Table>
        </TableContainer>
      </Paper>
    </Box>
  )
}
