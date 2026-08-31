import { createTheme } from '@mui/material/styles'

export const theme = createTheme({
  palette: {
    mode: 'light',
    background: { default: '#f7f8fa' },
    primary: { main: '#2f5d9e' },
  },
  typography: {
    fontFamily:
      '"Helvetica Neue", "Hiragino Kaku Gothic ProN", "Noto Sans JP", Meiryo, sans-serif',
  },
  shape: { borderRadius: 8 },
})
