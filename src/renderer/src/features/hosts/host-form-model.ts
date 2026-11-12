import type { AuthMethod, HostInput } from '../../../../shared/types'

export type HostFormInitial = Partial<HostInput> & { id?: string }

export interface HostFormDraft {
  name: string
  host: string
  port: string
  username: string
  authMethod: AuthMethod
  privateKeyPath: string
  password: string
}

export interface HostFormSubmit {
  input: HostInput
  /** Ephemeral credential; never part of HostInput, defaults from a host, or persisted JSON. */
  password?: string
}

export type HostFormErrors = Partial<Record<keyof HostFormDraft, string>>

export function hostFormDefaults(initial?: HostFormInitial): HostFormDraft {
  return {
    name: initial?.name ?? '',
    host: initial?.host ?? '',
    port: String(initial?.port ?? 22),
    username: initial?.username ?? '',
    authMethod: initial?.authMethod ?? 'password',
    privateKeyPath: initial?.privateKeyPath ?? '',
    password: ''
  }
}

/** Ports are decimal integers, not JavaScript numeric expressions or fractional values. */
export function parseHostPort(value: string): number | undefined {
  const normalized = value.trim()
  if (!/^\d+$/.test(normalized)) return undefined
  const port = Number(normalized)
  return Number.isInteger(port) && port >= 1 && port <= 65535 ? port : undefined
}

export function validateHostDraft(draft: HostFormDraft, initial?: HostFormInitial): HostFormErrors {
  const errors: HostFormErrors = {}
  if (!draft.name.trim()) errors.name = 'form.nameRequired'
  if (!draft.host.trim()) errors.host = 'form.hostRequired'
  if (parseHostPort(draft.port) === undefined) errors.port = 'form.portInvalid'
  if (!draft.username.trim()) errors.username = 'form.usernameRequired'
  if (draft.authMethod === 'password') {
    if (!initial?.id && !draft.password) errors.password = 'form.passwordRequired'
  } else if (!draft.privateKeyPath.trim() && !(initial?.id && initial.privateKeyPath)) {
    errors.privateKeyPath = 'form.privateKeyRequired'
  }
  return errors
}

/** Call only with a validated draft; hidden authentication fields never enter the payload. */
export function hostFormSubmission(draft: HostFormDraft): HostFormSubmit {
  const port = parseHostPort(draft.port)
  if (port === undefined) throw new Error('Invalid host port')
  return {
    input: {
      name: draft.name.trim(),
      host: draft.host.trim(),
      port,
      username: draft.username.trim(),
      authMethod: draft.authMethod,
      ...(draft.authMethod === 'privateKey' && draft.privateKeyPath.trim()
        ? { privateKeyPath: draft.privateKeyPath.trim() }
        : {})
    },
    ...(draft.authMethod === 'password' && draft.password ? { password: draft.password } : {})
  }
}
