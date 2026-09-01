import Typography from '@mui/material/Typography'

/**
 * 「該当 50,011 件 / 表示 100 件」を出す。
 *
 * サービスは 1 ページしか返さない。総件数を出さずに 100 行だけ並べると、
 * 利用者は「これで全部だ」と読む。件数の話は業務判断に直結するので、
 * 表示している範囲を明示する。
 */
export function ResultCount({
  count,
  shown,
  hasMore,
  unit,
}: {
  count: number
  shown: number
  hasMore: boolean
  unit: string
}) {
  if (shown === 0) return null
  return (
    <Typography variant="body2" color="text.secondary" sx={{ px: 2, py: 1 }}>
      該当 {count.toLocaleString()} {unit}
      {hasMore && ` / うち ${shown.toLocaleString()} ${unit}を表示（条件を絞り込んでください）`}
    </Typography>
  )
}
