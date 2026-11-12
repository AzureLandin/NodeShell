import { describe, expect, it } from 'vitest'
import {
  hostFormDefaults,
  hostFormSubmission,
  parseHostPort,
  validateHostDraft
} from '../../src/renderer/src/features/hosts/host-form-model'

const valid = {
  ...hostFormDefaults(),
  name: ' Server ',
  host: ' example.com ',
  username: ' root ',
  password: ' secret '
}

describe('host form model', () => {
  it.each(['', ' ', '0', '-1', '65536', '22.5', '22.0', '1e3', '0x16', 'NaN', 'Infinity'])(
    'rejects invalid decimal port %j',
    (value) => expect(parseHostPort(value)).toBeUndefined()
  )

  it.each([
    ['1', 1],
    ['65535', 65535],
    [' 0022 ', 22]
  ] as const)('parses decimal port %j', (value, expected) =>
    expect(parseHostPort(value)).toBe(expected)
  )

  it('keeps passwords and credential flags out of host-derived defaults', () => {
    expect(hostFormDefaults({ ...valid, port: 22, id: 'h1', credentialsSaved: true })).toEqual({
      name: ' Server ',
      host: ' example.com ',
      port: '22',
      username: ' root ',
      authMethod: 'password',
      privateKeyPath: '',
      password: ''
    })
  })

  it('validates whitespace and authentication fields without requiring hidden fields', () => {
    expect(
      validateHostDraft({ ...valid, name: ' ', host: '', username: '\t', password: '' })
    ).toEqual({
      name: 'form.nameRequired',
      host: 'form.hostRequired',
      username: 'form.usernameRequired',
      password: 'form.passwordRequired'
    })
    expect(validateHostDraft({ ...valid, authMethod: 'privateKey', password: '' })).toEqual({
      privateKeyPath: 'form.privateKeyRequired'
    })
    expect(
      validateHostDraft({
        ...valid,
        authMethod: 'privateKey',
        password: '',
        privateKeyPath: '/key'
      })
    ).toEqual({})
  })

  it('preserves blank edit credentials and existing private-key path omission semantics', () => {
    expect(validateHostDraft({ ...valid, password: '' }, { id: 'h1' })).toEqual({})
    expect(
      validateHostDraft(
        { ...valid, authMethod: 'privateKey', privateKeyPath: '' },
        {
          id: 'h1',
          privateKeyPath: '/saved/key'
        }
      )
    ).toEqual({})
    expect(
      validateHostDraft(
        { ...valid, authMethod: 'privateKey', privateKeyPath: '' },
        {
          id: 'h1'
        }
      )
    ).toEqual({ privateKeyPath: 'form.privateKeyRequired' })
  })

  it('trims host data, preserves password whitespace, and excludes inactive auth fields', () => {
    expect(hostFormSubmission({ ...valid, privateKeyPath: '/hidden' })).toEqual({
      input: {
        name: 'Server',
        host: 'example.com',
        port: 22,
        username: 'root',
        authMethod: 'password'
      },
      password: ' secret '
    })
    expect(
      hostFormSubmission({ ...valid, authMethod: 'privateKey', privateKeyPath: ' /key ' })
    ).toEqual({
      input: {
        name: 'Server',
        host: 'example.com',
        port: 22,
        username: 'root',
        authMethod: 'privateKey',
        privateKeyPath: '/key'
      }
    })
  })
})
