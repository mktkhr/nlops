/**
 * iOS Safari が入力欄へのフォーカスで勝手に拡大するのを止める。
 *
 * Safari は「実効の文字サイズが 16px 未満」の入力欄にフォーカスすると、
 * 読める大きさになるまで拡大する。**CSS で 16px にしても足りない。**
 * 利用者が Safari の拡大率を下げていれば、16px の CSS も実効では
 * それを下回るので、こちら側の CSS では条件を満たせない。
 * (実測: font-size は変更前から 16px で、それでも拡大は起きていた)
 *
 * viewport に `maximum-scale=1` を書けば自動拡大は止まるが、
 * **恒久的に書くとピンチズームまで殺す**。拡大したい人が拡大できなくなる
 * ほうが害が大きいので、**入力欄にフォーカスしている間だけ**に限る。
 * 離れたら元に戻すので、読むときの拡大は今までどおり効く。
 *
 * 触れる端末だけに入れる。マウスの画面では自動拡大が起きないので不要。
 */
const BASE = 'width=device-width, initial-scale=1.0'
const LOCKED = `${BASE}, maximum-scale=1.0`

export function preventFocusZoom(): () => void {
  if (typeof window === 'undefined') return () => {}
  if (!window.matchMedia('(pointer: coarse)').matches) return () => {}

  const meta = document.querySelector<HTMLMetaElement>('meta[name="viewport"]')
  if (!meta) return () => {}

  // 入力できる要素だけを見る。ボタンやリンクのフォーカスでは拡大は起きない。
  const isTextEntry = (t: EventTarget | null) =>
    t instanceof HTMLElement &&
    (t.tagName === 'TEXTAREA' ||
      (t.tagName === 'INPUT' &&
        !['checkbox', 'radio', 'button', 'submit', 'hidden'].includes(
          (t as HTMLInputElement).type,
        )))

  const lock = (e: FocusEvent) => {
    if (isTextEntry(e.target)) meta.content = LOCKED
  }
  const unlock = (e: FocusEvent) => {
    if (!isTextEntry(e.target)) return
    // すぐ戻すと、キーボードが閉じる前の拡大が一瞬効くことがある。
    // 次のフレームまで待ってから戻す。
    requestAnimationFrame(() => {
      meta.content = BASE
    })
  }

  document.addEventListener('focusin', lock)
  document.addEventListener('focusout', unlock)
  return () => {
    document.removeEventListener('focusin', lock)
    document.removeEventListener('focusout', unlock)
    meta.content = BASE
  }
}
