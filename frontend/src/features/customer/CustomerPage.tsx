import { useState } from 'react'
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
import { useUser } from '../../shared/user/user-context'

export function CustomerPage() {
  const { current } = useUser()
  const [name, setName] = useState('')
  const [region, setRegion] = useState('')
  const userId = current?.userId ?? ''
  const { items, error, loading } = useResource<Customer>(
    `${userId}|${name}|${region}`,
    () =>
      userId
        ? fetchCustomers(userId, { name, region })
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
            onChange={(e) => setName(e.target.value)}
            sx={{ minWidth: 180 }}
          />
          <TextField
            select
            size="small"
            label="担当地域"
            value={region}
            onChange={(e) => setRegion(e.target.value)}
            sx={{ minWidth: 180 }}
          >
            <MenuItem value="">すべて</MenuItem>
            <MenuItem value="EAST">EAST</MenuItem>
            <MenuItem value="WEST">WEST</MenuItem>
          </TextField>
        </Stack>
      </Paper>

      {error && (
        <Alert severity="warning" sx={{ mb: 2 }}>
          {error}
        </Alert>
      )}

      <Paper variant="outlined">
        {loading && <LinearProgress />}
        <TableContainer>
          <Table size="small">
            <TableHead>
              <TableRow>
                <TableCell>顧客ID</TableCell>
                <TableCell>氏名</TableCell>
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
      </Paper>
    </Box>
  )
}
