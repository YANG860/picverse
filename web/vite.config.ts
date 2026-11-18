import path from 'node:path'
import { fileURLToPath } from 'node:url'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'
import { defineConfig } from 'vite'

const rootDir = path.dirname(fileURLToPath(import.meta.url))

// https://vite.dev/config/
export default defineConfig({
  plugins: [react(), tailwindcss()],
  resolve: {
    alias: {
      '@': path.resolve(rootDir, 'src'),
      // 复用后端 buf generate 产出的 ConnectRPC/protobuf TS 代码（web/src/gen，纯 ESM js+d.ts）
      '@gen': path.resolve(rootDir, 'src/gen'),
    },
  },
  server: {
    proxy: {
      // ConnectRPC 服务路径同源代理到 BFF（BFF 未开 CORS，浏览器经代理访问）。
      // 默认远程测试服务器；本地联调用 VITE_BFF_PROXY_TARGET=http://127.0.0.1:8084 覆盖。
      '/bff.v1.BFFService': {
        target: process.env.VITE_BFF_PROXY_TARGET ?? 'http://106.53.172.186:8084',
        changeOrigin: true,
      },
    },
  },
})
