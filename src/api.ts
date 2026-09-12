import UGOSCore from '@ugreen-nas/core'
import cloudWindow from '@ugreen-nas/core/cloudWindow'

let ugToken: string | null = null
let authReady: Promise<void> | null = null
export const ugosReady = UGOSCore.init()

async function initializeUGOSAuth() {
  if (authReady) return authReady

  authReady = (async () => {
    await ugosReady
    try {
      const info = await cloudWindow.useCapacity('getThirdToken') as { third_token?: unknown } | null
      if (typeof info?.third_token === 'string' && info.third_token.length > 0) {
        ugToken = info.third_token
      }
    } catch {
      // The local Vite preview has no UGOS host. The backend's explicit dev mode handles it.
    }
  })()

  return authReady
}

export async function apiFetch(input: RequestInfo | URL, init: RequestInit = {}) {
  await initializeUGOSAuth()
  const headers = new Headers(init.headers)
  if (ugToken) headers.set('Ugreen-Ttk', ugToken)
  return fetch(input, { ...init, headers })
}
