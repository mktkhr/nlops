import MenuItem from '@mui/material/MenuItem'
import Skeleton from '@mui/material/Skeleton'
import TextField from '@mui/material/TextField'
import Typography from '@mui/material/Typography'
import { useUser } from '../shared/user/user-context'

/**
 * 権限差を確かめるためのユーザー切り替え。PoC 用であり認証ではない。
 *
 * この PoC の中核なので、幅が狭くても引き出しの中に隠さずヘッダーに出す。
 * ただし選択中の表示は名前だけに絞る (renderValue)。役割や地域まで閉じた
 * 状態で見せると 375px ではヘッダーが折り返して他の要素を押し出す。
 */
export function UserSwitcher() {
  const { users, current, setCurrent, loading } = useUser()

  // 読み込み中に幅 0 で描いてからいきなり広がると、ヘッダーがガタつく。
  if (loading && users.length === 0) {
    return <Skeleton variant="rounded" height={40} sx={{ width: { xs: 132, sm: 208 } }} />
  }
  if (users.length === 0) return null

  return (
    <TextField
      select
      size="small"
      label="実行ユーザー"
      value={current?.userId ?? ''}
      onChange={(e) => setCurrent(e.target.value)}
      sx={{ width: { xs: 132, sm: 208 }, flexShrink: 0 }}
      slotProps={{
        select: {
          renderValue: (value) => {
            const u = users.find((x) => x.userId === value)
            return u?.name ?? ''
          },
        },
      }}
    >
      {users.map((u) => (
        <MenuItem key={u.userId} value={u.userId}>
          {u.name}
          <Typography variant="caption" color="text.secondary" sx={{ ml: 1 }}>
            {u.role}
            {u.region ? ` / ${u.region}` : ''}
          </Typography>
        </MenuItem>
      ))}
    </TextField>
  )
}
