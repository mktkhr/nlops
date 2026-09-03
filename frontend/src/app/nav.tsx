import ChatBubbleOutlineIcon from '@mui/icons-material/ChatBubbleOutlineOutlined'
import HistoryIcon from '@mui/icons-material/History'
import Inventory2OutlinedIcon from '@mui/icons-material/Inventory2Outlined'
import PeopleOutlineIcon from '@mui/icons-material/PeopleOutlined'
import ReceiptLongIcon from '@mui/icons-material/ReceiptLong'
import type { ReactNode } from 'react'

export type NavItem = {
  to: string
  label: string
  icon: ReactNode
}

/**
 * 画面の一覧。
 *
 * アイコンを添えるのは飾りではなく、幅の狭い端末で引き出しを開いたときに
 * 文字を読む前に行を見分けられるようにするため。
 */
export const NAV: NavItem[] = [
  { to: '/assistant', label: 'アシスタント', icon: <ChatBubbleOutlineIcon fontSize="small" /> },
  { to: '/orders', label: '注文', icon: <ReceiptLongIcon fontSize="small" /> },
  { to: '/customers', label: '顧客', icon: <PeopleOutlineIcon fontSize="small" /> },
  { to: '/stock', label: '在庫', icon: <Inventory2OutlinedIcon fontSize="small" /> },
  { to: '/audit', label: '監査ログ', icon: <HistoryIcon fontSize="small" /> },
]
