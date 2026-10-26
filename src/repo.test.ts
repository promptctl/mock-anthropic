import { afterAll, describe, expect, test } from 'bun:test'
import { lstat, mkdir, mkdtemp, readFile, readlink, rm, symlink } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import pkg from '../package.json' with { type: 'json' }
import { isSyntheticUuid, syntheticUuid } from './account.ts'

// PROJECT.md "Technology": zod is the only runtime dependency. Every package.json field that
// makes an install of this package pull in another package counts, not only `dependencies`.
test('zod is the only runtime dependency', () => {
  const runtimeFields = ['dependencies', 'optionalDependencies', 'peerDependencies']
  const declared: Record<string, unknown> = pkg
  const runtime = runtimeFields.flatMap(field => Object.keys(declared[field] ?? {}))
  expect(runtime).toEqual(['zod'])
})

// [LAW:one-source-of-truth] `packageManager` pins Bun. The running Bun must be that one: setup-bun
// only warns and installs the latest Bun when it cannot read the pin. @types/bun describes that
// runtime's API to the typecheck, so it must name the same version.
test('the running Bun and @types/bun match the pinned Bun', () => {
  expect(`bun@${Bun.version}`).toBe(pkg.packageManager)
  expect(`bun@${pkg.devDependencies['@types/bun']}`).toBe(pkg.packageManager)
})

// PROJECT.md "Acceptance": the repository holds no stored response bodies and no real identifiers.
// The repo is public and the reference mock's captured traffic carries real account UUIDs, so this
// scans everything a push would publish: the files on disk (tracked, or untracked and not ignored)
// and every object of the commits no remote has yet, messages included.

// What a push could publish, split by the rules that apply to it [LAW:types-are-the-program]: a
// path is checked for what kind of file it is, a text for the identifiers it carries.
type Source =
  | { kind: 'path'; origin: string; path: string }
  | { kind: 'text'; origin: string; where: string; text: string }

// [LAW:one-source-of-truth] The declared kinds of file this repo holds. A body stored under any
// other name fails here; a ticket that adds a new kind of file adds it in the same diff.
const ROOT_FILES = new Set([
  '.gitignore',
  'AGENTS.md',
  'CLAUDE.md',
  'PROJECT.md',
  'bun.lock',
  'package.json',
  'tsconfig.json',
])
const DECLARED_KINDS = [/^src\/(?:[^/]+\/)*[^/]+\.ts$/, /^\.github\/workflows\/[^/]+\.yml$/]

// The conventional homes of stored response bodies, so a body dressed as `.ts` still fails.
const BODY_STORES = new Set([
  'fixtures',
  '__fixtures__',
  '__mocks__',
  '__snapshots__',
  'testdata',
  'cassettes',
  'recordings',
  'captures',
  'har',
])

// UUID-shaped in any case and inside any longer token; isSyntheticUuid decides which are ours.
const UUID = /[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}/gi

function foreignUuids(text: string): string[] {
  return (text.match(UUID) ?? []).filter(uuid => !isSyntheticUuid(uuid))
}

// A finding never repeats the identifier it reports: CI logs on a public repo are public.
function redact(text: string): string {
  return text.replace(UUID, uuid => (isSyntheticUuid(uuid) ? uuid : '<foreign UUID>'))
}

function pathLeaks({ origin, path }: { origin: string; path: string }): string[] {
  const shown = `${origin} ${redact(path)}`
  const declared = ROOT_FILES.has(path) || DECLARED_KINDS.some(kind => kind.test(path))
  const stores = path.split('/').filter(segment => BODY_STORES.has(segment.toLowerCase()))
  return [
    ...(declared ? [] : [`${shown}: not a declared kind of repo file`]),
    ...stores.map(store => `${shown}: stored response bodies (${store}/)`),
    ...foreignUuids(path).map(() => `${shown}: UUID not built by syntheticUuid`),
  ]
}

function textLeaks({ origin, where, text }: { origin: string; where: string; text: string }): string[] {
  return text
    .split('\n')
    .flatMap((line, i) =>
      foreignUuids(line).map(() => `${origin} ${redact(where)}:${i + 1}: UUID not built by syntheticUuid`),
    )
}

