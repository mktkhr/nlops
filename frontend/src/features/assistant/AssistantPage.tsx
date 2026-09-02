import { useCallback, useEffect, useRef, useState } from 'react'
import { useNavigate } from 'react-router'
import Alert from '@mui/material/Alert'
import Box from '@mui/material/Box'
import Button from '@mui/material/Button'
import Chip from '@mui/material/Chip'
import Collapse from '@mui/material/Collapse'
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
import AddIcon from '@mui/icons-material/Add'
import SendIcon from '@mui/icons-material/Send'
import { executeCommand, streamAsk } from '../../shared/api/client'
import type { Done, ExecuteResult, Navigation, Proposal, Step } from '../../shared/api/client'
import { NAV } from '../../app/nav'
import { AnswerText } from '../../shared/ui/AnswerText'
import { useUser } from '../../shared/user/user-context'

const EXAMPLES = [
  '田中太郎さんの注文を画面で見せて',
  '西日本の顧客の一覧を開いて',
  '高橋みどりさんに未払いの請求はありますか',
  '在庫が5個を下回っている商品を出して',
  '注文 O-1002 をキャンセルして',
]

/**
 * 更新前後の値の表示。
 *
 * 値の型はサービス次第なので unknown で受ける。オブジェクトをそのまま
 * 文字列にすると "[object Object]" になって何も分からなくなる。
 */
function fmtValue(v: unknown): string {
  if (v === null || v === undefined || v === '') return '(なし)'
  if (typeof v === 'string') return v
  if (typeof v === 'number' || typeof v === 'boolean') return String(v)
  return JSON.stringify(v)
}

/**
 * 進行中・完了した段階の 1 行。
 *
 * 画面遷移で終わる問い合わせは Tool を 1 つも実行しないので、
 * ステップが 1 つも出ないまま数秒が過ぎる。ボタンが回っているだけだと、
 * **送れているのか、応答を待っているのか**が利用者に分からない。
 *
 * 済んだ段階も消さずに残す。**何をどの順でやったか**が後から追えるほうが、
 * 一行が入れ替わるより読み取れる情報が多い。
 */
function PhaseRow({
  done,
  label,
  elapsed,
}: {
  done: boolean
  label: string
  elapsed: number
}) {
  return (
    <Stack direction="row" spacing={1} sx={{ alignItems: 'center' }}>
      {done ? (
        <CheckCircleIcon fontSize="small" color="success" />
      ) : (
        // 完了時のチェックと同じ幅を占めるようにして、行が横にずれないようにする。
        <Box sx={{ display: 'flex', width: 20, justifyContent: 'center' }}>
          <CircularProgress size={14} />
        </Box>
      )}
      <Typography variant="body2" color="text.secondary">
        {label}
        {/* 経過秒は「今待っている行」にだけ出す。済んだ行に出すと、
            その段階にかかった時間だと誤解される。 */}
        {!done && elapsed > 0 && `…（${elapsed} 秒経過）`}
        {!done && elapsed === 0 && '…'}
      </Typography>
    </Stack>
  )
}

/** その先にステップが続かない段階か。navigate / propose / finish で Loop は終わる。 */
function isTerminal(step: Step | undefined): boolean {
  return Boolean(step && (step.finish || step.navigate || step.proposal))
}

/** LLM が返した画面の状態を URL へ変換する。 */
function toPath(nav: Navigation): string {
  const q = new URLSearchParams(
    Object.entries(nav.filters ?? {}).filter(([, v]) => v !== ''),
  ).toString()
  return q ? `${nav.route}?${q}` : nav.route
}

