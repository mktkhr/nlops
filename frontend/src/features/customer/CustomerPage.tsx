import { useSearchParams } from 'react-router'
import Alert from '@mui/material/Alert'
import Box from '@mui/material/Box'
import Chip from '@mui/material/Chip'
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
import { fetchCustomers } from '../../shared/api/client'
import type { Customer } from '../../shared/api/client'
import { useResource } from '../../shared/api/useResource'
import { PageHeader } from '../../shared/ui/PageHeader'
import { Pager } from '../../shared/ui/Pager'
import { SortableCell } from '../../shared/ui/SortableCell'
import { usePaging } from '../../shared/ui/usePaging'
import { useSorting } from '../../shared/ui/useSorting'
import type { SortMap } from '../../shared/ui/useSorting'
import { useUser } from '../../shared/user/user-context'

// サービスが受け付ける値をそのまま使う。
const SORTS: SortMap = {
  customerId: { asc: 'customer_id_asc', desc: 'customer_id_desc' },
  name: { asc: 'name_asc', desc: 'name_desc' },
}

export function CustomerPage() {
  const { current, error: userError } = useUser()
  // フィルタは URL に置く (OrderPage と同じ理由)。
  const [params, setParams] = useSearchParams()
  const name = params.get('name') ?? ''
  const region = params.get('region') ?? ''

  const { limit, offset, setPage, resetOffset } = usePaging()
  const { sort, dirOf, toggle } = useSorting(SORTS)

  // 絞り込みを変えたらページ位置を落とす (OrderPage と同じ理由)。
  const setFilter = (key: string, value: string) => {
    const next = new URLSearchParams(params)
    if (value) next.set(key, value)
    else next.delete(key)
    setParams(resetOffset(next), { replace: true })
  }

  const userId = current?.userId ?? ''
  const { items, count, error, loading } = useResource<Customer>(
    `${userId}|${name}|${region}|${limit}|${offset}|${sort}`,
    () =>
      userId
        ? fetchCustomers(userId, { name, region, limit, offset, sort })
        : Promise.resolve({ items: [] }),
  )

  return (
    <Box>
      <PageHeader
        title="顧客"
        description="同じ条件でもユーザーを切り替えると見える範囲が変わります。"
      />
      <Paper variant="outlined" sx={{ p: 2, mb: 2 }}>
        <Stack direction="row" spacing={2} sx={{ flexWrap: 'wrap', gap: 2 }}>
          <TextField
            size="small"
            label="氏名"
            placeholder="田中"
            value={name}
            onChange={(e) => setFilter('name', e.target.value)}
            sx={{ minWidth: 180 }}
          />
          <TextField
            select
            size="small"
            label="担当地域"
            value={region}
            onChange={(e) => setFilter('region', e.target.value)}
            sx={{ minWidth: 180 }}
          >
            <MenuItem value="">すべて</MenuItem>
            <MenuItem value="EAST">EAST</MenuItem>
            <MenuItem value="WEST">WEST</MenuItem>
          </TextField>
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
                <SortableCell col="customerId" dir={dirOf('customerId')} onToggle={toggle}>
                  顧客ID
                </SortableCell>
                <SortableCell col="name" dir={dirOf('name')} onToggle={toggle}>
                  氏名
                </SortableCell>
                <TableCell>担当地域</TableCell>
                <TableCell>状態</TableCell>
              </TableRow>
            </TableHead>
            <TableBody>
              {items.map((c) => (
                <TableRow key={c.customerId} hover>
                  <TableCell sx={{ fontFamily: 'monospace' }}>{c.customerId}</TableCell>
                  <TableCell>{c.name}</TableCell>
                  <TableCell>{c.region}</TableCell>
                  <TableCell>
                    <Chip
                      size="small"
                      label={c.status}
                      color={c.status === 'ACTIVE' ? 'success' : 'default'}
                      variant="outlined"
                    />
                  </TableCell>
                </TableRow>
              ))}
              {!loading && items.length === 0 && !error && (
                <TableRow>
                  <TableCell colSpan={4}>
                    <Typography variant="body2" color="text.secondary" sx={{ py: 2 }}>
                      該当する顧客はありません。
                    </Typography>
                  </TableCell>
                </TableRow>
              )}
            </TableBody>
          </Table>
        </TableContainer>
        <Pager count={count} limit={limit} offset={offset} onChange={setPage} />
      </Paper>
    </Box>
  )
}
