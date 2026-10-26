import { createHash, randomUUID } from 'node:crypto'
import { lstat, mkdir, realpath, rename, rm, writeFile } from 'node:fs/promises'
import { join } from 'node:path'
import type { Account } from './account.ts'
import { type Command, parse, UsageError } from './command.ts'
import { accountFrom, commandLineDirectory, commandLinePath } from './command-line.ts'
import type { Json } from './dispatch.ts'
import { ACCESS_TOKEN, OAUTH_SCOPES, REFRESH_TOKEN, TOKEN_LIFETIME_SECONDS } from './oauth.ts'

// PROJECT.md "Commands": `login` writes a Claude Code config directory that the mock accepts as logged
// in: the mock's OAuth credential, the account, completed onboarding, and the trusted projects. It is the
// one definition of a mock login, and a second login into the same directory replaces the first whole.
// The Claude Code behavior cited below is 2.1.88's source, checked against the 2.1.289 binary.

export const CREDENTIALS_FILE = '.credentials.json'
export const CONFIG_FILE = '.claude.json'

export type LoginFile = { readonly name: typeof CREDENTIALS_FILE | typeof CONFIG_FILE; readonly value: Json }

// [LAW:effects-at-boundaries] Pure: the files of a login, given its account, the trust keys of its
// projects (trustKey), and the time it is written.
export function loginFiles(account: Account, trusted: readonly string[], now: number): readonly LoginFile[] {
  return [
    {
      name: CREDENTIALS_FILE,
      value: {
        claudeAiOauth: {
          accessToken: ACCESS_TOKEN,
          refreshToken: REFRESH_TOKEN,
          expiresAt: now + TOKEN_LIFETIME_SECONDS * 1000,
          scopes: [...OAUTH_SCOPES],
          subscriptionType: account.subscriptionType,
          rateLimitTier: null,
        },
      },
    },
    {
      // No `theme`: Claude Code lays the file over its defaults, which hold one (utils/config.ts:590,
      // :1439), so `hasCompletedOnboarding` alone ends onboarding (interactiveHelpers.tsx:111).
      name: CONFIG_FILE,
      value: {
        hasCompletedOnboarding: true,
        oauthAccount: {
          accountUuid: account.accountUuid,
          emailAddress: account.emailAddress,
          organizationUuid: account.organizationUuid,
          organizationName: account.organizationName,
        },
        projects: Object.fromEntries(trusted.map(key => [key, { hasTrustDialogAccepted: true }])),
      },
    },
  ]
}

// [LAW:one-source-of-truth] A project's key in `projects` is Claude Code's own name for its working
// directory: the real path, NFC-normalized (bootstrap/state.ts:271, utils/Shell.ts:447). On macOS a
// project under /tmp is keyed /private/tmp/..., and any other spelling is never looked up.
export async function trustKey(dir: string): Promise<string> {
  return (await realpath(await commandLineDirectory('--trust', dir))).normalize('NFC')
}

// The macOS keychain entry Claude Code keeps a config dir's credential in: named for the NFC
// CLAUDE_CONFIG_DIR string (utils/secureStorage/macOsKeychainHelpers.ts:29), and read ahead of
// `.credentials.json` (fallbackStorage.ts).
export function keychainService(configDir: string): string {
  return `Claude Code-credentials-${createHash('sha256').update(configDir).digest('hex').slice(0, 8)}`
}

// Claude Code moves the credential into the keychain the first time it saves one (a refresh, an MCP
// login) and deletes the file, so an entry from an earlier session would outrank the file this login
// writes. Removing it is what makes the file the login. `security` exits 44 when no entry is left.
// Returns how many it removed, or null where Claude Code keeps credentials only in the file.
// [LAW:no-silent-failure] Any other exit fails the login.
async function removeKeychainEntries(service: string): Promise<number | null> {
  // [LAW:dataflow-not-control-flow] The platform is Claude Code's own discriminator: only on darwin
  // does it read a keychain (utils/secureStorage/index.ts).
  if (process.platform !== 'darwin') return null
  for (let removed = 0; ; removed++) {
    const child = Bun.spawn(['security', 'delete-generic-password', '-s', service], { stdin: 'ignore', stdout: 'ignore', stderr: 'pipe' })
    const [code, stderr] = await Promise.all([child.exited, new Response(child.stderr).text()])
    if (code === 44) return removed
    if (code !== 0) throw new Error(`security delete-generic-password -s ${service}: exit ${code}: ${stderr.trim()}`)
  }
}

