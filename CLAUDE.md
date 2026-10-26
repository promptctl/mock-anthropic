<!-- BEGIN LIT INTEGRATION -->
## lit Agent-Native Workflow

This repository uses `lit` for agent-native issue tracking.

Start by running `lit quickstart` to load the workflow instructions. It prints how tickets are found, created, updated, and closed here, so running it first means the rest of your work follows the conventions this repo expects. It's a quick, read-only command — no need to check in before running it.

As you work a ticket, record its phase at each move: `lit phase <id> --to impl` when you start writing code, `lit phase <id> --to review` when its PR opens, `lit phase <id> --to merge` when review is done and the merge is under way. `lit start` puts it in `plan`, and `lit done <id>` ends it.

<!-- END LIT INTEGRATION -->

## Tests never read markdown

No test may read, parse, or depend on any markdown file (PROJECT.md, CLAUDE.md, AGENTS.md, a README, or any other `.md`). Docs are edited freely, and a doc edit must never fail CI. A test asserts on code and its behavior; where code and a doc must agree, the code is the source the test checks.