// [LAW:effects-at-boundaries] Pure: the findings for a set of sources, one line each.
function findLeaks(sources: readonly Source[]): string[] {
  return sources.flatMap(source => (source.kind === 'path' ? pathLeaks(source) : textLeaks(source)))
}

// `root` alone decides which repository git reads: a git hook running `bun test` exports GIT_DIR,
// GIT_INDEX_FILE and the like, which would point every call, the temp repos' included, at the
// repository the hook runs in.
const gitEnv = Object.fromEntries(Object.entries(process.env).filter(([name]) => !name.startsWith('GIT_')))

// A repository is its root and the environment git reads it with, so the temp repos below are
// scanned under the same machine-independent config they were built with.
type Repo = { root: string; env: Record<string, string | undefined> }

// [LAW:no-silent-failure] A git that fails throws with its stderr, so a broken listing can never
// pass as a clean tree.
async function git({ root, env }: Repo, args: string[], stdin: Uint8Array = new Uint8Array()): Promise<Buffer> {
  const proc = Bun.spawn(['git', ...args], { cwd: root, env, stdin, stdout: 'pipe', stderr: 'pipe' })
  const [stdout, stderr, code] = await Promise.all([
    new Response(proc.stdout).arrayBuffer(),
    new Response(proc.stderr).text(),
    proc.exited,
  ])
  if (code !== 0) {
    throw new Error(redact(`git ${args.join(' ')} in ${root} exited ${code}: ${stderr}`))
  }
  return Buffer.from(stdout)
}

function nulSeparated(output: Buffer): string[] {
  return output.toString('utf8').split('\0').filter(entry => entry !== '')
}

// The files on disk `git add --all` would take: tracked or untracked-and-not-ignored, minus
// deletions not yet committed. Read as git would record them: a symlink as its target, and a
// directory (a nested repository, which `git add` records as a gitlink) as a path with no text.
async function workingTreeSources(repo: Repo): Promise<Source[]> {
  const listed = nulSeparated(
    await git(repo, ['ls-files', '-z', '--deduplicate', '--cached', '--others', '--exclude-standard']),
  )
  const deleted = new Set(nulSeparated(await git(repo, ['ls-files', '-z', '--deleted'])))
  const paths = listed.map(entry => entry.replace(/\/$/, '')).filter(path => !deleted.has(path))
  if (paths.length === 0) {
    throw new Error(redact(`git ls-files listed no files in ${repo.root}: nothing was scanned`))
  }
  const sources = await Promise.all(
    paths.map(path =>
      workingTreeFile(repo.root, path).catch((error: unknown) => {
        throw new Error(redact(String(error)), { cause: error })
      }),
    ),
  )
  return sources.flat()
}

async function workingTreeFile(root: string, path: string): Promise<Source[]> {
  const origin = 'working tree'
  const file = join(root, path)
  const stat = await lstat(file)
  const pathSource: Source = { kind: 'path', origin, path }
  if (stat.isDirectory()) return [pathSource]
  const text = stat.isSymbolicLink() ? await readlink(file) : await readFile(file, 'utf8')
  return [pathSource, { kind: 'text', origin, where: path, text }]
}

type GitObject = { name: string; type: string; rest: string; text: string }

// `git cat-file --batch` output: per object a header line, then exactly `size` bytes of content.
function parseBatch(batch: Buffer): GitObject[] {
  const objects: GitObject[] = []
  let offset = 0
  while (offset < batch.length) {
    const headerEnd = batch.indexOf('\n', offset)
    const header = batch.toString('utf8', offset, headerEnd)
    const [name = '', type = '', size = '', ...rest] = header.split(' ')
    if (type === 'missing' || !/^\d+$/.test(size)) {
      throw new Error(`git cat-file could not read an object rev-list listed: ${redact(header)}`)
    }
    const contentEnd = headerEnd + 1 + Number(size)
    objects.push({ name, type, rest: rest.join(' '), text: batch.toString('utf8', headerEnd + 1, contentEnd) })
    offset = contentEnd + 1
  }
  return objects
}

