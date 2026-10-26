import { expect, test } from 'bun:test'
import pkg from '../package.json' with { type: 'json' }

// PROJECT.md "Technology": zod is the only runtime dependency. Consumers fetch this repo at a
// pinned tag and install it, so every runtime dependency is one more thing they inherit.
test('zod is the only runtime dependency', () => {
  expect(Object.keys(pkg.dependencies)).toEqual(['zod'])
})
