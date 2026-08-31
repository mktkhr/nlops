import { useCallback, useEffect, useMemo, useState } from 'react'
import type { ReactNode } from 'react'
import { fetchUsers } from '../api/client'
import type { User } from '../api/client'
import { UserContext } from './user-context'

export function UserProvider({ children }: { children: ReactNode }) {
  const [users, setUsers] = useState<User[]>([])
  const [currentId, setCurrentId] = useState<string>('u_admin')

  useEffect(() => {
    fetchUsers()
      .then((r) => setUsers(r.items))
      .catch(() => setUsers([]))
  }, [])

  const setCurrent = useCallback((userId: string) => setCurrentId(userId), [])

  const value = useMemo(
    () => ({
      users,
      current: users.find((u) => u.userId === currentId) ?? null,
      setCurrent,
    }),
    [users, currentId, setCurrent],
  )

  return <UserContext value={value}>{children}</UserContext>
}
