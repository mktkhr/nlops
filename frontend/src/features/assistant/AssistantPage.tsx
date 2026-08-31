import { useCallback, useRef, useState } from 'react'
import { useNavigate } from 'react-router'
import Alert from '@mui/material/Alert'
import Box from '@mui/material/Box'
import Button from '@mui/material/Button'
import Chip from '@mui/material/Chip'
import CircularProgress from '@mui/material/CircularProgress'
import Divider from '@mui/material/Divider'
import Paper from '@mui/material/Paper'
import Stack from '@mui/material/Stack'
import TextField from '@mui/material/TextField'
import Typography from '@mui/material/Typography'
import BlockIcon from '@mui/icons-material/Block'
import CheckCircleIcon from '@mui/icons-material/CheckCircle'
import OpenInNewIcon from '@mui/icons-material/OpenInNew'
import EditNoteIcon from '@mui/icons-material/EditNote'
import ErrorIcon from '@mui/icons-material/Error'
import SendIcon from '@mui/icons-material/Send'
import { executeCommand, streamAsk } from '../../shared/api/client'
import type { Done, Navigation, Proposal, Step } from '../../shared/api/client'
import { PageHeader } from '../../shared/ui/PageHeader'
import { useUser } from '../../shared/user/user-context'

const EXAMPLES = [
  '田中太郎さんの注文を画面で見せて',
  '西日本の顧客の一覧を開いて',
  '高橋みどりさんに未払いの請求はありますか',
  '在庫が5個を下回っている商品を出して',
  '注文 O-1002 をキャンセルして',
]

/** LLM が返した画面の状態を URL へ変換する。 */
function toPath(nav: Navigation): string {
  const q = new URLSearchParams(
    Object.entries(nav.filters ?? {}).filter(([, v]) => v !== ''),
  ).toString()
  return q ? `${nav.route}?${q}` : nav.route
}

export function AssistantPage() {
  const { current, error: userError, loading: userLoading } = useUser()
  const navigate = useNavigate()
  const [query, setQuery] = useState('')
  const [steps, setSteps] = useState<Step[]>([])
  const [answer, setAnswer] = useState('')
  const [done, setDone] = useState<Done | null>(null)
  const [proposal, setProposal] = useState<Proposal | null>(null)
  const [traceId, setTraceId] = useState('')
  const [error, setError] = useState('')
  const [running, setRunning] = useState(false)
  const abortRef = useRef<AbortController | null>(null)

  const ask = useCallback(
    async (q: string) => {
      if (!current || !q.trim() || running) return
      setSteps([])
      setAnswer('')
      setDone(null)
      setProposal(null)
      setError('')
      setRunning(true)

      const controller = new AbortController()
      abortRef.current = controller
      try {
        await streamAsk(
          q,
          current.userId,
          {
            onStart: (st) => setTraceId(st.traceId),
            onStep: (s) => setSteps((prev) => [...prev, s]),
            // 画面を開くのが答えなので、そのまま遷移する。
            onNavigate: (nav) => navigate(toPath(nav)),
            onProposal: setProposal,
            onAnswer: setAnswer,
            onDone: setDone,
            onError: setError,
          },
          controller.signal,
        )
      } catch (e) {
        if (!controller.signal.aborted) {
          setError(e instanceof Error ? e.message : String(e))
        }
      } finally {
        setRunning(false)
        abortRef.current = null
      }
    },
    [current, running, navigate],
  )

  return (
    <Box>
      <PageHeader
        title="アシスタント"
        description="自然言語で問い合わせると、必要な業務 API を選んで実行し、結果をまとめて答えます。参照できる範囲は右上のユーザーの権限に従います。"
      />

      {userError && (
        <Alert severity="error" sx={{ mb: 2 }}>
          {userError}
        </Alert>
      )}

      <Paper variant="outlined" sx={{ p: 2, mb: 2 }}>
        <Stack direction="row" spacing={1} sx={{ alignItems: 'flex-start' }}>
          <TextField
            fullWidth
            multiline
            maxRows={4}
            size="small"
            placeholder="例: 田中太郎さんの未発送の注文を確認したい"
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) {
                void ask(query)
              }
            }}
            disabled={running || !current}
          />
          <Button
            variant="contained"
            startIcon={running ? <CircularProgress size={16} /> : <SendIcon />}
            onClick={() => void ask(query)}
            // 実行ユーザーが決まっていないと権限を伴う問い合わせができない。
            // 押しても無反応になるより、押せないことを見せる。
            disabled={running || !query.trim() || !current || userLoading}
            sx={{ minWidth: 104, height: 40 }}
          >
            送信
          </Button>
        </Stack>
        <Stack direction="row" spacing={1} sx={{ mt: 1.5, flexWrap: 'wrap', gap: 1 }}>
          {EXAMPLES.map((ex) => (
            <Chip
              key={ex}
              label={ex}
              size="small"
              variant="outlined"
              onClick={() => {
                setQuery(ex)
                void ask(ex)
              }}
              disabled={running || !current}
            />
          ))}
        </Stack>
      </Paper>

      {error && (
        <Alert severity="error" sx={{ mb: 2 }}>
          {error}
        </Alert>
      )}

      {steps.length > 0 && (
        <Paper variant="outlined" sx={{ p: 2, mb: 2 }}>
          <Typography variant="overline" color="text.secondary">
            実行した操作
          </Typography>
          <Stack spacing={1} sx={{ mt: 1 }}>
            {steps.map((s) => (
              <StepRow key={s.iteration} step={s} />
            ))}
            {running && (
              <Stack direction="row" spacing={1} sx={{ alignItems: 'center', pl: 0.5 }}>
                <CircularProgress size={14} />
                <Typography variant="body2" color="text.secondary">
                  次の操作を判断しています…
                </Typography>
              </Stack>
            )}
          </Stack>
        </Paper>
      )}

      {proposal && (
        <ProposalCard
          proposal={proposal}
          traceId={traceId}
          onDone={() => setProposal(null)}
        />
      )}

      {answer && (
        <Paper variant="outlined" sx={{ p: 2, mb: 2 }}>
          <Typography variant="overline" color="text.secondary">
            回答
          </Typography>
          <Typography sx={{ mt: 1, whiteSpace: 'pre-wrap', lineHeight: 1.8 }}>
            {answer}
          </Typography>
        </Paper>
      )}

      {done && <Metrics done={done} />}
    </Box>
  )
}

