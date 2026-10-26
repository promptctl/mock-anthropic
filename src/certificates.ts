import { createHash, randomBytes, X509Certificate } from 'node:crypto'
import { mkdir, mkdtemp, rename, rm, stat } from 'node:fs/promises'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'

// PROJECT.md "How a program reaches it": the CA a consumer trusts through NODE_EXTRA_CA_CERTS, and
// the leaf the mock presents for every host in the endpoint table, generated with `openssl` on first
// start and reused afterwards.

// Where `serve` keeps them: in the checkout, and gitignored.
export const CERTS_DIR = fileURLToPath(new URL('../certs', import.meta.url))

// [LAW:nothing-unseen] Whether a start made a certificate or found one, for the start's own record.
export type Outcome = 'generated' | 'reused'

export type Certificates = {
  readonly ca: { readonly certPath: string; readonly outcome: Outcome }
  readonly leaf: { readonly certPath: string; readonly keyPath: string; readonly outcome: Outcome }
}

const DAYS = '3650'

// Config files, not `-addext` or `-copy_extensions`: LibreSSL, the openssl macOS ships, has neither.
function caConfig(): string {
  return `[req]
distinguished_name = dn
prompt = no
[dn]
CN = mock-anthropic CA
[ca]
basicConstraints = critical, CA:true
keyUsage = critical, keyCertSign, cRLSign
subjectKeyIdentifier = hash
`
}

function leafConfig(hosts: readonly string[]): string {
  return `[req]
distinguished_name = dn
prompt = no
[dn]
CN = mock-anthropic
[leaf]
basicConstraints = critical, CA:false
keyUsage = critical, digitalSignature, keyEncipherment
extendedKeyUsage = serverAuth
subjectAltName = ${hosts.map(host => `DNS:${host}`).join(', ')}
subjectKeyIdentifier = hash
authorityKeyIdentifier = keyid
`
}

// [LAW:no-silent-failure] A failing openssl throws with what it was asked and what it said.
async function openssl(args: readonly string[], cwd: string): Promise<void> {
  const proc = Bun.spawn(['openssl', ...args], { cwd, stdin: 'ignore', stdout: 'ignore', stderr: 'pipe' })
  const [stderr, code] = await Promise.all([new Response(proc.stderr).text(), proc.exited])
  if (code !== 0) throw new Error(`openssl ${args.join(' ')} in ${cwd} exited ${code}: ${stderr}`)
}

async function exists(path: string): Promise<boolean> {
  return stat(path).then(
    () => true,
    (error: NodeJS.ErrnoException) => {
      if (error.code === 'ENOENT') return false
      throw error
    },
  )
}

// [LAW:no-ambient-temporal-coupling] [LAW:one-type-per-behavior] The one way a certificate reaches
// `dir`: built in a scratch directory, then renamed into place whole. So a published name is always
// complete, and when two starts build the same name at once, the rename is what decides between
// them: renaming onto a published, non-empty directory fails and leaves it untouched, and the loser
// uses the winner's. The scratch directory is mkdtemp's 0700, which is what keeps the keys private:
// LibreSSL writes them 0644.
async function publish(dir: string, name: string, build: (scratch: string) => Promise<void>) {
  const path = join(dir, name)
  if (await exists(path)) return { path, outcome: 'reused' as const }
  const scratch = await mkdtemp(join(dir, '.tmp-'))
  try {
    await build(scratch)
    return await rename(scratch, path).then(
      () => ({ path, outcome: 'generated' as const }),
      (error: NodeJS.ErrnoException) => {
        if (error.code === 'ENOTEMPTY' || error.code === 'EEXIST') return { path, outcome: 'reused' as const }
        throw error
      },
    )
  } finally {
    await rm(scratch, { recursive: true, force: true })
  }
}

// [LAW:effects-at-boundaries] The CA, then a leaf for exactly `hosts` signed by it. The leaf's name is
// derived from the CA and the hosts, so a changed host set, or a replaced CA, names a leaf not yet
// built, and no leaf on disk is ever checked for staleness.
export async function ensureCertificates(dir: string, hosts: readonly string[]): Promise<Certificates> {
  await mkdir(dir, { recursive: true })
  const ca = await publish(dir, 'ca', async scratch => {
    await Bun.write(join(scratch, 'openssl.cnf'), caConfig())
    await openssl(
      ['req', '-x509', '-newkey', 'rsa:2048', '-nodes', '-keyout', 'key.pem', '-out', 'cert.pem', '-days', DAYS,
        '-config', 'openssl.cnf', '-extensions', 'ca'],
      scratch,
    )
  })
  const caCert = join(ca.path, 'cert.pem')
  const fingerprint = new X509Certificate(await Bun.file(caCert).text()).fingerprint256
  const digest = createHash('sha256').update([fingerprint, ...hosts].join('\n')).digest('hex')
  const leaf = await publish(dir, `leaf-${digest.slice(0, 16)}`, async scratch => {
    await Bun.write(join(scratch, 'openssl.cnf'), leafConfig(hosts))
    await openssl(
      ['req', '-new', '-newkey', 'rsa:2048', '-nodes', '-keyout', 'key.pem', '-out', 'request.csr', '-config', 'openssl.cnf'],
      scratch,
    )
    await openssl(
      ['x509', '-req', '-in', 'request.csr', '-CA', caCert, '-CAkey', join(ca.path, 'key.pem'),
        '-set_serial', `0x${randomBytes(16).toString('hex')}`, '-days', DAYS,
        '-extfile', 'openssl.cnf', '-extensions', 'leaf', '-out', 'cert.pem'],
      scratch,
    )
  })
  return {
    ca: { certPath: caCert, outcome: ca.outcome },
    leaf: { certPath: join(leaf.path, 'cert.pem'), keyPath: join(leaf.path, 'key.pem'), outcome: leaf.outcome },
  }
}
