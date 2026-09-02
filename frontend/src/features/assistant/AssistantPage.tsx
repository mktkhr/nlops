import { useCallback, useRef, useState } from 'react'
import { useNavigate } from 'react-router'
import Alert from '@mui/material/Alert'
import Box from '@mui/material/Box'
import Button from '@mui/material/Button'
import Chip from '@mui/material/Chip'
import CircularProgress from '@mui/material/CircularProgress'
import Divider from '@mui/material/Divider'
import FormControlLabel from '@mui/material/FormControlLabel'
import Switch from '@mui/material/Switch'
import Tooltip from '@mui/material/Tooltip'
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
import { NAV } from '../../app/nav'
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
  // 画面遷移は「提案」として扱い、すぐには飛ばさない。
  // 勝手に飛ぶと会話も、使われた絞り込みも見えないまま消える。
  // 更新操作と同じで、LLM は提案し、移動するかは人間が決める。
  const [navigation, setNavigation] = useState<Navigation | null>(null)
  const [traceId, setTraceId] = useState('')
  const [error, setError] = useState('')
  const [running, setRunning] = useState(false)
  // モデルの思考。既定は off。on にすると 5 倍以上遅くなり、
  // 制約デコードの JSON が出てこない失敗が増える (docs/decisions.md)。
  const [thinking, setThinking] = useState(false)
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
            onNavigate: setNavigation,
            onProposal: setProposal,
            onAnswer: setAnswer,
            onDone: setDone,
            onError: setError,
          },
          controller.signal,
          thinking,
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
    [current, running, navigate, thinking],
  )

  return (
    <Box>
      <PageHeader
        title="アシスタント"
        description="自然言語で問い合わせると、必要な業務 API を選んで実行し、結果をまとめて答えます。参照できる範囲はヘッダーで選んだ実行ユーザーの権限に従います。"
      />

      {userError && (
        <Alert severity="error" sx={{ mb: 2 }}>
          {userError}
        </Alert>
      )}

      <Paper variant="outlined" sx={{ p: 2, mb: 2 }}>
        {/* 狭い画面では横に並べると入力欄が数文字分しか残らない。縦に積む。 */}
        <Stack
          direction={{ xs: 'column', sm: 'row' }}
          spacing={1}
          sx={{ alignItems: { sm: 'flex-start' } }}
        >
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
            sx={{ minWidth: 104, height: 40, alignSelf: { xs: 'flex-end', sm: 'auto' } }}
          >
            送信
          </Button>
        </Stack>
        {/* Tooltip は触れる端末では開かない。要点はラベルに出し、
            詳しい数字だけを Tooltip に残す。 */}
        <Tooltip
          title="モデルに考えさせてから答えさせます。実測では精度が 98% → 78% に落ち、5 倍以上遅くなります (失敗の大半は応答が空になる形)。違いを見るための切り替えです。"
          placement="top-start"
        >
          <FormControlLabel
            sx={{ mt: 1, mr: 0, alignItems: 'center' }}
            control={
              <Switch
                size="small"
                checked={thinking}
                onChange={(e) => setThinking(e.target.checked)}
                disabled={running}
              />
            }
            label={
              <Typography variant="body2" color="text.secondary">
                モデルに思考させる（遅くなり、精度は落ちます）
              </Typography>
            }
          />
        </Tooltip>
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

      {navigation && (
        <NavigationCard
          nav={navigation}
          onOpen={() => navigate(toPath(navigation))}
        />
      )}

      {proposal && (
        <ProposalCard
          proposal={proposal}
          traceId={traceId}
          onDone={() => setProposal(null)}
        />
      )}

      {/* 遷移の場合は理由をカード側で見せているので、同じ文言を二度出さない。 */}
      {answer && answer !== navigation?.reason && (
        <Paper variant="outlined" sx={{ p: 2, mb: 2 }}>
          <Typography variant="overline" color="text.secondary">
            回答
          </Typography>
          <Typography sx={{ mt: 1, whiteSpace: 'pre-wrap', lineHeight: 1.8 }}>
            {answer}
          </Typography>
        </Paper>
      )}

      {done && <AppliedFilters filters={done.filters} />}
      {done && <Metrics done={done} />}
    </Box>
  )
}

