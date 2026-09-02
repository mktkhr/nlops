import { useState } from 'react'
import type { ReactNode } from 'react'
import AppBar from '@mui/material/AppBar'
import Box from '@mui/material/Box'
import Container from '@mui/material/Container'
import Divider from '@mui/material/Divider'
import Drawer from '@mui/material/Drawer'
import IconButton from '@mui/material/IconButton'
import List from '@mui/material/List'
import ListItem from '@mui/material/ListItem'
import ListItemButton from '@mui/material/ListItemButton'
import ListItemIcon from '@mui/material/ListItemIcon'
import ListItemText from '@mui/material/ListItemText'
import Toolbar from '@mui/material/Toolbar'
import Typography from '@mui/material/Typography'
import MenuIcon from '@mui/icons-material/Menu'
import { NavLink, useLocation } from 'react-router'
import { NAV } from './nav'
import { UserSwitcher } from './UserSwitcher'

const DRAWER_WIDTH = 232

/**
 * 画面の外枠。
 *
 * 以前はヘッダーの 1 行にロゴ・タブ・ユーザー切り替えを並べていたが、
 * 幅の狭い端末ではタブが潰れて到達できない画面ができていた。
 * 行き先は縦に並ぶ引き出しへ移し、広い画面では出しっぱなし、
 * 狭い画面ではハンバーガーで開く形にしている。
 */
export function Shell({ children }: { children: ReactNode }) {
  const { pathname } = useLocation()
  const [open, setOpen] = useState(false)

  return (
    <Box sx={{ display: 'flex', minHeight: '100dvh', bgcolor: 'background.default' }}>
      <AppBar
        position="fixed"
        color="default"
        elevation={0}
        // 引き出しより手前に置いて、ヘッダーを画面幅いっぱいに通す。
        // こうすると引き出しを開けたままでも実行ユーザーを切り替えられる。
        sx={{ zIndex: (t) => t.zIndex.drawer + 1, borderBottom: 1, borderColor: 'divider' }}
      >
        <Toolbar sx={{ gap: 1, px: { xs: 1.5, sm: 3 } }}>
          <IconButton
            edge="start"
            onClick={() => setOpen(true)}
            aria-label="メニューを開く"
            sx={{ display: { md: 'none' } }}
          >
            <MenuIcon />
          </IconButton>
          <Typography
            variant="h6"
            component="p"
            sx={{ fontWeight: 700, letterSpacing: 0.5, flexGrow: 1, minWidth: 0 }}
          >
            nlops
          </Typography>
          <UserSwitcher />
        </Toolbar>
      </AppBar>

      {/* 狭い画面: 覆いかぶさる引き出し。開いている間だけ場所を取る。 */}
      <Drawer
        variant="temporary"
        open={open}
        onClose={() => setOpen(false)}
        // 戻るたびに作り直すと開くのが遅れるので DOM に残す。
        ModalProps={{ keepMounted: true }}
        sx={{
          display: { xs: 'block', md: 'none' },
          '& .MuiDrawer-paper': { width: DRAWER_WIDTH, boxSizing: 'border-box' },
        }}
      >
        {/* 行き先を押したら閉じる。開いたままだと遷移先が引き出しの裏に隠れる。 */}
        <NavPanel pathname={pathname} onNavigate={() => setOpen(false)} />
      </Drawer>

      {/* 広い画面: 出しっぱなしの引き出し。 */}
      <Drawer
        variant="permanent"
        sx={{
          display: { xs: 'none', md: 'block' },
          width: DRAWER_WIDTH,
          flexShrink: 0,
          '& .MuiDrawer-paper': {
            width: DRAWER_WIDTH,
            boxSizing: 'border-box',
            borderRight: 1,
            borderColor: 'divider',
          },
        }}
        open
      >
        <NavPanel pathname={pathname} />
      </Drawer>

      {/*
        minWidth: 0 が要。これが無いと flex の子は中身 (表など) の幅まで
        広がり、body に横スクロールが出る。表は表の中だけで流したい。
      */}
      <Box component="main" sx={{ flexGrow: 1, minWidth: 0 }}>
        <Toolbar />
        <Container maxWidth="lg" sx={{ py: { xs: 2, sm: 3 } }}>
          {children}
        </Container>
      </Box>
    </Box>
  )
}

function NavPanel({ pathname, onNavigate }: { pathname: string; onNavigate?: () => void }) {
  return (
    <Box sx={{ overflowY: 'auto' }}>
      {/*
        ヘッダーは引き出しより手前に敷いてあるので、この余白が無いと
        先頭の行 (アシスタント) がヘッダーの下に潜って押せなくなる。
        まさに直したかった「ページが埋もれる」状態なので、両方の引き出しで空ける。
      */}
      <Toolbar />
      <Divider />
      <List sx={{ py: 1 }}>
        {NAV.map((n) => (
          <ListItem key={n.to} disablePadding sx={{ px: 1 }}>
            <ListItemButton
              component={NavLink}
              to={n.to}
              // 前方一致で選ぶ。/orders?status=... のように問い合わせが
              // 付いた URL でも同じ行が選ばれるようにする。
              selected={pathname.startsWith(n.to)}
              onClick={onNavigate}
              sx={{ borderRadius: 1 }}
            >
              <ListItemIcon sx={{ minWidth: 36 }}>{n.icon}</ListItemIcon>
              <ListItemText primary={n.label} slotProps={{ primary: { variant: 'body2' } }} />
            </ListItemButton>
          </ListItem>
        ))}
      </List>
    </Box>
  )
}
