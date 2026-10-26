import { afterAll, describe, expect, test } from 'bun:test'
import { mkdtemp, rm } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { z } from 'zod'
import {
  type Account,
  AccountSchema,
  DEFAULT_ACCOUNT,
  InvalidAccountError,
  isSyntheticUuid,
  readAccountFile,
  syntheticUuid,
} from './account.ts'

const dir = await mkdtemp(join(tmpdir(), 'mock-anthropic-account-'))
afterAll(() => rm(dir, { recursive: true, force: true }))

async function writeAccountFile(name: string, content: string): Promise<string> {
  const path = join(dir, name)
  await Bun.write(path, content)
  return path
}

const OTHER_ACCOUNT: Account = {
  accountUuid: syntheticUuid(3),
  emailAddress: 'other@mock-anthropic.invalid',
  organizationUuid: syntheticUuid(4),
  organizationName: 'Other Organization',
  subscriptionType: 'pro',
}

describe('synthetic UUIDs', () => {
  test('are valid UUIDs that the form recognizes', () => {
    for (const n of [0, 1, 0xabc, 16 ** 12 - 1]) {
      const uuid = syntheticUuid(n)
      expect(z.uuid().safeParse(uuid).success).toBe(true)
      expect(isSyntheticUuid(uuid)).toBe(true)
    }
    expect(syntheticUuid(1)).toBe('00000000-0000-4000-8000-000000000001')
  })

  test('cannot be built from a number outside the form', () => {
    for (const n of [-1, 1.5, 16 ** 12, Number.NaN]) {
      expect(() => syntheticUuid(n)).toThrow(RangeError)
    }
  })

  test('the form rejects every other UUID-shaped string', () => {
    expect(isSyntheticUuid(crypto.randomUUID())).toBe(false)
    expect(isSyntheticUuid(syntheticUuid(0xabc).toUpperCase())).toBe(false)
    expect(isSyntheticUuid('00000000-0000-4000-8000-00000000001')).toBe(false)
    expect(isSyntheticUuid(` ${syntheticUuid(1)}`)).toBe(false)
  })
})

describe('the default account', () => {
  test('parses and carries only synthetic identifiers', () => {
    expect(AccountSchema.parse(DEFAULT_ACCOUNT)).toEqual(DEFAULT_ACCOUNT)
    expect(isSyntheticUuid(DEFAULT_ACCOUNT.accountUuid)).toBe(true)
    expect(isSyntheticUuid(DEFAULT_ACCOUNT.organizationUuid)).toBe(true)
    expect(DEFAULT_ACCOUNT.emailAddress).toEndWith('.invalid')
  })
})

describe('an account file', () => {
  test('replaces the default with exactly the account it holds', async () => {
    const path = await writeAccountFile('other.json', JSON.stringify(OTHER_ACCOUNT))
    expect(await readAccountFile(path)).toEqual(OTHER_ACCOUNT)
  })

  test('with a bad field is rejected naming the file and the field', async () => {
    const path = await writeAccountFile('bad-uuid.json', JSON.stringify({ ...OTHER_ACCOUNT, organizationUuid: 'nope' }))
    const error = readAccountFile(path)
    await expect(error).rejects.toBeInstanceOf(InvalidAccountError)
    await expect(error).rejects.toThrow(path)
    await expect(error).rejects.toThrow('organizationUuid')
  })

  test('with a missing field is rejected naming the field', async () => {
    const { emailAddress: _, ...partial } = OTHER_ACCOUNT
    const path = await writeAccountFile('missing.json', JSON.stringify(partial))
    await expect(readAccountFile(path)).rejects.toThrow('emailAddress')
  })

  test('with an unknown key is rejected naming the key', async () => {
    const path = await writeAccountFile('extra.json', JSON.stringify({ ...OTHER_ACCOUNT, acountUuid: syntheticUuid(5) }))
    await expect(readAccountFile(path)).rejects.toThrow('acountUuid')
  })

  test('that is not JSON is rejected naming the file', async () => {
    const path = await writeAccountFile('broken.json', '{ not json')
    await expect(readAccountFile(path)).rejects.toThrow(`invalid account in ${path}: not JSON`)
  })

  test('that cannot be read is rejected naming the file', async () => {
    for (const path of [join(dir, 'absent.json'), dir]) {
      const error = readAccountFile(path)
      await expect(error).rejects.toBeInstanceOf(InvalidAccountError)
      await expect(error).rejects.toThrow(`invalid account in ${path}: cannot read`)
    }
  })

  test('with a blank organization name is rejected naming the field', async () => {
    const path = await writeAccountFile('blank.json', JSON.stringify({ ...OTHER_ACCOUNT, organizationName: ' ' }))
    await expect(readAccountFile(path)).rejects.toThrow('organizationName')
  })
})
