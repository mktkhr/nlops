import { useState } from 'react'
import Alert from '@mui/material/Alert'
import Box from '@mui/material/Box'
import Chip from '@mui/material/Chip'
import Stack from '@mui/material/Stack'
import TableBody from '@mui/material/TableBody'
import TableCell from '@mui/material/TableCell'
import TableHead from '@mui/material/TableHead'
import TableRow from '@mui/material/TableRow'
import Tab from '@mui/material/Tab'
import Tabs from '@mui/material/Tabs'
import Typography from '@mui/material/Typography'
import {
  fetchAuditExecutions,
  fetchAuditTraces,
} from '../../shared/api/client'
import type { AuditExecution, AuditTrace } from '../../shared/api/client'
import { useResource } from '../../shared/api/useResource'
import { DataTable } from '../../shared/ui/DataTable'
import { PageHeader } from '../../shared/ui/PageHeader'
import { useUser } from '../../shared/user/user-context'

function when(iso: string): string {
  return new Date(iso).toLocaleString('ja-JP', { hour12: false })
}

export function AuditPage() {
  const { current } = useUser()
  const [tab, setTab] = useState<'executions' | 'traces'>('executions')
  const isAdmin = current?.role === 'admin'

  return (
    <Box>
      <PageHeader
        title="監査ログ"
        description="更新の承認と、AI への問い合わせの実行記録。承認されたものだけでなく、権限や業務ルールで拒否された試行も残ります。"
      />
      {!isAdmin ? (
        <Alert severity="info">
          監査ログを参照できるのは管理者だけです。ヘッダーの実行ユーザーを切り替えてください。
        </Alert>
      ) : (
        <>
          {/* 幅が足りないときはタブ自体を横に流す。潰して読めなくしない。 */}
          <Tabs
            value={tab}
            onChange={(_, v: 'executions' | 'traces') => setTab(v)}
            variant="scrollable"
            scrollButtons="auto"
            allowScrollButtonsMobile
            sx={{ mb: 2, borderBottom: 1, borderColor: 'divider' }}
          >
            <Tab value="executions" label="更新の承認" />
            <Tab value="traces" label="問い合わせ" />
          </Tabs>
          {tab === 'executions' ? <Executions /> : <Traces />}
        </>
      )}
    </Box>
  )
}

function Executions() {
  const { current } = useUser()
  const userId = current?.userId ?? ''
  const { items, error, loading } = useResource<AuditExecution>(
    `exec|${userId}`,
    () => (userId ? fetchAuditExecutions(userId) : Promise.resolve({ items: [] })),
  )

  return (
    <>
      {error && (
        <Alert severity="warning" sx={{ mb: 2 }}>
          {error}
        </Alert>
      )}
      <DataTable
        minWidth={760}
        loading={loading}
        head={
          <TableHead>
            <TableRow>
              <TableCell>日時</TableCell>
              <TableCell>承認者</TableCell>
              <TableCell>操作</TableCell>
              <TableCell>引数</TableCell>
              <TableCell>結果</TableCell>
            </TableRow>
          </TableHead>
        }
      >
        <TableBody>
          {items.map((e) => (
            <TableRow key={e.execution_id} hover>
              <TableCell sx={{ whiteSpace: 'nowrap' }}>{when(e.created_at)}</TableCell>
              <TableCell sx={{ whiteSpace: 'nowrap' }}>
                {e.user_id}
                <Typography variant="caption" color="text.secondary" sx={{ ml: 1 }}>
                  {e.role}
                </Typography>
              </TableCell>
              <TableCell sx={{ fontFamily: 'monospace' }}>{e.command}</TableCell>
              {/* 引数は長さが読めないので、幅を決めて折り返す。放っておくと
                  1 列だけが表を何倍にも広げ、他の列が視界から消える。 */}
              <TableCell
                sx={{
                  fontFamily: 'monospace',
                  fontSize: 12,
                  maxWidth: 260,
                  overflowWrap: 'anywhere',
                }}
              >
                {JSON.stringify(e.arguments)}
              </TableCell>
              <TableCell>
                <Chip
                  size="small"
                  variant="outlined"
                  color={e.ok ? 'success' : 'error'}
                  label={e.ok ? '実行' : `拒否 ${e.status_code}`}
                />
                {e.error && (
                  <Typography variant="caption" color="text.secondary" sx={{ display: 'block' }}>
                    {e.error}
                  </Typography>
                )}
              </TableCell>
            </TableRow>
          ))}
          {!loading && items.length === 0 && !error && (
            <TableRow>
              <TableCell colSpan={5}>
                <Typography variant="body2" color="text.secondary" sx={{ py: 2 }}>
                  まだ更新の承認はありません。
                </Typography>
              </TableCell>
            </TableRow>
          )}
        </TableBody>
      </DataTable>
    </>
  )
}

function Traces() {
  const { current } = useUser()
  const userId = current?.userId ?? ''
  const { items, error, loading } = useResource<AuditTrace>(
    `trace|${userId}`,
    () => (userId ? fetchAuditTraces(userId) : Promise.resolve({ items: [] })),
  )

  const outcomeColor = (o: string) =>
    o === 'error' ? 'error' : o === 'propose' ? 'warning' : 'default'

  return (
    <>
      {error && (
        <Alert severity="warning" sx={{ mb: 2 }}>
          {error}
        </Alert>
      )}
      <DataTable
        minWidth={900}
        loading={loading}
        head={
          <TableHead>
            <TableRow>
              <TableCell>日時</TableCell>
              <TableCell>実行者</TableCell>
              <TableCell>問い合わせ</TableCell>
              <TableCell>判定</TableCell>
              <TableCell>結果</TableCell>
              <TableCell align="right">step</TableCell>
              <TableCell align="right">所要</TableCell>
            </TableRow>
          </TableHead>
        }
      >
        <TableBody>
          {items.map((t) => (
            <TableRow key={t.trace_id} hover>
              <TableCell sx={{ whiteSpace: 'nowrap' }}>{when(t.created_at)}</TableCell>
              <TableCell sx={{ whiteSpace: 'nowrap' }}>{t.user_id}</TableCell>
              {/* 問い合わせ文も長さが読めない。幅を決めて折り返す。 */}
              <TableCell sx={{ minWidth: 200, maxWidth: 320 }}>{t.query}</TableCell>
              <TableCell>{t.intent ?? '—'}</TableCell>
              <TableCell>
                <Stack direction="row" spacing={0.5} sx={{ flexWrap: 'wrap', rowGap: 0.5 }}>
                  <Chip size="small" variant="outlined" color={outcomeColor(t.outcome)} label={t.outcome} />
                  {t.denied && <Chip size="small" variant="outlined" color="warning" label="denied" />}
                  {t.incomplete && (
                    <Chip size="small" variant="outlined" color="warning" label="打ち切り" />
                  )}
                </Stack>
              </TableCell>
              <TableCell align="right">{t.step_count}</TableCell>
              <TableCell align="right" sx={{ whiteSpace: 'nowrap' }}>
                {(t.total_ms / 1000).toFixed(1)} 秒
              </TableCell>
            </TableRow>
          ))}
          {!loading && items.length === 0 && !error && (
            <TableRow>
              <TableCell colSpan={7}>
                <Typography variant="body2" color="text.secondary" sx={{ py: 2 }}>
                  まだ問い合わせの記録はありません。
                </Typography>
              </TableCell>
            </TableRow>
          )}
        </TableBody>
      </DataTable>
    </>
  )
}
