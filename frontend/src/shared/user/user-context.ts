import { createContext, use } from 'react'
import type { User } from '../api/client'

export type UserState = {
  users: User[]
  current: User | null
  setCurrent: (userId: string) => void
}

export const UserContext = createContext<UserState | null>(null)

/** 現在の実行ユーザー。権限差はここを切り替えて確認する。 */
export function useUser(): UserState {
  const ctx = use(UserContext)
  if (!ctx) throw new Error('UserProvider の外で useUser が呼ばれました')
  return ctx
}
