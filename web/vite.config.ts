import { defineConfig, loadEnv } from 'vite'

// The explorer reads the network from a node's API. In dev, requests go to
// the gl node that compose.yaml publishes on localhost:8443, or to the node
// at EXPLORER_API.
export default defineConfig(({ mode }) => ({
  server: {
    proxy: { '/v1': loadEnv(mode, '.', '').EXPLORER_API || 'http://localhost:8443' },
  },
}))
