import { useCallback, useEffect, useMemo, useState } from 'react'
import type { ReactNode } from 'react'
import { fetchUsers } from '../api/client'
import type { User } from '../api/client'
import { UserContext } from './user-context'

export function UserProvider({ children }: { children: ReactNode }) {
  const [users, setUsers] = useState<User[]>([])
  const [currentId, setCurrentId] = useState<string>('u_admin')
  // 取得失敗を握り潰さない。ユーザーが決まらないと何も実行できないので、
  // 黙って無反応になるより理由を出す。
  const [state, setState] = useState<{ error: string; loading: boolean }>({
    error: '',
    loading: true,
  })

  useEffect(() => {
    let cancelled = false
    fetchUsers()
      .then((r) => {
        if (cancelled) return
        setUsers(r.items)
        setState({
          error: r.items.length === 0 ? 'ユーザーが 1 人も定義されていません。' : '',
          loading: false,
        })
      })
      .catch((e: unknown) => {
        if (cancelled) return
        setUsers([])
        setState({
          error:
            'ユーザー一覧を取得できませんでした。BFF に到達できているか確認してください。' +
            (e instanceof Error ? ` (${e.message})` : ''),
          loading: false,
        })
      })
    return () => {
      cancelled = true
    }
  }, [])

  const setCurrent = useCallback((userId: string) => setCurrentId(userId), [])

  const value = useMemo(
    () => ({
      users,
      current: users.find((u) => u.userId === currentId) ?? null,
      setCurrent,
      error: state.error,
      loading: state.loading,
    }),
    [users, currentId, setCurrent, state],
  )

  return <UserContext value={value}>{children}</UserContext>
}
