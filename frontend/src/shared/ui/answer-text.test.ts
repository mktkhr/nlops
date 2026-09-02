import { expect, test } from 'vite-plus/test'
import { parseAnswer } from './answer-text'

test('箇条書きと段落を分ける', () => {
  // 実際の回答で唯一出てくる記法 (94 件中 55%)。
  const got = parseAnswer(
    '未払いの請求書は以下の通りです。\n\n*   INV-51843、79,800 円\n*   INV-55528、3,800 円',
  )
  expect(got).toEqual([
    { kind: 'p', text: '未払いの請求書は以下の通りです。' },
    { kind: 'ul', items: ['INV-51843、79,800 円', 'INV-55528、3,800 円'] },
  ])
})

test('- と + も箇条書きとして扱う', () => {
  expect(parseAnswer('- あ\n+ い')).toEqual([{ kind: 'ul', items: ['あ', 'い'] }])
})

test('段落の中の改行は保つ', () => {
  // 金額や ID を並べた行が繋がると読めなくなる。
  expect(parseAnswer('注文 O-1002\n金額 42,000 円')).toEqual([
    { kind: 'p', text: '注文 O-1002\n金額 42,000 円' },
  ])
})

test('箇条書きの前後に段落が挟まっても崩れない', () => {
  expect(parseAnswer('前\n* あ\n* い\n後')).toEqual([
    { kind: 'p', text: '前' },
    { kind: 'ul', items: ['あ', 'い'] },
    { kind: 'p', text: '後' },
  ])
})

test('扱わない記法は文字としてそのまま残す', () => {
  // 表や見出しは実測で 0% だったので整形しない。
  // ただし**消えてはいけない**。読めなくなるより、整形されないほうがよい。
  const md = '| ID | 名前 |\n| --- | --- |\n| C001 | 田中 |'
  const got = parseAnswer(md)
  expect(got).toEqual([{ kind: 'p', text: md }])
})

test('掛け算の * を箇条書きにしない', () => {
  // 行頭でなければ箇条書きではない。
  expect(parseAnswer('3 * 4 = 12')).toEqual([{ kind: 'p', text: '3 * 4 = 12' }])
})

test('空文字は何も生まない', () => {
  expect(parseAnswer('')).toEqual([])
})