// `git ls-tree -z` entry: `<mode> <type> <object>\t<path>`, the path unquoted.
function parseTreeEntry(entry: string): { object: string; path: string } {
  const match = /^\d+ \w+ ([0-9a-f]+)\t(.+)$/s.exec(entry)
  if (match === null) throw new Error(`git ls-tree printed an entry this scan cannot read: ${redact(entry)}`)
  return { object: match[1] ?? '', path: match[2] ?? '' }
}

// Every object a push could send: what HEAD, a local branch or a tag reaches and origin's
// remote-tracking refs do not. Only origin counts as published, so a commit that has reached a
// private mirror alone is still scanned. A CI push build reads nothing here, since its checkout is
// what origin holds; a CI pull-request build reads the one shallow merge commit it checks out,
// whose tree is the PR's whole result; in a clone with no origin it is all of history, which is
// what a first push sends. Commits and annotated tags are scanned whole, messages included, and
// each commit's full file list as paths, so a new name for content origin already holds is still
// checked. A blob is scanned as text at its path and reported under a commit that carries it.
async function unpushedSources(repo: Repo): Promise<Source[]> {
  const refs = ['HEAD', '--branches', '--tags', '--not', '--remotes=origin']
  const listed = await git(repo, ['rev-list', '--objects', ...refs])
  const format = '--batch=%(objectname) %(objecttype) %(objectsize) %(rest)'
  const objects = parseBatch(await git(repo, ['cat-file', format], listed))
  const commits = objects.filter(object => object.type === 'commit')
  const blobCommit = new Map<string, string>()
  const commitSources: Source[] = []
  for (const { name, text } of commits) {
    const origin = name.slice(0, 12)
    const entries = nulSeparated(await git(repo, ['ls-tree', '-r', '-z', name])).map(parseTreeEntry)
    for (const { object } of entries) blobCommit.set(object, origin)
    commitSources.push(
      { kind: 'text', origin, where: 'commit', text },
      ...entries.map(({ path }): Source => ({ kind: 'path', origin, path })),
    )
  }
  const blobSources = objects
    .filter(object => object.type === 'blob')
    .map(({ name, rest, text }): Source => {
      const origin = blobCommit.get(name)
      if (origin === undefined) throw new Error(`blob ${name} at ${redact(rest)} is in no unpushed commit's tree`)
      return { kind: 'text', origin, where: rest, text }
    })
  const tagSources = objects
    .filter(object => object.type === 'tag')
    .map(({ name, text }): Source => ({ kind: 'text', origin: name.slice(0, 12), where: 'tag', text }))
  return [...commitSources, ...blobSources, ...tagSources]
}

// One path source per path, keeping the first origin, so a path every commit carries is
// reported once.
async function readSources(repo: Repo): Promise<Source[]> {
  const sources = (await Promise.all([workingTreeSources(repo), unpushedSources(repo)])).flat()
  const seenPaths = new Set<string>()
  return sources.filter(source => {
    if (source.kind === 'text') return true
    const first = !seenPaths.has(source.path)
    seenPaths.add(source.path)
    return first
  })
}

test('the tree and unpushed history hold no foreign UUIDs and no stored response bodies', async () => {
  expect(findLeaks(await readSources({ root: join(import.meta.dir, '..'), env: gitEnv }))).toEqual([])
})

