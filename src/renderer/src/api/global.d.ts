import type { ElectronApi } from '../../../shared/types'

declare global {
  interface Window {
    api: ElectronApi
  }
}

export {}
