import Box from '@mui/material/Box'
import Paper from '@mui/material/Paper'
import type { ReactNode } from 'react'

/**
 * 絞り込みの並べ方。
 *
 * 各項目を固定幅で並べると 375px で右端が切れる。折り返しを許したうえで
 * 狭い画面では 1 行 1 項目まで伸ばし、広い画面では伸びすぎないよう頭を抑える。
 */
export function FilterBar({ children }: { children: ReactNode }) {
  return (
    <Paper variant="outlined" sx={{ p: 2, mb: 2 }}>
      <Box
        sx={{
          display: 'flex',
          flexWrap: 'wrap',
          gap: 2,
          '& > *': { flex: '1 1 200px', minWidth: 0, maxWidth: { sm: 240 } },
        }}
      >
        {children}
      </Box>
    </Paper>
  )
}