const isErrno = (error: unknown, ...codes: readonly string[]) => error instanceof Error && 'code' in error && codes.includes(String(error.code))

// The config dir, made if it is not there; a path that names something else is a wrong command line.
async function configDirectory(path: string): Promise<void> {
  await mkdir(path, { recursive: true }).catch((error: unknown) => {
    throw isErrno(error, 'EEXIST', 'ENOTDIR') ? new UsageError(`<config-dir> ${path}: not a directory`, { cause: error }) : error
  })
}

// Whether `path` has a directory entry: lstat, so a symlink, dangling or not, is one.
async function present(path: string): Promise<boolean> {
  return lstat(path).then(
    () => true,
    (error: unknown) => {
      if (isErrno(error, 'ENOENT')) return false
      throw error
    },
  )
}

// Written beside the target and renamed over it: a replaced file takes the mode (an in-place write
// keeps the old file's), and a Claude Code reading the directory never sees half a file. 0600, as
// Claude Code writes both files itself (utils/config.ts:1139, plainTextStorage.ts:61).
async function writeWhole(dir: string, file: LoginFile): Promise<'created' | 'replaced'> {
  const path = join(dir, file.name)
  const before = (await present(path)) ? 'replaced' : 'created'
  const temporary = join(dir, `${file.name}.${randomUUID()}.tmp`)
  try {
    await writeFile(temporary, `${JSON.stringify(file.value, null, 2)}\n`, { mode: 0o600, flag: 'wx' })
    await rename(temporary, path)
  } catch (error) {
    await rm(temporary, { force: true })
    throw error
  }
  return before
}

// Claude Code 2.1.289 reads `.config.json`, an older global config, in place of CONFIG_FILE whenever
// the config dir holds one (its getGlobalClaudeFile), so a login removes it.
export const LEGACY_CONFIG_FILE = '.config.json'

async function removeLegacyConfig(dir: string): Promise<'removed' | 'absent'> {
  const path = join(dir, LEGACY_CONFIG_FILE)
  if (!(await present(path))) return 'absent'
  await rm(path)
  return 'removed'
}

export const login: Command = {
  kind: 'task',
  usage: '<config-dir> [--trust DIR]... [--account FILE]',
  async run(args) {
    const {
      flags,
      positionals: [dir],
    } = parse(args, { trust: { type: 'string', multiple: true, default: [] }, account: { type: 'string' } }, ['config-dir'])
    // NFC, as Claude Code reads CLAUDE_CONFIG_DIR (utils/envUtils.ts:7), so the keychain name matches.
    const configDir = commandLinePath('<config-dir>', dir).normalize('NFC')
    const trusted = await Promise.all(flags.trust.map(trustKey))
    const accountFile = flags.account === undefined ? null : commandLinePath('--account', flags.account)
    const account = await accountFrom(accountFile)
    await configDirectory(configDir)
    const files = await Promise.all(loginFiles(account, trusted, Date.now()).map(async file => [file.name, await writeWhole(configDir, file)] as const))
    const legacy = await removeLegacyConfig(configDir)
    // Last: the keychain entry is an earlier login's, removed only once this one is written.
    const keychain = await removeKeychainEntries(keychainService(configDir))
    return {
      outcome: 'ok',
      // [LAW:nothing-unseen] Where the login went, which account won (null: the default), what is
      // trusted, what this login replaced or removed, and how many keychain entries it removed.
      facts: {
        config_dir: configDir,
        account_file: accountFile,
        trusted,
        files: { ...Object.fromEntries(files), [LEGACY_CONFIG_FILE]: legacy },
        keychain_removed: keychain,
      },
    }
  },
}
