# Working on signup

Invariants. Every change keeps all of these true; the tests that check them stay in place.

- **Personal data never reaches logs, metrics or traces.** Log identifiers, never names,
  numbers or addresses. `internal/logx` redacts defensively; that is a backstop, not
  permission. Request logs carry the route pattern, never the query string or body.
- **No secrets in the repository.** Configuration comes from the environment
  (`deploy/env.example` documents the keys). A committed credential is an incident.
- **No third-party origins on any page.** The content security policy is `'self'`; no
  external scripts, fonts, analytics or embeds.
- **Every organisation's data is isolated.** Once organisations exist, every query is scoped
  through the owning organisation and every endpoint has a cross-tenant test.
- **Capacity is atomic.** Two people cannot both take the last place; the test for that runs
  concurrently, not sequentially.
- **The binary is the deployment.** One static Go binary, one SQLite file, one reverse proxy.
  New runtime dependencies need a reason written in the PR.
- **Every page works on a phone first.** Design at 393 px wide; widen afterwards.
- **This repository is the application, not a deployment.** No hostnames, proxy configs,
  service units or hosting details live here, and nothing in code, docs, tests, beads or
  commit messages names where any instance runs. The only deployment contract is the
  environment variables documented in the README.
- **Nothing here is for crawlers.** Every response carries `X-Robots-Tag: noindex` and
  `/robots.txt` disallows everything. Do not weaken either to make something easier to test.

Day to day: `make test` before every push; CI runs the same. `make run` starts a local
server on 127.0.0.1:8080 with `./signup.db`.

How work happens here: the agent does the work in the open. It claims a bead, changes code
with tests, commits, and opens a pull request; CI and the Claude review run; a person (or,
once trust is earned, the agent) merges. Nothing reaches `main` except through a pull
request. This is the repository's explicit opt-in to the "team-maintainer" profile in the
Beads section below: agents may close beads, commit and push branches. Force-pushing and
rewriting `main` are never allowed.

<!-- BEGIN BEADS INTEGRATION v:1 profile:minimal hash:1105d646 -->
## Beads Issue Tracker

This project uses **bd (beads)** for issue tracking. Run `bd prime` to see full workflow context and commands.

### Quick Reference

```bash
bd ready              # Find available work
bd show <id>          # View issue details
bd update <id> --claim  # Claim work
bd close <id>         # Complete work
```

### Rules

- Use `bd` for ALL task tracking — do NOT use TodoWrite, TaskCreate, or markdown TODO lists
- Run `bd prime` for detailed command reference and session close protocol
- Use `bd remember` for persistent knowledge — do NOT use MEMORY.md files

**Architecture in one line:** issues live in a local Dolt DB; sync uses `refs/dolt/data` on your git remote; `.beads/issues.jsonl` is a passive export. See https://github.com/gastownhall/beads/blob/main/docs/core-concepts/sync-concepts.md for details and anti-patterns.

## Agent Context Profiles

The managed Beads block is task-tracking guidance, not permission to override repository, user, or orchestrator instructions.

- **Conservative (default)**: Use `bd` for task tracking. Do not run git commits, git pushes, or Dolt remote sync unless explicitly asked. At handoff, report changed files, validation, and suggested next commands.
- **Minimal**: Keep tool instruction files as pointers to `bd prime`; use the same conservative git policy unless active instructions say otherwise.
- **Team-maintainer**: Only when the repository explicitly opts in, agents may close beads, run quality gates, commit, and push as part of session close. A current "do not commit" or "do not push" instruction still wins.

## Session Completion

This protocol applies when ending a Beads implementation workflow. It is subordinate to explicit user, repository, and orchestrator instructions.

1. **File issues for remaining work** - Create beads for anything that needs follow-up
2. **Run quality gates** (if code changed) - Tests, linters, builds
3. **Update issue status** - Close finished work, update in-progress items
4. **Handle git/sync by active profile**:
   ```bash
   # Conservative/minimal/default: report status and proposed commands; wait for approval.
   git status

   # Team-maintainer opt-in only, unless current instructions forbid it:
   git pull --rebase
   git push
   git status
   ```
5. **Hand off** - Summarize changes, validation, issue status, and any blocked sync/commit/push step

**Critical rules:**
- Explicit user or orchestrator instructions override this Beads block.
- Do not commit or push without clear authority from the active profile or the current user request.
- If a required sync or push is blocked, stop and report the exact command and error.
<!-- END BEADS INTEGRATION -->