describe('findLeaks', () => {
  // Built at run time, so no foreign UUID is ever written into the repo.
  const foreign = crypto.randomUUID()
  const text = (where: string, body: string): Source => ({ kind: 'text', origin: 'o', where, text: body })
  const path = (p: string): Source => ({ kind: 'path', origin: 'o', path: p })

  const cases: [Source, string[]][] = [
    [text('src/a.ts', `one\nconst id = '${foreign}'`), ['o src/a.ts:2: UUID not built by syntheticUuid']],
    [text('src/a.ts', foreign.toUpperCase()), ['o src/a.ts:1: UUID not built by syntheticUuid']],
    [text('src/a.ts', `org-${foreign}x`), ['o src/a.ts:1: UUID not built by syntheticUuid']],
    [text('commit', `tree 0\n\nfix ${foreign}`), ['o commit:3: UUID not built by syntheticUuid']],
    [
      text('src/a.ts', syntheticUuid(0xabc).toUpperCase()),
      ['o src/a.ts:1: UUID not built by syntheticUuid'],
    ],
    [text('src/a.ts', `${syntheticUuid(1)} 3d3c42e5aac5ba805825da76410c181273ba90b1`), []],
    [
      path(`src/${foreign}.ts`),
      [`o src/<foreign UUID>.ts: UUID not built by syntheticUuid`],
    ],
    [
      text(`src/${foreign}.ts`, foreign),
      [`o src/<foreign UUID>.ts:1: UUID not built by syntheticUuid`],
    ],
    [path('src/mcp/servers.json'), ['o src/mcp/servers.json: not a declared kind of repo file']],
    [
      path('testdata/a.json'),
      ['o testdata/a.json: not a declared kind of repo file', 'o testdata/a.json: stored response bodies (testdata/)'],
    ],
    [path('x.har.json'), ['o x.har.json: not a declared kind of repo file']],
    [path('fixture/a.json'), ['o fixture/a.json: not a declared kind of repo file']],
    [path('src/fixtures/a.ts'), ['o src/fixtures/a.ts: stored response bodies (fixtures/)']],
    [path('src/a/__snapshots__/b.ts'), ['o src/a/__snapshots__/b.ts: stored response bodies (__snapshots__/)']],
    [path('src/__mocks__/messages.ts'), ['o src/__mocks__/messages.ts: stored response bodies (__mocks__/)']],
    [
      path('Fixtures/a.json'),
      ['o Fixtures/a.json: not a declared kind of repo file', 'o Fixtures/a.json: stored response bodies (Fixtures/)'],
    ],
    [path('src/fixture.ts'), []],
    [path('src/fixturesish.ts'), []],
    [path('.github/workflows/ci.yml'), []],
    [path('package.json'), []],
  ]

  for (const [source, findings] of cases) {
    const name = source.kind === 'path' ? source.path : `${source.where}: ${source.text}`
    test(`${redact(name)} -> ${findings.length} finding(s)`, () => {
      expect(findLeaks([source])).toEqual(findings)
    })
  }

  test('no finding repeats the foreign UUID it reports', () => {
    for (const finding of findLeaks(cases.map(([source]) => source))) {
      expect(finding.toLowerCase()).not.toContain(foreign)
    }
  })
})

const dir = await mkdtemp(join(tmpdir(), 'mock-anthropic-repo-'))
afterAll(() => rm(dir, { recursive: true, force: true }))

