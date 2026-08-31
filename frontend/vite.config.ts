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
    // /api は BFF へ回す。本番では Nginx が同じ役割を担う。
    proxy: {
      '/api': {
        target: 'http://127.0.0.1:8080',
        changeOrigin: true,
      },
    },
  },
})
