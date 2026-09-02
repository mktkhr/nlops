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
  components: {
    MuiInputBase: {
      styleOverrides: {
        // iOS Safari は font-size が 16px 未満の入力欄にフォーカスすると
        // 勝手に拡大する。**現状は既に 16px なので、この指定は効いていない**
        // (変更前後を実測して確認済み)。不変条件として残しておく:
        // 誰かが size="small" のフォントを縮めた瞬間に拡大が復活するため。
        //
        // viewport に maximum-scale=1 を足せば拡大は止まるが、それは
        // **ピンチズームそのものを殺す**ので使わない。拡大したい人が拡大
        // できなくなるほうが害が大きい。
        input: {
          '@media (pointer: coarse)': { fontSize: 16 },
          '@media (max-width: 600px)': { fontSize: 16 },
        },
      },
    },
  },
})
