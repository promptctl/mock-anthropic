import { createHash, randomBytes, X509Certificate } from 'node:crypto'
import { chmod, mkdir, mkdtemp, rename, rm, stat } from 'node:fs/promises'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'

// PROJECT.md "How a program reaches it": the CA a consumer trusts through NODE_EXTRA_CA_CERTS, and
// the leaf the mock presents for every host in the endpoint table, generated with `openssl` on first
// start and reused afterwards.

// Where `serve` keeps them: in the checkout, and gitignored.
export const CERTS_DIR = fileURLToPath(new URL('../certs', import.meta.url))

// [LAW:nothing-unseen] Whether a start made a certificate, found one, or made one and then found that a
// start running at the same moment had published first, for the start's own record.
export type Outcome = 'generated' | 'reused' | 'lost-race'

export type Issued = { readonly certPath: string; readonly keyPath: string; readonly outcome: Outcome }

export type Certificates = { readonly ca: Issued; readonly leaf: Issued }

// What a certificate is built from, run in its own directory: openssl.cnf holds `config`, each
// `prepare` command runs, then `issue` writes the certificate.
type Recipe = {
  readonly config: string
  readonly prepare: readonly (readonly string[])[]
  readonly issue: readonly string[]
}

const KEY = 'rsa:2048'
const DAYS = '3650'

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

const CA: Recipe = {
  config: CA_CONFIG,
  prepare: [],
  issue: ['req', '-x509', '-newkey', KEY, '-nodes', '-days', DAYS, '-config', 'openssl.cnf', '-extensions', 'ca', '-keyout', 'key.pem', '-out', 'cert.pem'],
}

// The leaf for `hosts`, signed by the CA whose files are at `signer`.
function leafRecipe(hosts: readonly string[], signer: Omit<Issued, 'outcome'>): Recipe {
  return {
    config: leafConfig(hosts),
    prepare: [['req', '-new', '-newkey', KEY, '-nodes', '-config', 'openssl.cnf', '-keyout', 'key.pem', '-out', 'request.csr']],
    issue: ['x509', '-req', '-in', 'request.csr', '-days', DAYS, '-extfile', 'openssl.cnf', '-extensions', 'leaf', '-CA', signer.certPath, '-CAkey', signer.keyPath, '-out', 'cert.pem'],
  }
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

export type Published = { readonly path: string; readonly outcome: Outcome }

// [LAW:no-ambient-temporal-coupling] [LAW:one-type-per-behavior] The one way a certificate reaches
// `dir`. Its name is `kind` and a digest of `recipe`, everything the build depends on, so a changed
// recipe names a directory not yet built and nothing on disk is ever checked for staleness. It is
// built in a scratch directory, then renamed into place whole, so a published name is always
// complete; when two starts build one name at once, the rename decides between them: renaming onto a
// published, non-empty directory fails and leaves it untouched, and the loser discards its own and
// uses the winner's.
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
        if (error.code === 'ENOTEMPTY' || error.code === 'EEXIST') return { path, outcome: 'lost-race' as const }
        throw error
      },
    )
  } finally {
    await rm(scratch, { recursive: true, force: true })
  }
}

// Where a certificate directory keeps the files its recipe writes.
const files = (path: string) => ({ certPath: join(path, 'cert.pem'), keyPath: join(path, 'key.pem') })

// [LAW:one-source-of-truth] The directory is named by the recipe it runs, plus `basis`, whatever else
// the result depends on, so what is built cannot drift from what names it. The serial is the one
// input outside the recipe: every issue gets a fresh one. The key is made owner-only: LibreSSL writes
// it 0644, and a copy of the directory made without its 0700 mode would otherwise publish the CA's key.
async function issue(dir: string, kind: string, basis: readonly string[], recipe: Recipe): Promise<Issued> {
  const { path, outcome } = await publish(dir, kind, [...basis, JSON.stringify(recipe)], async scratch => {
    await Bun.write(join(scratch, 'openssl.cnf'), recipe.config)
    for (const command of recipe.prepare) await openssl(command, scratch)
    await openssl([...recipe.issue, '-set_serial', `0x${randomBytes(16).toString('hex')}`], scratch)
    await chmod(join(scratch, 'key.pem'), 0o600)
  })
  return { ...files(path), outcome }
}

// [LAW:effects-at-boundaries] The CA, then a leaf for exactly `hosts` signed by it. The leaf's basis is
// the CA's fingerprint, so a replaced CA gets a new leaf too.
export async function ensureCertificates(dir: string, hosts: readonly string[]): Promise<Certificates> {
  await mkdir(dir, { recursive: true })
  const ca = await issue(dir, 'ca', [], CA)
  const fingerprint = new X509Certificate(await Bun.file(ca.certPath).text()).fingerprint256
  const leaf = await issue(dir, 'leaf', [fingerprint], leafRecipe(hosts, ca))
  return { ca, leaf }
}
