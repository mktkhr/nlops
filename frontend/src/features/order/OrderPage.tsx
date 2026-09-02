import { useSearchParams } from 'react-router'
import Alert from '@mui/material/Alert'
import Box from '@mui/material/Box'
import MenuItem from '@mui/material/MenuItem'
import TableBody from '@mui/material/TableBody'
import TableCell from '@mui/material/TableCell'
import TableHead from '@mui/material/TableHead'
import TableRow from '@mui/material/TableRow'
import TextField from '@mui/material/TextField'
import Typography from '@mui/material/Typography'
import { fetchOrders } from '../../shared/api/client'
import type { Order } from '../../shared/api/client'
import { useResource } from '../../shared/api/useResource'
import { DataTable } from '../../shared/ui/DataTable'
import { FilterBar } from '../../shared/ui/FilterBar'
import { PageHeader } from '../../shared/ui/PageHeader'
import { Pager } from '../../shared/ui/Pager'
import { SortableCell } from '../../shared/ui/SortableCell'
import { usePaging } from '../../shared/ui/usePaging'
import { useSorting } from '../../shared/ui/useSorting'
import type { SortMap } from '../../shared/ui/useSorting'
import { useUser } from '../../shared/user/user-context'

const STATUSES = ['PLACED', 'CONFIRMED', 'SHIPPED', 'DELIVERED', 'CANCELLED']

// サービスが受け付ける値をそのまま使う。画面側で組み立て直さない。
const SORTS: SortMap = {
  orderedAt: { asc: 'ordered_at_asc', desc: 'ordered_at_desc' },
  totalAmount: { asc: 'total_amount_asc', desc: 'total_amount_desc' },
}

export function OrderPage() {
  const { current, error: userError } = useUser()
  // フィルタは URL に置く。LLM が返した画面の状態をそのまま反映でき、
  // URL を共有すれば同じ絞り込みを再現できる。
  const [params, setParams] = useSearchParams()
  const status = params.get('status') ?? ''
  const customerName = params.get('customer_name') ?? ''
  const customerId = params.get('customer_id') ?? ''

  const { limit, offset, setPage, resetOffset } = usePaging()
  const { sort, dirOf, toggle } = useSorting(SORTS)

  // 絞り込みを変えたらページ位置を落とす。8 ページ目のまま絞り込むと
  // 該当が 3 ページしかなくても「0 件」に見える。
  const setFilter = (key: string, value: string) => {
    const next = new URLSearchParams(params)
    if (value) next.set(key, value)
    else next.delete(key)
    setParams(resetOffset(next), { replace: true })
  }

  const userId = current?.userId ?? ''
  const { items, count, error, loading } = useResource<Order>(
    `${userId}|${status}|${customerName}|${customerId}|${limit}|${offset}|${sort}`,
    () =>
      userId
        ? fetchOrders(userId, {
            status,
            customer_name: customerName,
            customer_id: customerId,
            limit,
            offset,
            sort,
          })
        : Promise.resolve({ items: [] }),
  )

  return (
    <Box>
      <PageHeader
        title="注文"
        description="表示される範囲は実行ユーザーの権限に従います。"
      />
      <FilterBar>
        <TextField
          select
          size="small"
          label="状態"
          value={status}
          onChange={(e) => setFilter('status', e.target.value)}
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
        />
        <TextField
          size="small"
          label="顧客ID"
          placeholder="C001"
          value={customerId}
          onChange={(e) => setFilter('customer_id', e.target.value)}
        />
      </FilterBar>

      {(userError || error) && (
        <Alert severity={userError ? 'error' : 'warning'} sx={{ mb: 2 }}>
          {userError || error}
        </Alert>
      )}

      <DataTable
        minWidth={720}
        loading={loading}
        footer={<Pager count={count} limit={limit} offset={offset} onChange={setPage} />}
        head={
          <TableHead>
            <TableRow>
              <TableCell>注文ID</TableCell>
              <TableCell>顧客</TableCell>
              <TableCell>状態</TableCell>
              <SortableCell col="orderedAt" dir={dirOf('orderedAt')} onToggle={toggle}>
                受注日
              </SortableCell>
              <SortableCell
                col="totalAmount"
                dir={dirOf('totalAmount')}
                onToggle={toggle}
                align="right"
              >
                金額
              </SortableCell>
            </TableRow>
          </TableHead>
        }
      >
        <TableBody>
          {items.map((o) => (
            <TableRow key={o.orderId} hover>
              <TableCell sx={{ fontFamily: 'monospace', whiteSpace: 'nowrap' }}>
                {o.orderId}
              </TableCell>
              <TableCell>
                {o.customerName || o.customerId}
                <Typography variant="caption" color="text.secondary" sx={{ ml: 1 }}>
                  {o.customerId}
                </Typography>
              </TableCell>
              <TableCell>{o.status}</TableCell>
              <TableCell sx={{ whiteSpace: 'nowrap' }}>{o.orderedAt}</TableCell>
              <TableCell align="right" sx={{ whiteSpace: 'nowrap' }}>
                {o.totalAmount.toLocaleString()} 円
              </TableCell>
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
      </DataTable>
    </Box>
  )
}
