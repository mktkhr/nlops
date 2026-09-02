/**
 * LLM の回答に出てくる Markdown を、描画に必要な最小限だけ解釈する。
 *
 * **完全な Markdown パーサは入れていない。** 実際の回答 94 件を数えたところ、
 * 出てくるのは箇条書き (55%) だけで、太字・番号付き・コード・見出し・表・
 * リンク・HTML はすべて 0% だった。react-markdown を入れるとアシスタント画面の
 * チャンクが 22.4kB → 57.0kB gzip (2.5 倍) になる。
 * 1 種類の記法のために払う額ではない。
 *
 * 扱うのは箇条書き・段落・太字・インラインコードまで。
 * それ以外は**そのまま文字として残す** (表なら `| a | b |` が見える)。
 * 整形されないだけで、読めなくはならない。この境界はテストで固定してある。
 *
 * 描画は React の要素として組み立てる (HTML 文字列にしない)。
 * モデルの出力にも業務データにも、こちらの制御下にない文字列が混ざるため。
 */

/** 段落か箇条書き。 */
export type Block = { kind: 'p'; text: string } | { kind: 'ul'; items: string[] }

/** 行内の断片。 */
export type Inline = { kind: 'text' | 'strong' | 'code'; text: string }

const BULLET = /^\s*[*\-+]\s+(.*)$/

/** 回答をブロックに分ける。空行が段落の区切り。 */
export function parseAnswer(text: string): Block[] {
  const out: Block[] = []
  let items: string[] = []
  let para: string[] = []

  const flushList = () => {
    if (items.length > 0) {
      out.push({ kind: 'ul', items })
      items = []
    }
  }
  const flushPara = () => {
    if (para.length > 0) {
      // 段落の中の改行は保つ。金額や ID を並べた行が繋がると読めない。
      out.push({ kind: 'p', text: para.join('\n') })
      para = []
    }
  }

  for (const line of text.split('\n')) {
    const m = BULLET.exec(line)
    if (m) {
      flushPara()
      items.push(m[1])
      continue
    }
    if (line.trim() === '') {
      flushList()
      flushPara()
      continue
    }
    flushList()
    para.push(line)
  }
  flushList()
  flushPara()
  return out
}

// 閉じていない記号は拾わない。途中で切れた出力を壊さないため。
const INLINE = /(\*\*[^*\n]+\*\*|`[^`\n]+`)/g

/** 行内を太字・インラインコード・素の文字に分ける。入れ子は扱わない。 */
export function splitInline(text: string): Inline[] {
  const out: Inline[] = []
  for (const part of text.split(INLINE)) {
    if (part === '') continue
    if (part.length > 4 && part.startsWith('**') && part.endsWith('**')) {
      out.push({ kind: 'strong', text: part.slice(2, -2) })
    } else if (part.length > 2 && part.startsWith('`') && part.endsWith('`')) {
      out.push({ kind: 'code', text: part.slice(1, -1) })
    } else {
      out.push({ kind: 'text', text: part })
    }
  }
  return out
}
