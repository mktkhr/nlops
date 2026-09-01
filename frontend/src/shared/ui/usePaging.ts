import { useCallback } from 'react'
import { useSearchParams } from 'react-router'

export const PAGE_SIZES = [25, 50, 100]

/**
 * ページ位置を URL に置く。
 *
 * フィルタと同じ場所に置くことで、URL を共有すれば同じ画面が再現できる。
 * フィルタを変えたらページは 1 に戻す。戻さないと、絞り込んだ結果が
 * 3 ページしかないのに 8 ページ目を見ていて「0 件」に見える。
 */
export function usePaging(defaultSize = PAGE_SIZES[0]) {
  const [params, setParams] = useSearchParams()
  const limit = Number(params.get('limit')) || defaultSize
  const offset = Math.max(0, Number(params.get('offset')) || 0)

  const setPage = useCallback(
    (nextOffset: number, nextLimit: number) => {
      const next = new URLSearchParams(params)
      if (nextOffset > 0) next.set('offset', String(nextOffset))
      else next.delete('offset')
      if (nextLimit !== defaultSize) next.set('limit', String(nextLimit))
      else next.delete('limit')
      setParams(next, { replace: true })
    },
    [params, setParams, defaultSize],
  )

  // フィルタ変更時に呼ぶ。ページ位置だけを落とす。
  const resetOffset = useCallback(
    (next: URLSearchParams) => {
      next.delete('offset')
      return next
    },
    [],
  )

  return { limit, offset, setPage, resetOffset }
}