describe('readSources', () => {
  // A git that ignores the machine's config, so commits here need no identity or signing setup and
  // the scan's listing does not depend on a global excludesFile.
  const env = {
    ...gitEnv,
    GIT_CONFIG_GLOBAL: '/dev/null',
    GIT_CONFIG_NOSYSTEM: '1',
    GIT_AUTHOR_NAME: 'mock',
    GIT_AUTHOR_EMAIL: 'mock@mock-anthropic.invalid',
    GIT_COMMITTER_NAME: 'mock',
    GIT_COMMITTER_EMAIL: 'mock@mock-anthropic.invalid',
  }
  function run(cwd: string, ...args: string[]): string {
    const result = Bun.spawnSync(['git', ...args], { cwd, env })
    if (result.exitCode !== 0) throw new Error(`git ${args.join(' ')}: ${result.stderr.toString()}`)
    return result.stdout.toString().trim()
  }
  const scan = (root: string) => readSources({ root, env })
  async function repo(name: string): Promise<string> {
    const root = join(dir, name)
    await mkdir(join(root, 'src'), { recursive: true })
    run(root, 'init', '-q', '-b', 'master')
    await Bun.write(join(root, 'src/keep.ts'), '')
    run(root, 'add', '.')
    run(root, 'commit', '-q', '-m', 'keep')
    return root
  }
  const foreign = crypto.randomUUID()
  const leak = 'UUID not built by syntheticUuid'

  test('reads tracked and untracked files, skips ignored ones, and names a nested repository', async () => {
    const root = await repo('listing')
    await Bun.write(join(root, '.gitignore'), 'ignored.ts\n')
    await Bun.write(join(root, 'src/untracked.ts'), '')
    await Bun.write(join(root, 'ignored.ts'), foreign)
    await mkdir(join(root, 'nested'))
    run(join(root, 'nested'), 'init', '-q')
    const sources = await scan(root)
    const paths = sources.flatMap(source => (source.kind === 'path' ? [source.path] : []))
    expect(paths.toSorted()).toEqual(['.gitignore', 'nested', 'src/keep.ts', 'src/untracked.ts'])
    expect(findLeaks(sources)).toEqual(['working tree nested: not a declared kind of repo file'])
  })

  test('finds a leak in an unpushed root commit that a later commit deleted', async () => {
    const root = join(dir, 'root-commit')
    await mkdir(join(root, 'src'), { recursive: true })
    run(root, 'init', '-q', '-b', 'master')
    await Bun.write(join(root, 'src/keep.ts'), '')
    await Bun.write(join(root, 'src/leak.ts'), `export const id = '${foreign}'\n`)
    run(root, 'add', '.')
    run(root, 'commit', '-q', '-m', 'leak')
    const leaked = run(root, 'rev-parse', '--short=12', 'HEAD')
    run(root, 'rm', '-q', 'src/leak.ts')
    run(root, 'commit', '-q', '-m', 'unleak')
    expect(findLeaks(await scan(root))).toEqual([`${leaked} src/leak.ts:1: ${leak}`])
  })

  test('reads a symlink as its target and skips a tracked file deleted on disk', async () => {
    const root = await repo('disk')
    await Bun.write(join(root, 'src/gone.ts'), '')
    run(root, 'add', '.')
    run(root, 'commit', '-q', '-m', 'gone')
    await rm(join(root, 'src/gone.ts'))
    await symlink(foreign, join(root, 'src/link.ts'))
    expect(findLeaks(await scan(root))).toEqual([`working tree src/link.ts:1: ${leak}`])
  })

  test('finds a leak in a commit message', async () => {
    const root = await repo('message')
    run(root, 'commit', '-q', '--allow-empty', '-m', `about ${foreign}`)
    const commit = run(root, 'rev-parse', '--short=12', 'HEAD')
    expect(findLeaks(await scan(root))).toEqual([`${commit} commit:6: ${leak}`])
  })

  test('reads only commits no remote has', async () => {
    const root = await repo('pushed')
    run(root, 'commit', '-q', '--allow-empty', '-m', `about ${foreign}`)
    run(root, 'update-ref', 'refs/remotes/origin/master', 'HEAD')
    expect((await scan(root)).filter(source => source.origin !== 'working tree')).toEqual([])
    run(root, 'commit', '-q', '--allow-empty', '-m', 'after the push')
    const commit = run(root, 'rev-parse', '--short=12', 'HEAD')
    const origins = new Set((await scan(root)).map(source => source.origin))
    expect(origins).toEqual(new Set(['working tree', commit]))
  })

  test('reads unpushed commits on other branches, tag messages, and commits only a mirror has', async () => {
    const root = await repo('refs')
    run(root, 'update-ref', 'refs/remotes/origin/master', 'HEAD')
    run(root, 'checkout', '-q', '-b', 'wip')
    run(root, 'commit', '-q', '--allow-empty', '-m', `wip ${foreign}`)
    const wip = run(root, 'rev-parse', '--short=12', 'HEAD')
    run(root, 'update-ref', 'refs/remotes/mirror/wip', 'HEAD')
    run(root, 'checkout', '-q', 'master')
    run(root, 'tag', '-a', 'v0', '-m', `release ${foreign}`)
    const tag = run(root, 'rev-parse', '--short=12', 'v0')
    expect(findLeaks(await scan(root)).toSorted()).toEqual(
      [`${wip} commit:6: ${leak}`, `${tag} tag:6: ${leak}`].toSorted(),
    )
  })
})
