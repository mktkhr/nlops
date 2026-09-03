import { useEffect } from 'react'
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
import { fetchStock } from '../../shared/api/client'
import type { Stock } from '../../shared/api/client'
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

const WAREHOUSES = ['WH_TOKYO', 'WH_OSAKA']

// サービス側 (/stock/low) の既定と揃える。ここで明示しないと、
// 絞り込まれていることが画面から分からない。
const DEFAULT_AT_MOST = 10

// サービスが受け付ける値をそのまま使う。
const SORTS: SortMap = {
  quantity: { asc: 'quantity_asc', desc: 'quantity_desc' },
}

/**
 * 在庫一覧。
 *
 * この画面が無かったとき、「在庫が5個を下回っている商品を出して」は
 * Tool 経路へ落ち、モデルが 20 行を散文で書いていた (実測 11.9 秒、
 * うち回答生成が 7.8 秒)。**一覧の要求は画面で受けるほうが速い。**
 */
export function StockPage() {
  const { current, error: userError } = useUser()
  // フィルタは URL に置く (他の画面と同じ理由)。
  const [params, setParams] = useSearchParams()
  const below = params.get('below') ?? ''
  const atMost = params.get('at_most') ?? ''
  const warehouseId = params.get('warehouse_id') ?? ''
  const productName = params.get('product_name') ?? ''

  // サービスは既定でしきい値 10 を適用する (/stock/low)。
  // そのままだと「在庫一覧」と言いながら黙って絞られた結果が出る。
  // **URL に書き出して見えるようにする。** 利用者は数を変えられるし、
  // 何で絞られているかが画面にもアドレスにも残る。
  useEffect(() => {
    if (params.get('below') || params.get('at_most')) return
    const next = new URLSearchParams(params)
    next.set('at_most', String(DEFAULT_AT_MOST))
    setParams(next, { replace: true })
  }, [params, setParams])

  const { limit, offset, setPage, resetOffset } = usePaging()
  const { sort, dirOf, toggle } = useSorting(SORTS)

  const setFilter = (key: string, value: string) => {
    const next = new URLSearchParams(params)
    if (value) next.set(key, value)
    else next.delete(key)
    setParams(resetOffset(next), { replace: true })
  }

  const userId = current?.userId ?? ''
  const { items, count, error, loading } = useResource<Stock>(
    `${userId}|${below}|${atMost}|${warehouseId}|${productName}|${limit}|${offset}|${sort}`,
    () =>
      userId
        ? fetchStock(userId, {
            below,
            at_most: atMost,
            warehouse_id: warehouseId,
            product_name: productName,
            limit,
            offset,
            sort,
          })
        : Promise.resolve({ items: [] }),
  )

  return (
    <Box>
      <PageHeader
        title="在庫"
        description="倉庫ごとの在庫数です。少ない順に並びます。参照できる範囲は実行ユーザーの権限に従います。"
      />

      <FilterBar>
        <TextField
          size="small"
          label={atMost ? 'この数以下' : 'この数を下回る'}
          placeholder="5"
          value={atMost || below}
          onChange={(e) =>
            setFilter(atMost ? 'at_most' : 'below', e.target.value.replace(/[^0-9]/g, ''))
          }
        />
        <TextField
          select
          size="small"
          label="倉庫"
          value={warehouseId}
          onChange={(e) => setFilter('warehouse_id', e.target.value)}
        >
          <MenuItem value="">すべて</MenuItem>
          {WAREHOUSES.map((w) => (
            <MenuItem key={w} value={w}>
              {w}
            </MenuItem>
          ))}
        </TextField>
        <TextField
          size="small"
          label="商品名"
          placeholder="ウェブカメラ"
          value={productName}
          onChange={(e) => setFilter('product_name', e.target.value)}
        />
      </FilterBar>

      {(userError || error) && (
        <Alert severity={userError ? 'error' : 'warning'} sx={{ mb: 2 }}>
          {userError || error}
        </Alert>
      )}

      <DataTable
        minWidth={560}
        loading={loading}
        footer={<Pager count={count} limit={limit} offset={offset} onChange={setPage} />}
        head={
          <TableHead>
            <TableRow>
              <TableCell>商品ID</TableCell>
              <TableCell>商品名</TableCell>
              <TableCell>倉庫</TableCell>
              <SortableCell
                col="quantity"
                dir={dirOf('quantity')}
                onToggle={toggle}
                align="right"
              >
                在庫数
              </SortableCell>
            </TableRow>
          </TableHead>
        }
      >
        <TableBody>
          {items.map((s) => (
            <TableRow key={`${s.productId}/${s.warehouseId}`} hover>
              <TableCell sx={{ fontFamily: 'monospace', whiteSpace: 'nowrap' }}>
                {s.productId}
              </TableCell>
              <TableCell>{s.productName}</TableCell>
              <TableCell sx={{ whiteSpace: 'nowrap' }}>{s.warehouseId}</TableCell>
              <TableCell
                align="right"
                sx={{
                  whiteSpace: 'nowrap',
                  // 0 は補充が要る。数字だけだと他の行に埋もれる。
                  fontWeight: s.quantity === 0 ? 700 : undefined,
                  color: s.quantity === 0 ? 'error.main' : undefined,
                }}
              >
                {s.quantity.toLocaleString()}
              </TableCell>
            </TableRow>
          ))}
          {!loading && items.length === 0 && !error && (
            <TableRow>
              <TableCell colSpan={4}>
                <Typography variant="body2" color="text.secondary" sx={{ py: 2 }}>
                  該当する在庫はありません。
                </Typography>
              </TableCell>
            </TableRow>
          )}
        </TableBody>
      </DataTable>
    </Box>
  )
}
