-- 監査とトレース。
--
-- 業務データとは分離した schema に置く。ここに入るのは「システムが何をしたか」で
-- あって業務データそのものではない。所有するのは BFF (リクエストの入口) で、
-- 各マイクロサービスはこの schema を参照しない。
--
-- 記録する理由は 2 つ。
--   1. LLM が業務データの変更を提案する以上、誰がいつ何を承認したかが残らないと
--      運用できない。拒否された試行も残す (何をやろうとしたかが監査の本体)。
--   2. 実際の利用トレースがゴールデンセットの材料になる。

CREATE SCHEMA IF NOT EXISTS audit;

DROP TABLE IF EXISTS audit.command_executions;
DROP TABLE IF EXISTS audit.trace_steps;
DROP TABLE IF EXISTS audit.traces;

-- 1 リクエスト = 1 行。
CREATE TABLE audit.traces (
    trace_id     uuid PRIMARY KEY,
    created_at   timestamptz NOT NULL DEFAULT now(),
    user_id      text NOT NULL,
    role         text NOT NULL,
    query        text NOT NULL,
    model        text NOT NULL,
    mode         text NOT NULL,
    intent       text,               -- Intent Gate の判定 (navigate / tool / write)
    outcome      text NOT NULL,      -- answer / navigate / propose / error
    answer       text,
    denied       boolean NOT NULL,   -- 途中で 403 に当たったか
    incomplete   boolean NOT NULL,   -- 最大ステップ数で打ち切ったか
    error        text,
    step_count   int    NOT NULL,
    total_ms     double precision NOT NULL,
    intent_ms    double precision NOT NULL,
    answer_ms    double precision NOT NULL,
    prompt_tok   int NOT NULL,
    cached_tok   int NOT NULL,
    comp_tok     int NOT NULL,
    raw_bytes    int NOT NULL,       -- Projection 前の API レスポンス合計
    proj_bytes   int NOT NULL        -- LLM へ渡した合計
);

-- Loop の 1 ステップ = 1 行。§21 の「どの Tool をどの引数で呼んだか」を残す。
CREATE TABLE audit.trace_steps (
    trace_id   uuid NOT NULL REFERENCES audit.traces(trace_id) ON DELETE CASCADE,
    iteration  int  NOT NULL,
    kind       text NOT NULL,   -- tool / navigate / propose / finish
    tool       text,
    arguments  jsonb,
    status     int,
    denied     boolean NOT NULL DEFAULT false,
    error      text,
    result     jsonb,           -- Projection 済みのもの。生レスポンスは残さない
    llm_ms     double precision NOT NULL,
    prompt_tok int NOT NULL,
    cached_tok int NOT NULL,
    comp_tok   int NOT NULL,
    PRIMARY KEY (trace_id, iteration)
);

-- 更新操作の承認と実行。拒否された試行も 1 行として残す。
--
-- idempotency_key はこの表を**二重実行のロックとして使う**ためのもの。
-- 実行の記録先と排他の置き場所を分けると、記録が入る前に 2 本目が走れてしまう。
-- 実行前に行を確保 (status_code = 0) し、完了時に結果で更新する。
CREATE TABLE audit.command_executions (
    execution_id uuid PRIMARY KEY,
    created_at   timestamptz NOT NULL DEFAULT now(),
    -- traces への外部キーは張らない。
    -- 監査は best-effort (書けなくても操作は止めない) と決めてあるのに、
    -- ここを FK にすると**トレース記録が落ちただけで更新が実行できなくなる**。
    -- 実行の記録のほうが、会話との紐付けより優先度が高い。
    trace_id     uuid,
    user_id      text NOT NULL,   -- 承認して実行した人
    role         text NOT NULL,
    command      text NOT NULL,
    arguments    jsonb NOT NULL,
    status_code  int  NOT NULL,   -- 0 は「実行中」
    ok           boolean NOT NULL,
    error        text,            -- 業務ルールや権限で拒否された理由
    before       jsonb,           -- 実行直前に読んだ対象の状態
    result       jsonb,           -- サービスが返した実行後の状態
    idempotency_key text          -- 同じ承認を 2 回実行させないための鍵
);

CREATE INDEX ON audit.traces (created_at DESC);
CREATE INDEX ON audit.traces (user_id, created_at DESC);
CREATE INDEX ON audit.command_executions (created_at DESC);
CREATE INDEX ON audit.command_executions (command, created_at DESC);

-- 二重実行を止めるのはこの一意制約。アプリ側の「事前に確認してから実行」は
-- 同時に来た 2 本を止められない (両方が「まだ無い」を見る)。
CREATE UNIQUE INDEX ON audit.command_executions (idempotency_key)
    WHERE idempotency_key IS NOT NULL;
