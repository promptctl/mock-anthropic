import { expect, test } from 'bun:test'
import pkg from '../package.json' with { type: 'json' }

// PROJECT.md "Technology": zod is the only runtime dependency. Consumers fetch this repo at a
// pinned tag and install it, so every package a consumer's install would pull in counts, under
// whichever package.json field declares it.
test('zod is the only runtime dependency', () => {
  const runtimeFields = ['dependencies', 'optionalDependencies', 'peerDependencies']
  const declared: Record<string, unknown> = pkg
  const runtime = runtimeFields.flatMap(field => Object.keys(declared[field] ?? {}))
  expect(runtime).toEqual(['zod'])
})