/** 更新操作の確認。ここで人間が承認して初めて実行される。 */
function ProposalCard({
  proposal,
  traceId,
  onDone,
}: {
  proposal: Proposal
  traceId: string
  onDone: () => void
}) {
  const { current } = useUser()
  const [running, setRunning] = useState(false)
  const [result, setResult] = useState<{ ok: boolean; message: string } | null>(null)

  const run = async () => {
    if (!current) return
    setRunning(true)
    setResult(null)
    try {
      await executeCommand(current.userId, proposal.command, proposal.arguments, traceId)
      setResult({ ok: true, message: '実行しました。' })
    } catch (e) {
      setResult({ ok: false, message: e instanceof Error ? e.message : String(e) })
    } finally {
      setRunning(false)
    }
  }

  return (
    <Paper variant="outlined" sx={{ p: 2, mb: 2, borderColor: 'warning.main' }}>
      <Stack direction="row" spacing={1} sx={{ alignItems: 'center', mb: 1 }}>
        <EditNoteIcon fontSize="small" color="warning" />
        <Typography variant="overline" color="text.secondary">
          操作の提案（まだ実行されていません）
        </Typography>
      </Stack>

      <Typography variant="subtitle1" sx={{ fontWeight: 600 }}>
        {proposal.title}
      </Typography>
      {proposal.reason && (
        <Typography variant="body2" color="text.secondary" sx={{ mt: 0.5 }}>
          {proposal.reason}
        </Typography>
      )}

      <Box
        component="dl"
        sx={{
          display: 'grid',
          gridTemplateColumns: 'max-content 1fr',
          columnGap: 2,
          rowGap: 0.5,
          my: 1.5,
        }}
      >
        {Object.entries(proposal.arguments).map(([k, v]) => (
          <Box key={k} sx={{ display: 'contents' }}>
            <Typography component="dt" variant="body2" color="text.secondary">
              {k}
            </Typography>
            <Typography component="dd" variant="body2" sx={{ m: 0, fontFamily: 'monospace' }}>
              {String(v)}
            </Typography>
          </Box>
        ))}
      </Box>

      {proposal.confirm && !result && (
        <Alert severity="warning" sx={{ mb: 1.5 }}>
          {proposal.confirm}
        </Alert>
      )}

      {result ? (
        <Alert severity={result.ok ? 'success' : 'error'}>{result.message}</Alert>
      ) : (
        <Stack direction="row" spacing={1}>
          <Button
            variant="contained"
            color="warning"
            onClick={() => void run()}
            disabled={running}
            startIcon={running ? <CircularProgress size={16} /> : undefined}
          >
            実行する
          </Button>
          <Button variant="outlined" onClick={onDone} disabled={running}>
            取り消す
          </Button>
        </Stack>
      )}
    </Paper>
  )
}

