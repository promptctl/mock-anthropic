import { expect, test } from 'bun:test'
import pkg from '../package.json' with { type: 'json' }

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
