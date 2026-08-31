# P3 スパイク結果

コード投資前の関門として測った内容と、そこから設計へ反映した判断。

計測環境: RTX 4080 SUPER 16GB / llama.cpp + llama-swap (:11435) / temperature 0

---

## S1. json_schema による制約デコードは成立する

llama.cpp の OpenAI 互換エンドポイントは `response_format.json_schema` を受け付け、
GBNF へ変換して出力を拘束する。`anyOf` による「Tool ごとに引数スキーマを固定する」
構成も通る。

初手 Tool 選定 160 コール (4 構成 × 40 ケース) で **JSON 追従率 100%**。

→ §17「Structured Output で制約する」はプロンプトでの依頼ではなく、
文法レベルで実現できる。設計方針をそのまま採用してよい。

## S2. Qwen3.6 は thinking モデル。無効化しないと Tool Loop が破綻する

既定では `reasoning_content` に長い思考を吐き、`content` が空のまま `max_tokens` に到達する。
Tool Loop は 1 要求あたり数回 LLM を呼ぶので、これを止めないとレイテンシが実用外になる。

| 方法 | 結果 |
|---|---|
| `chat_template_kwargs: {"enable_thinking": false}` | **有効** |
| `reasoning_effort: "none"` | **有効** |
| ユーザーメッセージ末尾に `/no_think` | **無効** (思考を続ける) |

→ `pkg/llm` の `DisableThinking` で前 2 者を常に送る。

## S3. モデル階段 — 初手 Tool 選定 (40 ケース)

`one_stage` = 全 24 Tool を提示 / `two_stage` = サービス選定 → そのサービスの Tool のみ提示。

| モデル | 構成 | JSON 追従 | tool 的中 | args 一致 |
|---|---|--:|--:|--:|
| Qwen3.6-35B-A3B UD-Q4_K_XL (ncmoe=17) | one_stage | 100% | **100%** | **100%** |
| Qwen3.6-35B-A3B UD-Q4_K_XL | two_stage | 100% | **100%** | **100%** |
| Qwen3.6-35B-A3B UD-IQ4_XS (ncmoe=10) | one_stage | 100% | **100%** | **100%** |
| Qwen3.6-35B-A3B UD-IQ4_XS | two_stage | 100% | **100%** | **100%** |
| Qwen3.6-35B-A3B UD-IQ3_S (オフロードなし) | one_stage | 100% | 98% | 98% |
| Qwen3.6-35B-A3B UD-IQ3_S | two_stage | 100% | 98% | 98% |
| Gemma 4 12B (QAT q4_0) | one_stage | 100% | **100%** | 98% |
| Gemma 4 12B | two_stage | 100% | **100%** | 98% |
| gpt-oss 20B (MXFP4) | one_stage | 88% | 88% | 100% |
| gpt-oss 20B | two_stage | 68% | 68% | 100% |
| Qwen3.5 9B Q4_K_M | one_stage | 100% | 80% | 82% |
| Qwen3.5 9B | two_stage | 100% | 90% | 88% |
| Qwen3.5 4B Q4_K_M | one_stage | 100% | 88% | 90% |
| Qwen3.5 4B | two_stage | 100% | 80% | 82% |

読み取れること:

1. **IQ4_XS で天井に届く。** Q4_K_XL との差はゼロ。このタスクでは
   IQ4_XS より上へ量子化ビットを積んでも意味がない。
2. **IQ3_S への 1 段落としで 2% 落ちる。** 失敗の中身はどちらも
   「顧客 ID を捏造して検索ステップを飛ばす」型 (`{"customer_id":"CUST-001"}` /
   `{"customer_id":"田中太郎"}`)。精度タスクでの低ビット量子化の影響が
   最も出やすい箇所に出ている。
3. **Gemma 4 12B が 100% を出した。** llama-swap の構成メモには
   「tool-call ネイティブハンドラがなく Generic フォールバックになる系列」とあるが、
   本設計は tools API ではなく `response_format` の制約デコードを使うため、
   ネイティブ tool-call 対応の有無が影響しない。**モデル選択の自由度が上がる**
   という設計上の副次効果。