function StepRow({ step }: { step: Step }) {
  if (step.proposal) {
    return (
      <Stack direction="row" spacing={1} sx={{ alignItems: 'center' }}>
        <EditNoteIcon fontSize="small" color="warning" />
        <Typography variant="body2" color="text.secondary">
          操作を提案しました:{' '}
          <Box component="span" sx={{ fontFamily: 'monospace' }}>
            {step.proposal.command}
          </Box>
        </Typography>
      </Stack>
    )
  }
  if (step.navigate) {
    return (
      <Stack direction="row" spacing={1} sx={{ alignItems: 'center' }}>
        <OpenInNewIcon fontSize="small" color="primary" />
        <Typography variant="body2" color="text.secondary">
          画面を開きます:{' '}
          <Box component="span" sx={{ fontFamily: 'monospace' }}>
            {toPath(step.navigate)}
          </Box>
        </Typography>
      </Stack>
    )
  }
  if (step.finish) {
    return (
      <Stack direction="row" spacing={1} sx={{ alignItems: 'center' }}>
        <CheckCircleIcon fontSize="small" color="success" />
        <Typography variant="body2" color="text.secondary">
          情報が揃ったので終了しました
          {step.forced && '（空振りが続いたため打ち切り）'}
        </Typography>
      </Stack>
    )
  }

  const failed = Boolean(step.error) || step.denied
  return (
    <Stack direction="row" spacing={1} sx={{ alignItems: 'flex-start' }}>
      {step.denied ? (
        <BlockIcon fontSize="small" color="warning" />
      ) : failed ? (
        <ErrorIcon fontSize="small" color="error" />
      ) : (
        <CheckCircleIcon fontSize="small" color="success" />
      )}
      <Box sx={{ minWidth: 0 }}>
        <Typography variant="body2" component="span" sx={{ fontFamily: 'monospace' }}>
          {step.tool}
        </Typography>
        {step.arguments && Object.keys(step.arguments).length > 0 && (
          <Typography
            variant="body2"
            component="span"
            color="text.secondary"
            sx={{ fontFamily: 'monospace', ml: 1 }}
          >
            {JSON.stringify(step.arguments)}
          </Typography>
        )}
        {step.denied && (
          <Typography variant="caption" color="warning.main" sx={{ display: 'block' }}>
            権限がないため参照できませんでした
          </Typography>
        )}
        {step.error && !step.denied && (
          <Typography variant="caption" color="error.main" sx={{ display: 'block' }}>
            {step.error}
          </Typography>
        )}
      </Box>
    </Stack>
  )
}

function Metrics({ done }: { done: Done }) {
  const cacheRate = done.promptTok
    ? Math.round((done.cachedTok / done.promptTok) * 100)
    : 0
  const reduction = done.rawBytes
    ? Math.round((1 - done.projBytes / done.rawBytes) * 100)
    : 0
  const items = [
    ['所要時間', `${(done.totalMs / 1000).toFixed(1)} 秒`],
    ['prompt', `${done.promptTok.toLocaleString()} tok`],
    ['cache', `${cacheRate}%`],
    ['API 応答の削減', `${reduction}%`],
  ] as const

  return (
    <Paper variant="outlined" sx={{ px: 2, py: 1.5 }}>
      <Stack
        direction="row"
        spacing={2}
        divider={<Divider orientation="vertical" flexItem />}
        sx={{ flexWrap: 'wrap', rowGap: 1 }}
      >
        {items.map(([label, value]) => (
          <Box key={label}>
            <Typography variant="caption" color="text.secondary" sx={{ display: 'block' }}>
              {label}
            </Typography>
            <Typography variant="body2" sx={{ fontWeight: 600 }}>
              {value}
            </Typography>
          </Box>
        ))}
      </Stack>
      {done.incomplete && (
        <Alert severity="warning" sx={{ mt: 1.5 }}>
          最大ステップ数に達したため途中で打ち切りました。回答が不完全な可能性があります。
        </Alert>
      )}
    </Paper>
  )
}
