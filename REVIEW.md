# What a reviewer must check in this repository

The invariants are in AGENTS.md; each one broken is a blocking finding. In addition:

- Nothing names a host, a person, an account or a private setup, in code, docs, tests,
  beads or commit messages (a public, managed repository). The repository's own GitHub
  path is the one exception.
- A change to configuration changes the README's Deploy table in the same change.
- Every change of behavior has a test; the capacity and cross-tenant tests stay concurrent
  and in place.
- New prose, identifiers and messages are US English. Older text in UK spelling changes
  only where a change already touches it.
