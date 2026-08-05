import { createApi } from './adapter'
import { WailsBridge } from './bridge'
import type { ElectronApi } from '../../../shared/types'

/**
 * Installs the Wails-backed window.api adapter. In the Electron build the
 * preload exposes window.api before the renderer runs, so this is a no-op
 * there; in the Wails WebView no preload exists, so we install the adapter.
 */
export function installWailsApi(): void {
  if (window.api) return
  window.api = createApi(new WailsBridge()) as unknown as ElectronApi
}

installWailsApi()
