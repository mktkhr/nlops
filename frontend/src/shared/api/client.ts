// BFF との通信をここに閉じる。Feature からは fetch を直接呼ばない。

export type User = {
  userId: string
  name: string
  role: string
  region?: string
}

export type Navigation = {
  route: string
  filters?: Record<string, string>
  reason?: string
  /** その画面に該当する件数。数えられなかった場合は入らない (0 と区別する)。 */
  count?: number
}

export type Proposal = {
  command: string
  title: string
  arguments: Record<string, unknown>
  reason?: string
  confirm?: string
}

export type Step = {
  iteration: number
  tool?: string
  arguments?: Record<string, unknown>
  finish?: boolean
  forced?: boolean
  status?: number
  denied?: boolean
  error?: string
  result?: unknown
  navigate?: Navigation
  proposal?: Proposal
  llmMs: number
}

export type Start = {
  query: string
  user: string
  model: string
  traceId: string
  thinking?: boolean
}

export type Done = {
  totalMs: number
  promptTok: number
  cachedTok: number
  compTok: number
  rawBytes: number
  projBytes: number
  denied: boolean
  incomplete: boolean
  toolsUsed: string[]
  navigated: boolean
  proposed: boolean
  /** 実際に適用された絞り込み条件。頼んでいない条件が付いていないか確かめるため。 */
  filters?: Record<string, string>
}

export type Order = {
  orderId: string
  customerId: string
  customerName: string
  status: string
  orderedAt: string
  totalAmount: number
}

export type Customer = {
  customerId: string
  name: string
  region: string
  status: string
}

export type AuditExecution = {
  execution_id: string
  created_at: string
  trace_id: string | null
  user_id: string
  role: string
  command: string
  arguments: Record<string, unknown>
  status_code: number
  ok: boolean
  error: string | null
}

export type AuditTrace = {
  trace_id: string
  created_at: string
  user_id: string
  role: string
  query: string
  outcome: string
  intent: string | null
  denied: boolean
  incomplete: boolean
  error: string | null
  step_count: number
  total_ms: number
  prompt_tok: number
  cached_tok: number
}

const USER_HEADER = 'X-Nlops-User-Id'

async function get<T>(path: string, userId: string): Promise<T> {
  const res = await fetch(path, { headers: { [USER_HEADER]: userId } })
  const body = (await res.json()) as T & { error?: string }
  if (!res.ok) {
    throw new Error(body.error ?? `リクエストに失敗しました (${res.status})`)
  }
  return body
}

export async function fetchUsers(): Promise<{ items: User[] }> {
  const res = await fetch('/api/users')
  if (!res.ok) {
    throw new Error(`/api/users が ${res.status} を返しました`)
  }
  return (await res.json()) as { items: User[] }
}

// 空文字の絞り込みは送らない。サービス側で「空文字に一致する行」を
// 探しに行かせないため。
function queryOf(params: Record<string, string | number>): string {
  const q = new URLSearchParams()
  for (const [k, v] of Object.entries(params)) {
    if (v === '' || v === undefined || v === null) continue
    q.set(k, String(v))
  }
  return q.toString()
}

export function fetchOrders(
  userId: string,
  params: Record<string, string | number>,
): Promise<{ items: Order[]; count: number; hasMore: boolean }> {
  return get(`/api/orders?${queryOf(params)}`, userId)
}

export function fetchCustomers(
  userId: string,
  params: Record<string, string | number>,
): Promise<{ items: Customer[]; count: number; hasMore: boolean }> {
  return get(`/api/customers?${queryOf(params)}`, userId)
}

/**
 * 人間が確認した更新操作を実行する。
 *
 * LLM はこの経路を呼べない。画面からの明示的な操作だけが実行に至る。
 * 実行できるかどうかの業務判断はサービス側にあるので、
 * 断られた場合はその理由をそのまま表示する。
 */
export async function executeCommand(
  userId: string,
  command: string,
  args: Record<string, unknown>,
  traceId?: string,
): Promise<void> {
  const res = await fetch('/api/commands/execute', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', [USER_HEADER]: userId },
    // traceId を送ることで、どの会話に対する承認だったかが監査に残る。
    body: JSON.stringify({ command, arguments: args, traceId }),
  })
  const body = (await res.json()) as { error?: string }
  if (!res.ok) {
    throw new Error(body.error ?? `実行に失敗しました (${res.status})`)
  }
}

export function fetchAuditExecutions(
  userId: string,
): Promise<{ items: AuditExecution[]; count: number }> {
  return get('/api/audit/executions?limit=100', userId)
}

export function fetchAuditTraces(
  userId: string,
): Promise<{ items: AuditTrace[]; count: number }> {
  return get('/api/audit/traces?limit=100', userId)
}

export type AskHandlers = {
  onStart: (start: Start) => void
  onStep: (step: Step) => void
  onNavigate: (nav: Navigation) => void
  onProposal: (p: Proposal) => void
  onAnswer: (answer: string) => void
  onDone: (done: Done) => void
  onError: (message: string) => void
}

/**
 * Tool Loop の進捗を SSE で受け取る。
 *
 * EventSource は POST できないので fetch のストリームを自前で解析する。
 * Loop は 1 要求あたり数秒かかるため、完了を待たずステップ単位で描画する。
 */
export async function streamAsk(
  query: string,
  userId: string,
  handlers: AskHandlers,
  signal?: AbortSignal,
  thinking = false,
): Promise<void> {
  const res = await fetch('/api/ask', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', [USER_HEADER]: userId },
    body: JSON.stringify({ query, thinking }),
    signal,
  })
  if (!res.ok || !res.body) {
    const text = await res.text().catch(() => '')
    handlers.onError(text || `問い合わせに失敗しました (${res.status})`)
    return
  }

  const reader = res.body.getReader()
  const decoder = new TextDecoder()
  let buffer = ''

  for (;;) {
    const { done, value } = await reader.read()
    if (done) break
    buffer += decoder.decode(value, { stream: true })

    // SSE はイベントを空行で区切る。
    let sep = buffer.indexOf('\n\n')
    while (sep !== -1) {
      dispatch(buffer.slice(0, sep), handlers)
      buffer = buffer.slice(sep + 2)
      sep = buffer.indexOf('\n\n')
    }
  }
}

function dispatch(block: string, handlers: AskHandlers): void {
  let event = 'message'
  const dataLines: string[] = []
  for (const line of block.split('\n')) {
    if (line.startsWith('event: ')) event = line.slice(7)
    else if (line.startsWith('data: ')) dataLines.push(line.slice(6))
  }
  if (dataLines.length === 0) return

  let payload: unknown
  try {
    payload = JSON.parse(dataLines.join('\n'))
  } catch {
    return
  }

  switch (event) {
    case 'start':
      handlers.onStart(payload as Start)
      break
    case 'step':
      handlers.onStep(payload as Step)
      break
    case 'proposal':
      handlers.onProposal(payload as Proposal)
      break
    case 'navigate':
      handlers.onNavigate(payload as Navigation)
      break
    case 'answer':
      handlers.onAnswer((payload as { answer: string }).answer)
      break
    case 'done':
      handlers.onDone(payload as Done)
      break
    case 'error':
      handlers.onError((payload as { message: string }).message)
      break
    default:
      break
  }
}
