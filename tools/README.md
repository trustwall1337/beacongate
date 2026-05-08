# Tools

Shared development and build tooling that does not belong to any single
language subtree. Examples once they exist:

- Code generators (e.g. JSON Schema → Go/TS/Swift type generators driven
  by `protocol/`).
- Release scripts (cross-language version bumps, changelog assembly).
- Local-dev helpers (TLS cert minting for `localhost`, fixture
  generators for integration tests).

Anything Go-specific belongs under `cmd/` (if it's a runnable utility
the user installs) or inside the relevant package (if it's a build-time
helper). Anything language-specific to desktop/mobile belongs inside
those subtrees.
