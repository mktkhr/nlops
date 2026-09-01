import TableCell from '@mui/material/TableCell'
import TableSortLabel from '@mui/material/TableSortLabel'
import type { ReactNode } from 'react'

/**
 * 並べ替えできる列見出し。
 *
 * 並べ替えを持たない列はただの見出しにする。サービスが受け付けない列に
 * 並べ替えの UI を出すと、押しても何も起きない見出しができてしまう。
 */
export function SortableCell({
  col,
  dir,
  onToggle,
  align,
  children,
}: {
  col?: string
  dir: 'asc' | 'desc' | false
  onToggle: (col: string) => void
  align?: 'right'
  children: ReactNode
}) {
  if (!col) {
    return <TableCell align={align}>{children}</TableCell>
  }
  return (
    <TableCell align={align} sortDirection={dir}>
      <TableSortLabel
        active={dir !== false}
        direction={dir === false ? 'asc' : dir}
        onClick={() => onToggle(col)}
      >
        {children}
      </TableSortLabel>
    </TableCell>
  )
}