/**
 * 画面遷移の提案。押して初めて移動する。
 *
 * 自動で飛ばしていたときは、会話も「使われた絞り込み」も見えないまま
 * 消えていた。**遷移先とフィルタを見てから移動を決められる**ようにする。
 * 絞り込みをここで見せるのは、モデルが頼まれていない条件を足すことが
 * あるため (docs/decisions.md)。
 */
function NavigationCard({ nav, onOpen }: { nav: Navigation; onOpen: () => void }) {
  const label = NAV.find((n) => n.to === nav.route)?.label ?? nav.route
  const filters = Object.entries(nav.filters ?? {}).filter(([, v]) => v !== '')
  return (
    <Paper variant="outlined" sx={{ p: 2, mb: 2 }}>
      <Typography variant="overline" color="text.secondary">
        画面で確認できます
      </Typography>
      <Stack
        direction={{ xs: 'column', sm: 'row' }}
        spacing={1.5}
        sx={{ mt: 1, alignItems: { sm: 'center' }, justifyContent: 'space-between' }}
      >
        <Box sx={{ minWidth: 0 }}>
          <Typography sx={{ fontWeight: 600 }}>
            {label}
            {nav.count !== undefined && (
              <Typography component="span" color="text.secondary" sx={{ ml: 1 }}>
                該当 {nav.count.toLocaleString()} 件
              </Typography>
            )}
          </Typography>
          {filters.length > 0 && (
            <Stack direction="row" spacing={1} sx={{ mt: 1, flexWrap: 'wrap', rowGap: 1 }}>
              {filters.map(([k, v]) => (
                <Chip key={k} size="small" variant="outlined" label={`${k} = ${v}`} />
              ))}
            </Stack>
          )}
          {nav.reason && (
            <Typography variant="body2" color="text.secondary" sx={{ mt: 1 }}>
              {nav.reason}
            </Typography>
          )}
        </Box>
        <Button
          variant="contained"
          startIcon={<OpenInNewIcon />}
          onClick={onOpen}
          sx={{ flexShrink: 0, alignSelf: { xs: 'stretch', sm: 'center' } }}
        >
          この条件で開く
        </Button>
      </Stack>
    </Paper>
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

      {/* 引数の値は長さが読めない。折り返しを許さないと枠から溢れる。 */}
      <Box
        component="dl"
        sx={{
          display: 'grid',
          gridTemplateColumns: 'max-content minmax(0, 1fr)',
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
            <Typography
              component="dd"
              variant="body2"
              sx={{ m: 0, fontFamily: 'monospace', overflowWrap: 'anywhere' }}
            >
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
          <Box component="span" sx={{ fontFamily: 'monospace', overflowWrap: 'anywhere' }}>
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
            // 引数の JSON は区切りが無いので、指定しないと折り返らず枠を突き破る。
            sx={{ fontFamily: 'monospace', ml: 1, overflowWrap: 'anywhere' }}
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

/**
 * 実際に適用された絞り込み条件。
 *
 * モデルが利用者の求めていない条件を勝手に足すことがあり
 * (「一番古い注文」に status=PLACED を付けて別の注文を答えた)、
 * **構造で防ぐ方法は見つかっていない**。防げないので見せる。
 * 頼んでいない条件が並んでいれば、人間なら一目で分かる。
 */
function AppliedFilters({ filters }: { filters?: Record<string, string> }) {
  const entries = Object.entries(filters ?? {})
  if (entries.length === 0) return null
  return (
    <Paper variant="outlined" sx={{ px: 2, py: 1.5, mb: 2 }}>
      <Typography variant="caption" color="text.secondary" sx={{ display: 'block', mb: 0.5 }}>
        この回答で使われた絞り込み
      </Typography>
      <Stack direction="row" spacing={1} sx={{ flexWrap: 'wrap', rowGap: 1 }}>
        {entries.map(([k, v]) => (
          <Chip key={k} size="small" variant="outlined" label={`${k} = ${v}`} />
        ))}
      </Stack>
      <Typography variant="caption" color="text.secondary" sx={{ display: 'block', mt: 1 }}>
        求めていない条件が含まれていないか確認してください。
      </Typography>
    </Paper>
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
