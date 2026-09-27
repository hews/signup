# signup

Sign-up sheets that work from a text message.

Someone organising an event puts up a sheet: conference slots, helpers for a class party,
things to bring, shifts at a table. People open a link on their phone, tap a slot, enter
their name and number, and they're in. Confirmations and reminders arrive by text; nobody
creates a password.

Early days. This repository holds the server and its tests; a usable first version is the
current milestone.

## Run it locally

Requires Go 1.26.

```sh
make run          # http://127.0.0.1:8080 with ./signup.db
make test         # format, vet, tests
make build        # static binary at ./signup
```

## Deploy

The application is one static binary and one SQLite file. Run it behind any reverse proxy
that terminates TLS and forwards to the address it listens on. It reads three environment
variables:

| Variable | Default | Meaning |
|---|---|---|
| `SIGNUP_LISTEN` | `127.0.0.1:8080` | Address to listen on |
| `SIGNUP_DB` | `signup.db` | Path to the SQLite database file |
| `SIGNUP_LOG_LEVEL` | `info` | `debug`, `info`, `warn` or `error` |

Back up the database file continuously (Litestream works well) or copy it; there is nothing
else to keep. Proxy configuration, service units and hosting details are deliberately not in
this repository; they belong with whatever deploys it.

## How this is built

This project is an experiment in building software with an AI agent doing the work and one
person directing it. Claude Code writes the code, the tests and this text; the human sets
direction, reviews, and approves. Everything about that process is visible here:

- `AGENTS.md` is the short list of rules every change must keep.
- `.claude/` holds the agent's configuration: the model it uses, what it may run without
  asking, hooks that format code and insist on tests, and two sub-agents (a reviewer and a
  writer).
- `.beads/` is the task tracker (`bd list` shows the work).
- Every pull request is reviewed by Claude in CI before a person looks at it, and CI also
  runs the tests, CodeQL, govulncheck and Dependabot.

If you want to see how the sausage is made, the pull requests are the record.

## Licence

MIT.
