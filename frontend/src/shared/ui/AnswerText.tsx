import Box from '@mui/material/Box'
import Typography from '@mui/material/Typography'
import type { ReactNode } from 'react'
import { parseAnswer, splitInline } from './answer-text'

/**
 * LLM の回答を描く。
 *
 * `whiteSpace: pre-wrap` のままだと「*   請求書ID: …」の `*` がそのまま出て
 * 読みにくい。解釈する範囲と、その理由は answer-text.ts に書いてある。
 */
export function AnswerText({ text }: { text: string }) {
  return (
    <>
      {parseAnswer(text).map((b, i) =>
        b.kind === 'ul' ? (
          <Box key={i} component="ul" sx={{ my: 1, pl: 3, '& li': { mb: 0.5 } }}>
            {b.items.map((item, j) => (
              <Typography key={j} component="li" sx={{ lineHeight: 1.8 }}>
                {inline(item)}
              </Typography>
            ))}
          </Box>
        ) : (
          <Typography key={i} sx={{ lineHeight: 1.8, whiteSpace: 'pre-wrap' }}>
            {inline(b.text)}
          </Typography>
        ),
      )}
    </>
  )
}

function inline(text: string): ReactNode[] {
  return splitInline(text).map((part, i) => {
    if (part.kind === 'strong') {
      return (
        <Box key={i} component="strong" sx={{ fontWeight: 700 }}>
          {part.text}
        </Box>
      )
    }
    if (part.kind === 'code') {
      return (
        <Box
          key={i}
          component="code"
          sx={{
            fontFamily: 'monospace',
            fontSize: '0.9em',
            bgcolor: 'action.hover',
            px: 0.5,
            borderRadius: 0.5,
          }}
        >
          {part.text}
        </Box>
      )
    }
    return part.text
  })
}
