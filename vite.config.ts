import react from '@vitejs/plugin-react'
import { defineConfig } from 'vite'

// https://vite.dev/config/
export default defineConfig(async ({ command }) => {
  const plugins = [react()]
  if (command === 'build') {
    const { UgosViteBuilder } = await import('@ugreen-nas/builder-open')
    const ugosBuilder = new UgosViteBuilder({
      windowConfig: {
        width: 1200,
        height: 800,
        minWidth: 960,
        minHeight: 640,
        resizable: true,
      },
      getIgnoreFolder: (config: unknown, _isElectron: boolean) => config,
    })
    // The official builder is required for release artifacts; its version hook requires Git HEAD.
    plugins.push(...ugosBuilder.pluginEntry())
  }
  return {
    plugins,
    server: {
      proxy: {
        '/api': 'http://127.0.0.1:21010',
      },
    },
  }
})
