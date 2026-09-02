import TablePagination from '@mui/material/TablePagination'
import { PAGE_SIZES } from './usePaging'

/**
 * ページ送り。
 *
 * 件数は該当総件数を使う。表示中の行数を渡すと最終ページが分からない。
 * サービスは offset 上限 (100,000) を持つので、そこを超える範囲は
 * ページ送りではなく絞り込みで到達する想定。
 */
export function Pager({
  count,
  limit,
  offset,
  onChange,
}: {
  count: number
  limit: number
  offset: number
  onChange: (offset: number, limit: number) => void
}) {
  return (
    <TablePagination
      component="div"
      count={count}
      page={Math.floor(offset / Math.max(limit, 1))}
      rowsPerPage={limit}
      rowsPerPageOptions={PAGE_SIZES}
      onPageChange={(_, page) => onChange(page * limit, limit)}
      // 1 ページの件数を変えたら先頭へ戻す。offset を維持すると
      // 見ていた位置と噛み合わない場所へ飛ぶ。
      onRowsPerPageChange={(e) => onChange(0, Number(e.target.value))}
      labelRowsPerPage="表示件数"
      labelDisplayedRows={({ from, to, count: c }) =>
        `${c.toLocaleString()} 件中 ${from.toLocaleString()}–${to.toLocaleString()} 件`
      }
      // 既定のままだと 375px で「次へ」の矢印が枠の外へはみ出して押せない。
      // 1 行に収めることを諦めて折り返させ、詰め物も狭い画面では削る。
      sx={{
        '& .MuiTablePagination-toolbar': {
          flexWrap: 'wrap',
          justifyContent: 'flex-end',
          rowGap: 0.5,
          px: { xs: 1, sm: 2 },
        },
        '& .MuiTablePagination-spacer': { display: { xs: 'none', sm: 'block' } },
        '& .MuiTablePagination-actions': { ml: { xs: 0, sm: 2.5 } },
      }}
    />
  )
}
