import { defineConfig, loadEnv } from 'vite'

// The explorer (index.html) and the chat (chat.html) read from a node's API.
// In dev, requests go to the gl node that compose.yaml publishes on
// localhost:8443, or to the node at EXPLORER_API.
export default defineConfig(({ mode }) => ({
  build: {
    rollupOptions: { input: { explorer: 'index.html', chat: 'chat.html' } },
  },
  server: {
    proxy: { '/v1': loadEnv(mode, '.', '').EXPLORER_API || 'http://localhost:8443' },
  },
}))
