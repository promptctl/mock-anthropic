# mock-anthropic

A local server that stands in for every network host Claude Code talks to. PROJECT.md is the specification.

## Tests never read markdown

No test may read, parse, or depend on any markdown file (PROJECT.md, CLAUDE.md, AGENTS.md, this README, or any other `.md`). Docs are edited freely, and a doc edit must never fail CI. A test asserts on code and its behavior; where code and a doc must agree, the code is the source the test checks.
