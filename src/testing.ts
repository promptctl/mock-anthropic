import { afterAll } from 'bun:test'
import { mkdir, mkdtemp, rm } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { dispatch, type Dispatched, type Generators, type MockRequest } from './dispatch.ts'
import { API, ENDPOINT_TABLE, type Method, resolveEndpoint, type Row } from './endpoints.ts'
import { messagesGenerator } from './messages-generator.ts'
import type { Script } from './scripts.ts'

// What the tests share: consumer script directories, and a request answered as the server answers it.

// A script is written as given when it is text, so a test can write one that is not JSON.
export async function writeScript(dir: string, name: string, script: unknown): Promise<string> {
  const path = join(dir, name)
  await Bun.write(path, typeof script === 'string' ? script : JSON.stringify(script))
  return path
}

// One temporary root per test file, removed when its tests finish, and a fresh directory under it for
// each call, holding the scripts given.
export async function scriptDirectories(name: string) {
  const root = await mkdtemp(join(tmpdir(), `mock-anthropic-${name}-`))
  afterAll(() => rm(root, { recursive: true, force: true }))
  let next = 0
  async function directory(scripts: Record<string, unknown> = {}): Promise<string> {
    const dir = join(root, String(next++))
    await mkdir(dir)
    for (const [file, script] of Object.entries(scripts)) await writeScript(dir, file, script)
    return dir
  }
  return { root, directory }
}

// A request through dispatch, resolved against the real endpoint table, as the server sends it.
export function send(generators: Generators<Row>, method: Method, url: string, body = ''): Promise<Dispatched> {
  const request: MockRequest = { method, url: new URL(url), headers: new Headers(), body }
  return dispatch(generators, resolveEndpoint(ENDPOINT_TABLE, request.method, request.url), request)
}

export function sendMessages(raw: string, dirs: readonly string[], builtIns: readonly Script[]): Promise<Dispatched> {
  return send({ messages: messagesGenerator(dirs, builtIns) }, 'POST', `https://${API}/v1/messages`, raw)
}