4. **gpt-oss 20B の失敗は「モデルが弱い」ではない。** 全失敗が
   `max_tokens 到達 (content 空)` で、harmony 形式の reasoning が
   `reasoning_effort:"none"` で止まらず出力枠を食い切っている。
   引数を出せた場合の args 一致は 100%。**設定の問題**として切り分ける。
5. **モデルサイズと精度は単調ではない。** Qwen3.5 9B (80%) が 4B (88%) を
   one_stage で下回った。「大きい方が良い」を前提にしてはいけない。

## S4. 1 段階 vs 2 段階ルーティング

精度はどのモデルでも実質同じだった。差が出たのは **prompt prefix cache** の効き方。

| 構成 | prompt tok | cache 率 |
|---|--:|--:|
| one_stage | 1548 | **95〜97%** |
| two_stage | 957〜1049 | 70〜77% |

機序:

- `one_stage` は system prompt が「全 24 Tool」で**常に固定**。
  prefix 全体がキャッシュに乗るため、投入トークンは多いが prefill はほぼ無料になる。
- `two_stage` は Stage 2 の Tool 一覧が Stage 1 の出力によって変わるため、
  **要求ごとに prefix が変わりキャッシュが効かない**。
  加えて LLM 往復が 1 回増える。

→ 「context を減らすために 2 段階にする」は、prefix cache のある環境では
**トークン数は減るが実効コストは下がらない**。§16 の段階的絞り込みは
Tool 数がキャッシュに乗らない規模になって初めて効く。PoC 規模 (24 Tool) では
1 段階を既定とする。

> レイテンシの絶対値は、モデル階段の測定中に並行して別作業 (ビルド・DB 操作) を
> していたため信用できない。ncmoe による CPU オフロード構成は CPU 負荷の影響を
> 受ける。精度とトークン/キャッシュの数値は影響を受けない。

## S5. strict / loose スキーマ

Tool ごとに引数スキーマを固定する `strict` と、tool 名だけ enum で拘束する `loose` を比較。

- 精度差なし (どちらも同じケースで失敗)
- `strict` は anyOf の文法が大きくなる分わずかに遅い

→ `strict` の価値は精度ではなく **防御** にある。存在しない引数名や enum 外の値を
構造的に生成不能にできるので、既定は `strict` とする。

## S6. スパイクから設計へ反映した 3 点

### (1) Tool を 1 つも実行しないうちの `finish` を文法で禁止

Tool Loop の初回反復でいきなり `next:"finish"` を選び、何も調べずに
「情報がありません」と答える失敗を実測した。プロンプトで依頼するのではなく、
Tool 実行が 1 回成立するまで `finish` の分岐自体をスキーマから外す
(`prompt.LoopSchema(tools, strictArgs, allowFinish)`)。

### (2) 未解決 ID の差し戻し (Executor 側の防御)

IQ3_S の失敗 2 件はいずれも **存在しない ID の捏造**だった。
`strict` スキーマでは防げない (customer_id は正当な string 引数なので)。
そこで Executor 側に、ユーザー入力にも過去の Tool 結果にも現れていない
ID 値を検出して実行前に差し戻す guard を入れた。

**スキーマで防げるのは「形」だけで、「出所」は Executor が見るしかない。**

### (3) context 削減は履歴圧縮ではなく Tool 結果の Projection で行う

`--cache-reuse 256` が有効なため、履歴を途中で要約圧縮すると prefix が壊れて
全再 prefill になる。したがって Orchestrator は履歴を append のみとし、
削減は Tool 結果を追加する時点の Projection だけで行う。

Orchestrator が守る制約:

- system prompt と Tool 定義の順序を固定する (map の iteration 順で並べない)
- prefix に現在時刻・ランダム ID・リクエスト ID を入れない
- 履歴は append のみ

## S7. 実験設計の不備と修正

初版のモックサービスは、カタログの Projection 投影先とまったく同じフィールドしか
返していなかった。そのため Projection の削減率が 1% にしかならず、
**「Response Projection が必要」という主張自体が検証できていなかった。**

実際の業務 API が持つ冗長性 (監査カラム・HATEOAS リンク・テナント ID・
ページングメタ・ETag) を返すよう修正したところ、削減率は **約 80%** になった。
