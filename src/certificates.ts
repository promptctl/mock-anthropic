import { createHash, randomBytes, X509Certificate } from 'node:crypto'
import { chmod, mkdir, mkdtemp, rename, rm, stat } from 'node:fs/promises'
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
  readonly ca: { readonly certPath: string; readonly keyPath: string; readonly outcome: Outcome }
  readonly leaf: { readonly certPath: string; readonly keyPath: string; readonly outcome: Outcome }
}

// What each certificate is built from: openssl.cnf, then each command, run in the certificate's own
// directory. The leaf's signing command also gets the CA and a serial, which are not part of its recipe.
const KEY = 'rsa:2048'
const DAYS = '3650'
const CA_REQUEST = ['req', '-x509', '-newkey', KEY, '-nodes', '-days', DAYS, '-config', 'openssl.cnf', '-extensions', 'ca', '-keyout', 'key.pem', '-out', 'cert.pem']
const LEAF_REQUEST = ['req', '-new', '-newkey', KEY, '-nodes', '-config', 'openssl.cnf', '-keyout', 'key.pem', '-out', 'request.csr']
const LEAF_SIGN = ['x509', '-req', '-in', 'request.csr', '-days', DAYS, '-extfile', 'openssl.cnf', '-extensions', 'leaf', '-out', 'cert.pem']

// Config files, not `-addext` or `-copy_extensions`: LibreSSL, the openssl macOS ships, has neither.
const CA_CONFIG = `[req]
distinguished_name = dn
prompt = no
[dn]
CN = mock-anthropic CA
[ca]
basicConstraints = critical, CA:true
keyUsage = critical, keyCertSign, cRLSign
subjectKeyIdentifier = hash
`

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

export type Published = { readonly path: string; readonly outcome: Outcome }

// [LAW:no-ambient-temporal-coupling] [LAW:one-type-per-behavior] The one way a certificate reaches
// `dir`. Its name is `kind` and a digest of `recipe`, everything the build depends on, so a changed
// recipe names a directory not yet built and nothing on disk is ever checked for staleness. It is
// built in a scratch directory, then renamed into place whole, so a published name is always
// complete; when two starts build one name at once, the rename decides between them: renaming onto a
// published, non-empty directory fails and leaves it untouched, and the loser uses the winner's.
export async function publish(
  dir: string,
  kind: string,
  recipe: readonly string[],
  build: (scratch: string) => Promise<void>,
): Promise<Published> {
  const path = join(dir, `${kind}-${createHash('sha256').update(recipe.join('\0')).digest('hex').slice(0, 16)}`)
  if (await exists(path)) return { path, outcome: 'reused' }
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

// One certificate and its key in `scratch`. The key is made owner-only here: LibreSSL writes it 0644,
// and a copy of the directory made without its 0700 mode would otherwise publish the CA's key.
async function generate(scratch: string, config: string, steps: readonly (readonly string[])[]): Promise<void> {
  await Bun.write(join(scratch, 'openssl.cnf'), config)
  for (const step of steps) await openssl(step, scratch)
  await chmod(join(scratch, 'key.pem'), 0o600)
}

// Where a published certificate directory keeps the files the openssl steps write.
const files = (path: string) => ({ certPath: join(path, 'cert.pem'), keyPath: join(path, 'key.pem') })

// [LAW:effects-at-boundaries] The CA, then a leaf for exactly `hosts` signed by it. The leaf's recipe
// holds the CA's fingerprint, so a replaced CA gets a new leaf too.
export async function ensureCertificates(dir: string, hosts: readonly string[]): Promise<Certificates> {
  await mkdir(dir, { recursive: true })
  const ca = await publish(dir, 'ca', [CA_CONFIG, ...CA_REQUEST], scratch => generate(scratch, CA_CONFIG, [CA_REQUEST]))
  const caFiles = files(ca.path)
  const fingerprint = new X509Certificate(await Bun.file(caFiles.certPath).text()).fingerprint256
  const config = leafConfig(hosts)
  const sign = [...LEAF_SIGN, '-CA', caFiles.certPath, '-CAkey', caFiles.keyPath, '-set_serial', `0x${randomBytes(16).toString('hex')}`]
  const leaf = await publish(dir, 'leaf', [fingerprint, config, ...LEAF_REQUEST, ...LEAF_SIGN], scratch =>
    generate(scratch, config, [LEAF_REQUEST, sign]),
  )
  return {
    ca: { ...caFiles, outcome: ca.outcome },
    leaf: { ...files(leaf.path), outcome: leaf.outcome },
  }
}
