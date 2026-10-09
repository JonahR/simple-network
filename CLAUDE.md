# simple-network: notes for agents

A card network PoC in Go (see PLAN.md). Several agents often work on this repo at once, and the owner keeps the app running in a browser while you work.

## Keep main runnable

- **Work in a git worktree on your own branch**, not in the main checkout. Start one with `claude --worktree`, or create it under `.claude/worktrees/`.
- **Only merge into `main` when it builds and passes tests**: `go build ./... && go vet ./... && go test ./...`. The pre-commit hook (`make hooks`) checks this on the staged snapshot. Don't bypass it with `--no-verify`.
- **Never leave half-finished, uncommitted edits in the main checkout.** Other agents and `make serve` share it.
- `make serve` runs the newest commit of `main` that builds and passes tests, and restarts automatically when `main` moves. Merging green work into `main` is how the owner sees it: they refresh the browser.

## Running

| Command | What it does |
|---|---|
| `make serve` | Serve the latest good commit of `main`: POS on http://localhost:8080, acquirer on http://localhost:8081, network dashboard on http://localhost:8090, and the issuers' back-office pages on http://localhost:8091 (First Simple Bank) and http://localhost:8092 (Union Card Bank). Logs are in `.run/logs/`. |
| `make serve-stop` | Stop it |
| `make test` | `go test ./...` |
| `make up` / `make down` | Full Docker setup (uses the same ports, so stop `make serve` first) |

New services go under `cmd/<name>/`. `make serve` builds and starts every binary there, so give each one its own port in `serve.env` and `docker-compose.yml`.

## Learning labels

Every label in a UI (POS, network dashboard, any new service) and in `Network KT/visuals/` must teach what it means:

- Give it `data-term="<id>"` with an id from `internal/learn/web/glossary.json`. Each term has a definition, at least one Network KT link (`file.md#heading`) and one outside link.
- Labels a page renders at runtime are tagged by that service's terms script (`cmd/pos/web/pos-terms.js`, `cmd/network/web/dashboard-terms.js`; the acquirer's `app.js` sets its terms directly), usually through glossary aliases, so a new label text needs a matching alias.
- A new UI loads `/learn/learn.css` and `/learn/learn.js` and mounts `learn.Handler` at `/learn/`, which also serves the learning hub, the doc viewer and the KT docs.
- `go test ./...` fails on untagged SVG text in `Network KT/visuals/`, unknown `data-term` ids in any service's `web/` or in `Network KT/`, glossary terms missing links, doc links to headings that don't exist, and some runtime labels without aliases (the 0100 fields and dashboard labels the tests extract from each `app.js`). It does not see every label a terms script looks up at runtime (card rows, product and read badges, wallet tabs), so check those by hand.

## Conventions

- Card data: simulation only. Mask PANs in logs (`card.Mask`), never store CVV.
- Messages follow ISO 8583 field numbering (`internal/iso8583`).
- Background reading: `Network KT/` (start with its README; design decisions are in `12-design-decisions.md`).
