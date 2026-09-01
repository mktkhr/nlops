import { useCallback } from 'react'
import { useSearchParams } from 'react-router'

/** 画面の列と、サービスが受け付ける sort の値の対応。 */
export type SortMap = Record<string, { asc: string; desc: string }>

/**
 * 並べ替えの状態を URL に置く。
 *
 * 値はサービスの enum をそのまま使う (ordered_at_asc など)。
 * 画面側で列と向きを別々に持つと、サービスへ渡すときに組み立て直すことになり、
 * 「向きだけ指定された」状態を画面側でも作ってしまう。
 *
 * 並べ替えを変えたらページ位置は落とす。3 ページ目のまま並べ替えると、
 * 見ていた行とは無関係な場所に飛ぶ。
 */
export function useSorting(map: SortMap) {
  const [params, setParams] = useSearchParams()
  const sort = params.get('sort') ?? ''

  const dirOf = useCallback(
    (col: string): 'asc' | 'desc' | false => {
      const m = map[col]
      if (!m) return false
      if (sort === m.asc) return 'asc'
      if (sort === m.desc) return 'desc'
      return false
    },
    [map, sort],
  )

  const toggle = useCallback(
    (col: string) => {
      const m = map[col]
      if (!m) return
      const next = new URLSearchParams(params)
      // 未指定 → 昇順 → 降順 → 未指定 (既定へ戻す)
      if (sort === m.asc) next.set('sort', m.desc)
      else if (sort === m.desc) next.delete('sort')
      else next.set('sort', m.asc)
      next.delete('offset')
      setParams(next, { replace: true })
    },
    [map, params, setParams, sort],
  )

  return { sort, dirOf, toggle }
}
