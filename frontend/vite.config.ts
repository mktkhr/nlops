import react from '@vitejs/plugin-react'
import { defineConfig, lazyPlugins } from 'vite-plus'

// https://vite.dev/config/
export default defineConfig({
  fmt: {},
  lint: {
    "plugins": [
      "react",
      "typescript",
      "oxc"
    ],
    "rules": {
      "react/rules-of-hooks": "error",
      "react/only-export-components": [
        "warn",
        {
          "allowConstantExport": true
        }
      ],
      "vite-plus/prefer-vite-plus-imports": "error"
    },
    "options": {
      "typeAware": true,
      "typeCheck": true
    },
    "jsPlugins": [
      {
        "name": "vite-plus",
        "specifier": "vite-plus/oxlint-plugin"
      }
    ]
  },
  plugins: lazyPlugins(() => [react()]),
  server: {
    // Tailscale 経由で別端末から開けるようにする。
    host: true,
    // MagicDNS 名 (*.ts.net) で開いても Vite にブロックされないようにする。
    allowedHosts: ['.ts.net'],
    // /api の転送先。既定は compose の Nginx (make up で立つ)。
    // 開発用 BFF を直接叩くなら NLOPS_API=http://127.0.0.1:8080 を指定する。
    proxy: {
      '/api': {
        target: process.env.NLOPS_API ?? 'http://127.0.0.1:8081',
        changeOrigin: true,
      },
    },
  },
})
