import { expect, test } from 'vite-plus/test'
import { splitInline } from './answer-text'

test('太字とインラインコードを切り出す', () => {
  expect(splitInline('合計 **990 件** です')).toEqual([
    { kind: 'text', text: '合計 ' },
    { kind: 'strong', text: '990 件' },
    { kind: 'text', text: ' です' },
  ])
  expect(splitInline('`order.search` を実行')).toEqual([
    { kind: 'code', text: 'order.search' },
    { kind: 'text', text: ' を実行' },
  ])
})

test('閉じていない記号はそのまま文字にする', () => {
  // 途中で切れた出力を壊さない。
  expect(splitInline('**閉じていない')).toEqual([{ kind: 'text', text: '**閉じていない' }])
  expect(splitInline('3 * 4')).toEqual([{ kind: 'text', text: '3 * 4' }])
})

test('空の強調は文字として残す', () => {
  expect(splitInline('****')).toEqual([{ kind: 'text', text: '****' }])
})