export function AssistantPage() {
  const { current, error: userError } = useUser()
  const navigate = useNavigate()
  const [query, setQuery] = useState('')
  // 送った質問。入力欄は送信で空にするので、表示用に別に持つ。
  const [asked, setAsked] = useState('')
  // 済んだ往復。**サーバは会話の状態を持たないので、これを毎回送る。**
  // 途中の Tool 結果は送らない (質問と回答だけ)。積むと context が膨らみ、
  // この基盤が拠り所にしている prefix cache の利点を失う。
  const [turns, setTurns] = useState<{ query: string; answer: string }[]>([])
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
  // 送信してから最初のステップが届くまで数秒あるので、何を待っているかを出す。
  const [started, setStarted] = useState(false)
  const [elapsed, setElapsed] = useState(0)
  // モデルの思考。既定は off。on にすると 5 倍以上遅くなり、
  // 制約デコードの JSON が出てこない失敗が増える (docs/decisions.md)。
  const [thinking, setThinking] = useState(false)
  const abortRef = useRef<AbortController | null>(null)

  // 何も始まっていない状態か。入力欄を中央に置くかどうかの判断に使う。
  const empty = !asked && turns.length === 0

  // 経過秒。数十秒かかることがあるので、動いていることが分かるようにする。
  useEffect(() => {
    if (!running) return
    const t = setInterval(() => setElapsed((v) => v + 1), 1000)
    return () => clearInterval(t)
  }, [running])

  const ask = useCallback(
    async (q: string) => {
      if (!current || !q.trim()) return
      // running を条件にすると、state の反映が 1 描画遅れるぶん、
      // 素早く 2 回押したときに両方通ってしまう。**前の要求を必ず打ち切る**。
      // 打ち切らずに走らせると、古いストリームの step が新しい結果に混ざる。
      abortRef.current?.abort()

      const controller = new AbortController()
      abortRef.current = controller
      // この要求が最新かを毎回確かめる。中断しても、既に届いていた
      // イベントのコールバックが後から走ることがある。
      const isLatest = () => abortRef.current === controller

      // 直前の往復を履歴へ送る。回答が無いまま次を送った場合 (中断など) は
      // 積まない。空の回答を文脈として渡しても役に立たない。
      const history = asked && answer ? [...turns, { query: asked, answer }] : turns
      setTurns(history)
      setAsked(q)
      setQuery('')
      setSteps([])
      setAnswer('')
      setDone(null)
      setProposal(null)
      setNavigation(null)
      setTraceId('')
      setError('')
      setStarted(false)
      setElapsed(0)
      setRunning(true)

      try {
        await streamAsk(
          q,
          current.userId,
          {
            onStart: (st) => {
              if (!isLatest()) return
              setTraceId(st.traceId)
              setStarted(true)
            },
            onStep: (s) => isLatest() && setSteps((prev) => [...prev, s]),
            onNavigate: (n) => isLatest() && setNavigation(n),
            onProposal: (pr) => isLatest() && setProposal(pr),
            onAnswer: (a) => isLatest() && setAnswer(a),
            onDone: (d) => isLatest() && setDone(d),
            onError: (m) => isLatest() && setError(m),
          },
          controller.signal,
          thinking,
          history,
        )
      } catch (e) {
        if (!controller.signal.aborted) {
          setError(e instanceof Error ? e.message : String(e))
        }
      } finally {
        if (abortRef.current === controller) {
          setRunning(false)
          abortRef.current = null
        }
      }
    },
    [current, thinking, asked, answer, turns],
  )

  // 最初の状態へ戻す。ask の頭でやっているリセットと同じものを、
  // 質問文まで含めて行う。
  const reset = useCallback(() => {
    abortRef.current?.abort()
    abortRef.current = null
    setAsked('')
    setTurns([])
    setQuery('')
    setSteps([])
    setAnswer('')
    setDone(null)
    setProposal(null)
    setNavigation(null)
    setTraceId('')
    setError('')
    setStarted(false)
    setElapsed(0)
    setRunning(false)
  }, [])

  const form = (
    <PromptForm
      query={query}
      setQuery={setQuery}
      ask={(q) => void ask(q)}
      running={running}
      thinking={thinking}
      setThinking={setThinking}
      showExamples={empty}
    />
  )

  if (empty) {
    // 何も始まっていないときは入力欄を画面の中央に置く。
    // 業務画面の見出しより先に「何を聞けばいいか」を見せたい。
    return (
      <Box
        sx={{
          flexGrow: 1,
          display: 'flex',
          flexDirection: 'column',
          justifyContent: 'center',
          maxWidth: 720,
          width: '100%',
          mx: 'auto',
        }}
      >
        {userError && (
          <Alert severity="error" sx={{ mb: 2 }}>
            {userError}
          </Alert>
        )}
        <Typography
          variant="h5"
          component="h1"
          sx={{ fontWeight: 700, textAlign: 'center', mb: 1 }}
        >
          何を調べますか？
        </Typography>
        <Typography
          variant="body2"
          color="text.secondary"
          sx={{ textAlign: 'center', mb: 3 }}
        >
          必要な業務 API を選んで実行し、結果をまとめて答えます。
          参照できる範囲はヘッダーで選んだ実行ユーザーの権限に従います。
        </Typography>
        {form}
      </Box>
    )
  }

  return (
    <Box sx={{ flexGrow: 1, display: 'flex', flexDirection: 'column', maxWidth: 860, width: '100%', mx: 'auto' }}>
      {userError && (
        <Alert severity="error" sx={{ mb: 2 }}>
          {userError}
        </Alert>
      )}

      {/* 済んだ往復は質問と回答だけを残す。実行の詳細まで積むと、
          今どの質問の結果を見ているのかが分からなくなる。 */}
      {turns.map((t, i) => (
        <Box key={i} sx={{ mb: 2 }}>
          <QuestionBubble text={t.query} />
          <Box sx={{ mt: 1 }}>
            <AnswerText text={t.answer} />
          </Box>
        </Box>
      ))}

      {/* 何を聞いたかを最初に出す。入力欄は送信で空になるので、
          ここに残っていないと「何に対する答えか」が分からなくなる。 */}
      {asked && <QuestionBubble text={asked} />}

      {error && (
        <Alert severity="error" sx={{ mb: 2 }}>
          {error}
        </Alert>
      )}

      {(running || steps.length > 0) && (
        <Paper variant="outlined" sx={{ p: 2, mb: 2 }}>
          <Typography variant="overline" color="text.secondary">
            実行した操作
          </Typography>
          <Stack spacing={1} sx={{ mt: 1 }}>
            {/* 済んだ段階は消さずに残す。何をどの順でやったかが後から追えるように。 */}
            <PhaseRow
              done={started}
              label={started ? 'リクエストを送信しました' : 'リクエストを送信しています'}
              elapsed={elapsed}
            />
            {started && (
              <PhaseRow
                done={steps.length > 0}
                label={
                  steps.length > 0
                    ? 'どの操作が必要かを判断しました'
                    : 'どの操作が必要かを判断しています'
                }
                elapsed={elapsed}
              />
            )}
            {steps.map((s) => (
              <StepRow key={s.iteration} step={s} />
            ))}
            {running && steps.length > 0 && !isTerminal(steps[steps.length - 1]) && (
              <PhaseRow done={false} label="次の操作を判断しています" elapsed={elapsed} />
            )}
            {running && steps.length > 0 && isTerminal(steps[steps.length - 1]) && !answer && (
              <PhaseRow done={false} label="回答をまとめています" elapsed={elapsed} />
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
          <Box sx={{ mt: 1 }}>
            <AnswerText text={answer} />
          </Box>
        </Paper>
      )}

      {done && <AppliedFilters filters={done.filters} />}
      {done && <Metrics done={done} />}

      {/* 実行中は入力欄を出さない。押しても何も起きない欄を残すのは、
          「今は受け付けない」ことを利用者に判断させることになる。
          終わったら続けて聞けるように出す。 */}
      {!running && (
        <Box sx={{ mt: 3 }}>
          {form}
          <Box sx={{ mt: 1, display: 'flex', justifyContent: 'center' }}>
            <Button size="small" startIcon={<AddIcon />} onClick={reset}>
              新しい会話を始める
            </Button>
          </Box>
        </Box>
      )}
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
        {nav.denied ? '参照できない画面です' : '画面で確認できます'}
      </Typography>
      <Stack
        direction={{ xs: 'column', sm: 'row' }}
        spacing={1.5}
        sx={{ mt: 1, alignItems: { sm: 'center' }, justifyContent: 'space-between' }}
      >
        <Box sx={{ minWidth: 0 }}>
          <Typography sx={{ fontWeight: 600 }}>
            {label}
            {nav.summary && (
              <Typography component="span" color="text.secondary" sx={{ ml: 1 }}>
                該当 {nav.summary.count.toLocaleString()} {nav.summary.unit}
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
        {/* 権限が無い画面への導線は出さない。押しても何も見られないボタンは、
            「壊れている」のか「見せてもらえない」のかを利用者に判断させる。 */}
        {!nav.denied && (
          <Button
            variant="contained"
            startIcon={<OpenInNewIcon />}
            onClick={onOpen}
            sx={{ flexShrink: 0, alignSelf: { xs: 'stretch', sm: 'center' } }}
          >
            この条件で開く
          </Button>
        )}
      </Stack>

      {nav.denied && (
        <Alert severity="warning" icon={<BlockIcon fontSize="small" />} sx={{ mt: 1.5 }}>
          この画面を参照する権限がありません。実行ユーザーを切り替えるか、
          権限のある担当者に依頼してください。
        </Alert>
      )}

      {nav.summary && nav.summary.rows.length > 0 && (
        <Box sx={{ mt: 2 }}>
          <Divider sx={{ mb: 1 }} />
          <Stack divider={<Divider flexItem />}>
            {nav.summary.rows.map((row) => (
              <Stack
                key={row.key}
                direction="row"
                spacing={1}
                sx={{ py: 0.75, alignItems: 'baseline', minWidth: 0 }}
              >
                <Typography
                  variant="caption"
                  color="text.secondary"
                  sx={{ fontFamily: 'monospace', flexShrink: 0 }}
                >
                  {row.key}
                </Typography>
                <Typography variant="body2" sx={{ flexGrow: 1, minWidth: 0 }} noWrap>
                  {row.title}
                </Typography>
                <Typography
                  variant="caption"
                  color="text.secondary"
                  sx={{ flexShrink: 0, display: { xs: 'none', sm: 'block' } }}
                >
                  {row.detail}
                </Typography>
                {row.trailing !== undefined && (
                  <Typography variant="body2" sx={{ flexShrink: 0, fontVariantNumeric: 'tabular-nums' }}>
                    {row.trailing.toLocaleString()} 円
                  </Typography>
                )}
              </Stack>
            ))}
          </Stack>
          {nav.summary.count > nav.summary.rows.length && (
            <Typography variant="caption" color="text.secondary" sx={{ display: 'block', mt: 1 }}>
              先頭 {nav.summary.rows.length} {nav.summary.unit}のみ表示しています。
              残りは画面で確認してください。
            </Typography>
          )}
        </Box>
      )}
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
  const [result, setResult] = useState<{
    ok: boolean
    message: string
    changes?: ExecuteResult['changes']
  } | null>(null)

  const run = async () => {
    if (!current) return
    setRunning(true)
    setResult(null)
    try {
      const r = await executeCommand(current.userId, proposal.command, proposal.arguments, traceId)
      setResult({
        ok: true,
        // 二重送信は実行されず、済んでいることだけが返る。
        // 「実行しました」と出すと 2 回実行されたように読める。
        message: r.alreadyExecuted ? 'この操作は既に実行済みです。' : '実行しました。',
        changes: r.changes,
      })
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
        <Alert severity={result.ok ? 'success' : 'error'}>
          {result.message}
          {/* 何がどう変わったかを出す。承認した内容がそのまま反映されたかを
              画面で確かめられないと、承認した意味が薄い。 */}
          {result.changes && result.changes.length > 0 && (
            <Box
              component="dl"
              sx={{
                display: 'grid',
                gridTemplateColumns: 'max-content minmax(0, 1fr)',
                columnGap: 2,
                rowGap: 0.5,
                mt: 1,
                mb: 0,
              }}
            >
              {result.changes.map((c) => (
                <Box key={c.field} sx={{ display: 'contents' }}>
                  <Typography component="dt" variant="body2" color="text.secondary">
                    {c.field}
                  </Typography>
                  <Typography
                    component="dd"
                    variant="body2"
                    sx={{ m: 0, overflowWrap: 'anywhere' }}
                  >
                    <Box component="span" sx={{ color: 'text.secondary', textDecoration: 'line-through' }}>
                      {fmtValue(c.before)}
                    </Box>
                    {' → '}
                    <Box component="span" sx={{ fontWeight: 600 }}>
                      {fmtValue(c.after)}
                    </Box>
                  </Typography>
                </Box>
              ))}
            </Box>
          )}
        </Alert>
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

/**
 * 問い合わせの入力欄。**質問を出す前にしか出さない。**
 *
 * 実行中は押しても何も起きないので隠し、終わったら「新しい質問」を出す。
 * 動かない入力欄を置いたままにするのは、1 問ずつしか処理できないという
 * 実装の都合を利用者に見せているだけになる。
 */
function PromptForm({
  query,
  setQuery,
  ask,
  running,
  thinking,
  setThinking,
  showExamples,
}: {
  query: string
  setQuery: (v: string) => void
  ask: (q: string) => void
  running: boolean
  thinking: boolean
  setThinking: (v: boolean) => void
  showExamples: boolean
}) {
  const { current, loading: userLoading } = useUser()
  const inputRef = useRef<HTMLTextAreaElement>(null)
  const disabled = running || !current

  // autoFocus では効かない。実行ユーザーが決まるまで入力欄は disabled で、
  // **無効な要素はフォーカスを受け取れない**。マウント時に一度だけ効く
  // autoFocus では、有効になった後に当たらない。
  // 有効になった時点で当てに行き、中央表示のときだけにする
  // (会話が始まった後に奪うと、結果を読んでいる最中に画面が入力欄へ飛ぶ)。
  useEffect(() => {
    if (!disabled) inputRef.current?.focus()
  }, [disabled])

  return (
      <Paper variant="outlined" sx={{ p: 2 }}>
      {/* 初期状態は縦に積む。狭い画面で横に並べると入力欄が数文字分しか残らない。
          会話中の下部バーでは逆に横並びにする。縦積みだと 375px で
          バーが画面の 28% を占め、結果の見える範囲がその分減る。 */}
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
          inputRef={inputRef}
          placeholder="例: 田中太郎さんの未発送の注文を確認したい"
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) {
              ask(query)
            }
          }}
          disabled={disabled}
        />
        <Button
          variant="contained"
          startIcon={running ? <CircularProgress size={16} /> : <SendIcon />}
          onClick={() => ask(query)}
          // 実行ユーザーが決まっていないと権限を伴う問い合わせができない。
          // 押しても無反応になるより、押せないことを見せる。
          disabled={running || !query.trim() || !current || userLoading}
          sx={{ minWidth: 104, height: 40, alignSelf: { xs: 'flex-end', sm: 'auto' } }}
        >
          送信
        </Button>
      </Stack>
      {/* Tooltip は触れる端末では開かない。要点はラベルに出し、
          詳しい数字だけを Tooltip に残す。
          会話中はラベルを短くする。下部に貼り付く枠なので、
          縦を取るほど結果の見える範囲が減る (375px で実測 223px を占めていた)。 */}
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
      {/* 例は最初だけ。会話が始まった後は、済んだやり取りの下に
          関係のない候補が並ぶことになる。 */}
      {showExamples && (
        <Stack direction="row" spacing={1} sx={{ mt: 1.5, flexWrap: 'wrap', gap: 1 }}>
          {EXAMPLES.map((ex) => (
            <Chip
              key={ex}
              label={ex}
              size="small"
              variant="outlined"
              // 押したら送るだけ。入力欄には残さない (送信で空にする方針に揃える)。
              onClick={() => ask(ex)}
              disabled={disabled}
            />
          ))}
        </Stack>
      )}
    </Paper>
  )
}

/** 利用者が送った質問。右寄せの吹き出しにして、回答と見分けられるようにする。 */
function QuestionBubble({ text }: { text: string }) {
  return (
    <Box sx={{ display: 'flex', justifyContent: 'flex-end' }}>
      <Paper
        elevation={0}
        sx={{ px: 2, py: 1.25, maxWidth: '85%', bgcolor: 'action.selected', borderRadius: 3 }}
      >
        <Typography sx={{ whiteSpace: 'pre-wrap', overflowWrap: 'anywhere' }}>{text}</Typography>
      </Paper>
    </Box>
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
        <StepResult step={step} />
      </Box>
    </Stack>
  )
}

/**
 * 実行した操作の結果。
 *
 * ここに出すのは **Projection を通した後のもの、つまり LLM が実際に見たもの**。
 * 生のレスポンスではない。回答が正しいかを確かめるには、
 * モデルが何を根拠にしたかが見えている必要がある
 * (ダミーの値をそのまま答えに使う失敗を実測している)。
 */
function StepResult({ step }: { step: Step }) {
  const [open, setOpen] = useState(false)
  if (step.result === undefined || step.result === null) return null

  const r = step.result as Record<string, unknown>
  const items = Array.isArray(r.items) ? (r.items as unknown[]) : null
  const count = typeof r.count === 'number' ? r.count : null

  // 件数の言い方を「取得できた分」と「全体」で分ける。
  // 20 件しか見ていないのに「10,787 件を見た」と読めると、
  // 回答の裏取りにならない。
  let summary: string
  if (items) {
    summary =
      count !== null && count > items.length
        ? `${count.toLocaleString()} 件中 ${items.length} 件を取得`
        : `${items.length} 件`
  } else {
    summary = '1 件'
  }

  return (
    <Box sx={{ mt: 0.5 }}>
      <Stack direction="row" spacing={1} sx={{ alignItems: 'center', flexWrap: 'wrap' }}>
        <Typography variant="caption" color="text.secondary">
          {step.status ? `${step.status} · ` : ''}
          {summary}
          {step.llmMs ? ` · 判断 ${(step.llmMs / 1000).toFixed(1)} 秒` : ''}
        </Typography>
        <Button size="small" sx={{ minWidth: 0, p: 0 }} onClick={() => setOpen((v) => !v)}>
          {open ? '閉じる' : 'LLM が見たデータ'}
        </Button>
      </Stack>
      <Collapse in={open} unmountOnExit>
        <Box
          component="pre"
          sx={{
            mt: 0.5,
            p: 1,
            m: 0,
            fontSize: 12,
            bgcolor: 'action.hover',
            borderRadius: 1,
            // 長い JSON で画面全体が横に伸びないよう、この中だけを流す。
            maxHeight: 240,
            overflow: 'auto',
          }}
        >
          {JSON.stringify(step.result, null, 2)}
        </Box>
      </Collapse>
    </Box>
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
