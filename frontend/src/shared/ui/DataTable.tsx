import Box from '@mui/material/Box'
import LinearProgress from '@mui/material/LinearProgress'
import Paper from '@mui/material/Paper'
import Table from '@mui/material/Table'
import TableContainer from '@mui/material/TableContainer'
import type { ReactNode } from 'react'

/**
 * 一覧表の器。
 *
 * 表には最小幅を持たせて、狭い画面でも列を潰さず表の中だけを横に流す。
 * 最小幅が無いと 375px では 1 文字ずつ折り返され、注文 ID すら読めない。
 * 横スクロールは TableContainer の内側に閉じるので body には出ない。
 */
export function DataTable({
  minWidth,
  loading,
  head,
  children,
  footer,
}: {
  minWidth: number
  loading?: boolean
  head: ReactNode
  children: ReactNode
  footer?: ReactNode
}) {
  return (
    <Paper variant="outlined" sx={{ overflow: 'hidden' }}>
      {/* 読み込みの線が出入りするたびに表全体が 2px 上下すると落ち着かない。
          場所だけは常に空けておく。 */}
      {loading ? <LinearProgress sx={{ height: 2 }} /> : <Box sx={{ height: 2 }} />}
      {/*
        横に流れていることが分かるよう、左右の端に影を出す。
        背景と同色の板を local、影を scroll で貼ると、スクロールできる側の
        端にだけ影が残る。JS を挟まず、はみ出していないときは何も出ない。
      */}
      <TableContainer
        sx={(t) => {
          const paper = t.palette.background.paper
          const clear = 'rgba(255,255,255,0)'
          return {
            backgroundImage: `linear-gradient(to right, ${paper} 40%, ${clear}),
              linear-gradient(to left, ${paper} 40%, ${clear}),
              linear-gradient(to right, rgba(0,0,0,0.12), ${clear}),
              linear-gradient(to left, rgba(0,0,0,0.12), ${clear})`,
            backgroundPosition: 'left center, right center, left center, right center',
            backgroundRepeat: 'no-repeat',
            backgroundSize: '32px 100%, 32px 100%, 10px 100%, 10px 100%',
            backgroundAttachment: 'local, local, scroll, scroll',
          }
        }}
      >
        <Table size="small" sx={{ minWidth }}>
          {head}
          {children}
        </Table>
      </TableContainer>
      {footer}
    </Paper>
  )
}
