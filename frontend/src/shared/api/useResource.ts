import { useEffect, useState } from 'react'

type Loaded<T> = {
  key: string
  items: T[]
  count: number
  hasMore: boolean
  error: string
}

/**
 * 一覧の読み込み。
 *
 * count は該当総件数であって items.length ではない。サービスが 1 ページ
 * (既定 100 件) しか返さないので、両者を混同すると画面が「100 件しかない」と
 * 嘘をつくことになる。
 *
 * loading を state に持たず「読み込み済みの key が現在の key と違うか」で
 * 導出する。effect の中で同期的に setState しないので、余計な再レンダリングが
 * 連鎖しない。key が変わる前の応答は破棄するため、フィルタを速く切り替えても
 * 古い結果で上書きされない。
 */
export function useResource<T>(
  key: string,
  load: () => Promise<{ items: T[]; count?: number; hasMore?: boolean }>,
): { items: T[]; count: number; hasMore: boolean; error: string; loading: boolean } {
  const [loaded, setLoaded] = useState<Loaded<T> | null>(null)

  useEffect(() => {
    let cancelled = false
    load()
      .then((r) => {
        if (!cancelled) {
          setLoaded({
            key,
            items: r.items,
            count: r.count ?? r.items.length,
            hasMore: r.hasMore ?? false,
            error: '',
          })
        }
      })
      .catch((e: unknown) => {
        if (!cancelled) {
          setLoaded({
            key,
            items: [],
            count: 0,
            hasMore: false,
            error: e instanceof Error ? e.message : String(e),
          })
        }
      })
    return () => {
      cancelled = true
    }
    // load は key から一意に決まるので、依存は key だけでよい。
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [key])

  return {
    items: loaded?.items ?? [],
    count: loaded?.count ?? 0,
    hasMore: loaded?.hasMore ?? false,
    error: loaded?.error ?? '',
    loading: loaded?.key !== key,
  }
}
