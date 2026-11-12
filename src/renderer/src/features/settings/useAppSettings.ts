import { useEffect, useLayoutEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import type { AppSettings, ResolvedTheme } from '../../../../shared/types'
import { useNotify } from '../../ui/use-notify'
import i18n from '../../i18n'
import {
  applyResolvedTheme,
  getSystemPrefersDark,
  resolveTheme,
  subscribeSystemPrefersDark
} from '../../theme'

type Preferences = Required<
  Pick<
    AppSettings,
    | 'language'
    | 'themePreference'
    | 'terminalFontFamily'
    | 'terminalFontSize'
    | 'mcpIdleTimeoutMinutes'
    | 'mcpMaxSessions'
    | 'mcpPermissionMode'
    | 'permissionPolicy'
  >
>

const DEFAULTS: Preferences = {
  language: 'zh',
  themePreference: 'system',
  terminalFontFamily: 'Hack',
  terminalFontSize: 14,
  mcpIdleTimeoutMinutes: 10,
  mcpMaxSessions: 8,
  mcpPermissionMode: 'external',
  permissionPolicy: 'ask'
}

function preferences(settings: AppSettings): Preferences {
  return {
    language: settings.language,
    themePreference: settings.themePreference,
    terminalFontFamily: settings.terminalFontFamily,
    terminalFontSize: settings.terminalFontSize,
    mcpIdleTimeoutMinutes: settings.mcpIdleTimeoutMinutes,
    mcpMaxSessions: settings.mcpMaxSessions,
    mcpPermissionMode: settings.mcpPermissionMode === 'local' ? 'local' : 'external',
    permissionPolicy: settings.permissionPolicy ?? 'ask'
  }
}

/** Saved preference baseline and optimistic UI live here, outside the host query pilot. */
export function useAppSettings(): {
  values: Preferences
  resolvedTheme: ResolvedTheme
  change: <K extends keyof Preferences>(key: K, next: Preferences[K]) => void
} {
  const { t } = useTranslation()
  const notify = useNotify()
  const [values, setValues] = useState<Preferences>(DEFAULTS)
  const [systemDark, setSystemDark] = useState(getSystemPrefersDark)
  const confirmed = useRef<Preferences>({ ...DEFAULTS })
  const revisions = useRef<Partial<Record<keyof Preferences, number>>>({})
  const savedRevisions = useRef<Partial<Record<keyof Preferences, number>>>({})
  const writes = useRef<Partial<Record<keyof Preferences, Promise<void>>>>({})
  const lifetime = useRef(0)
  const mounted = useRef(false)
  const fontTimer = useRef<ReturnType<typeof setTimeout> | null>(null)

  useLayoutEffect(() => {
    mounted.current = true
    lifetime.current += 1
    return () => {
      mounted.current = false
      lifetime.current += 1
      if (fontTimer.current !== null) clearTimeout(fontTimer.current)
    }
  }, [])

  useEffect(() => {
    const generation = lifetime.current
    void window.api.settings
      .get()
      .then((settings) => {
        if (generation !== lifetime.current) return
        const saved = preferences(settings)
        for (const key of Object.keys(saved) as (keyof Preferences)[]) {
          if (!savedRevisions.current[key]) Object.assign(confirmed.current, { [key]: saved[key] })
        }
        setValues((current) => {
          const next = { ...saved }
          for (const key of Object.keys(saved) as (keyof Preferences)[]) {
            if (revisions.current[key]) Object.assign(next, { [key]: current[key] })
          }
          return next
        })
      })
      .catch(() => {
        // Defaults remain usable if configuration cannot be read.
      })
  }, [])

  const resolvedTheme = resolveTheme(values.themePreference, systemDark)
  useEffect(() => {
    applyResolvedTheme(resolvedTheme)
  }, [resolvedTheme])
  useEffect(() => {
    if (values.themePreference !== 'system') return
    setSystemDark(getSystemPrefersDark())
    return subscribeSystemPrefersDark(setSystemDark)
  }, [values.themePreference])
  useEffect(() => {
    void i18n.changeLanguage(values.language)
  }, [values.language])

  const change = <K extends keyof Preferences>(key: K, next: Preferences[K]): void => {
    if (!mounted.current) return
    const revision = (revisions.current[key] ?? 0) + 1
    revisions.current[key] = revision
    const generation = lifetime.current
    setValues((current) => ({ ...current, [key]: next }))
    const persist = async (): Promise<void> => {
      if (generation !== lifetime.current) return
      try {
        const saved = preferences(await window.api.settings.set({ [key]: next }))
        if (generation !== lifetime.current) return
        confirmed.current[key] = saved[key]
        savedRevisions.current[key] = revision
        if (revisions.current[key] === revision) {
          setValues((current) => ({ ...current, [key]: saved[key] }))
        }
      } catch {
        if (generation !== lifetime.current || revisions.current[key] !== revision) return
        setValues((current) => ({ ...current, [key]: confirmed.current[key] }))
        notify(t('auth.connectionFailed'))
      }
    }
    const enqueue = (): void => {
      // Serialize each preference's writes so the persisted value follows user
      // intent even when two IPC requests would otherwise finish out of order.
      const previous = writes.current[key] ?? Promise.resolve()
      writes.current[key] = previous.then(persist)
    }
    if (key === 'terminalFontSize') {
      if (fontTimer.current !== null) clearTimeout(fontTimer.current)
      fontTimer.current = setTimeout(() => {
        fontTimer.current = null
        enqueue()
      }, 300)
    } else {
      enqueue()
    }
  }

  return { values, resolvedTheme, change }
}
