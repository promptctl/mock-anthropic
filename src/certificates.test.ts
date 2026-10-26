import { afterAll, describe, expect, test } from 'bun:test'
import { X509Certificate } from 'node:crypto'
import { mkdtemp, readdir, rm } from 'node:fs/promises'
import tls from 'node:tls'
import { tmpdir } from 'node:os'
import { join, relative } from 'node:path'
import { type Certificates, CERTS_DIR, ensureCertificates } from './certificates.ts'
import { ENDPOINT_HOSTS } from './endpoints.ts'

const root = await mkdtemp(join(tmpdir(), 'mock-anthropic-certs-'))
afterAll(() => rm(root, { recursive: true, force: true }))
const fresh = () => mkdtemp(join(root, 'dir-'))

async function run(argv: string[], cwd: string = root): Promise<{ code: number; output: string }> {
  // A git hook running `bun test` exports GIT_DIR and the like, which would point git elsewhere.
  const env = Object.fromEntries(Object.entries(process.env).filter(([name]) => !name.startsWith('GIT_')))
  const proc = Bun.spawn(argv, { cwd, env, stdout: 'pipe', stderr: 'pipe' })
  const [stdout, stderr, code] = await Promise.all([
    new Response(proc.stdout).text(),
    new Response(proc.stderr).text(),
    proc.exited,
  ])
  return { code, output: stdout + stderr }
}

const read = (path: string) => Bun.file(path).text()

async function sans(certs: Certificates): Promise<string[]> {
  const names = new X509Certificate(await read(certs.leaf.certPath)).subjectAltName ?? ''
  return names.split(', ').map(name => name.replace(/^DNS:/, ''))
}

// Whether a client that trusts only the CA accepts the leaf as `host`, as Claude Code does with
// NODE_EXTRA_CA_CERTS.
async function handshake(certs: Certificates, host: string): Promise<boolean> {
  const [cert, key, ca] = await Promise.all([read(certs.leaf.certPath), read(certs.leaf.keyPath), read(certs.ca.certPath)])
  const server = tls.createServer({ cert, key }, socket => socket.end())
  await new Promise<void>(resolve => server.listen(0, '127.0.0.1', resolve))
  const { port } = server.address() as { port: number }
  try {
    return await new Promise<boolean>((resolve, reject) => {
      const socket = tls.connect({ host: '127.0.0.1', port, servername: host, ca }, () => {
        resolve(socket.authorized)
        socket.end()
      })
      socket.on('error', reject)
    })
  } finally {
    server.close()
  }
}

describe('ensureCertificates', async () => {
  const dir = await fresh()
  const first = await ensureCertificates(dir, ENDPOINT_HOSTS)

  test('a first start generates the CA and the leaf', () => {
    expect([first.ca.outcome, first.leaf.outcome]).toEqual(['generated', 'generated'])
  })

  test('openssl verify accepts the leaf against the CA', async () => {
    const { code, output } = await run(['openssl', 'verify', '-CAfile', first.ca.certPath, first.leaf.certPath])
    expect({ code, output: output.trim() }).toEqual({ code: 0, output: `${first.leaf.certPath}: OK` })
  })

  test("the leaf's SANs are exactly the endpoint table's hosts", async () => {
    expect((await sans(first)).toSorted()).toEqual([...ENDPOINT_HOSTS].toSorted())
  })

  test('a client trusting only the CA accepts the leaf for every host in the table', async () => {
    const accepted = await Promise.all(ENDPOINT_HOSTS.map(host => handshake(first, host)))
    expect(accepted).toEqual(ENDPOINT_HOSTS.map(() => true))
  })

  test('a client rejects the leaf for a host outside the table', async () => {
    await expect(handshake(first, 'example.invalid')).rejects.toThrow(/altnames/i)
  })

  test('a second start reuses both, byte for byte', async () => {
    const paths = [first.ca.certPath, first.leaf.certPath, first.leaf.keyPath]
    const before = await Promise.all(paths.map(read))
    const second = await ensureCertificates(dir, ENDPOINT_HOSTS)
    expect(second).toEqual({
      ca: { ...first.ca, outcome: 'reused' },
      leaf: { ...first.leaf, outcome: 'reused' },
    })
    expect(await Promise.all(paths.map(read))).toEqual(before)
  })

  test('a changed host set keeps the CA and generates a leaf for the new hosts', async () => {
    const hosts = ['api.anthropic.com', 'added.mock-anthropic.invalid']
    const changed = await ensureCertificates(dir, hosts)
    expect(changed.ca).toEqual({ ...first.ca, outcome: 'reused' })
    expect(changed.leaf.outcome).toBe('generated')
    expect(changed.leaf.certPath).not.toBe(first.leaf.certPath)
    expect(await sans(changed)).toEqual(hosts)
    expect((await run(['openssl', 'verify', '-CAfile', changed.ca.certPath, changed.leaf.certPath])).code).toBe(0)
  })

  test('the keys are readable by their owner alone', async () => {
    for (const path of [join(dir, 'ca'), join(first.leaf.keyPath, '..')]) {
      expect((await Bun.file(path).stat()).mode & 0o077).toBe(0)
    }
  })
})

test('two starts at once in an empty dir agree on one CA and one leaf', async () => {
  const dir = await fresh()
  const [a, b] = await Promise.all([ensureCertificates(dir, ENDPOINT_HOSTS), ensureCertificates(dir, ENDPOINT_HOSTS)])
  expect(a.ca.certPath).toBe(b.ca.certPath)
  expect(a.leaf.certPath).toBe(b.leaf.certPath)
  // Exactly one of the two published each certificate; the other used it.
  expect([a.ca.outcome, b.ca.outcome].toSorted()).toEqual(['generated', 'reused'])
  expect([a.leaf.outcome, b.leaf.outcome].toSorted()).toEqual(['generated', 'reused'])
  expect((await run(['openssl', 'verify', '-CAfile', a.ca.certPath, b.leaf.certPath])).code).toBe(0)
  // No scratch directory is left behind.
  expect((await readdir(dir)).toSorted()).toEqual(['ca', relative(dir, join(a.leaf.certPath, '..'))])
})

test('a failing openssl fails the start and names its command', async () => {
  const dir = await fresh()
  await expect(ensureCertificates(dir, [])).rejects.toThrow(/openssl x509 -req .* exited/)
})

test('the default certificate directory is gitignored', async () => {
  const repo = join(import.meta.dir, '..')
  const probe = relative(repo, join(CERTS_DIR, 'ca', 'key.pem'))
  expect(await run(['git', 'check-ignore', '--quiet', probe], repo)).toEqual({ code: 0, output: '' })
})
