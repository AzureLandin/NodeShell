import { createContext } from 'react'

export interface NotificationOptions {
  tone?: string
  /** Milliseconds before dismissal; 0 (the default) requires manual dismissal. */
  timeout?: number
}

export type Notify = (message: string, options?: NotificationOptions) => void

export const NotificationContext = createContext<Notify | null>(null)
