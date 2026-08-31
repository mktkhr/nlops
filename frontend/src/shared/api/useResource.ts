import { useEffect, useState } from 'react'

type Loaded<T> = { key: string; items: T[]; error: string }

/**
 * 一覧の読み込み。
 *
 * loading を state に持たず「読み込み済みの key が現在の key と違うか」で
 * 導出する。effect の中で同期的に setState しないので、余計な再レンダリングが
 * 連鎖しない。key が変わる前の応答は破棄するため、フィルタを速く切り替えても
 * 古い結果で上書きされない。
 */
export function useResource<T>(
  key: string,
  load: () => Promise<{ items: T[] }>,
): { items: T[]; error: string; loading: boolean } {
  const [loaded, setLoaded] = useState<Loaded<T> | null>(null)

  useEffect(() => {
    let cancelled = false
    load()
      .then((r) => {
        if (!cancelled) setLoaded({ key, items: r.items, error: '' })
      })
      .catch((e: unknown) => {
        if (!cancelled) {
          setLoaded({
            key,
            items: [],
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
    error: loaded?.error ?? '',
    loading: loaded?.key !== key,
  }
}
