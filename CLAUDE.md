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
| `make serve` | Serve the latest good commit of `main`: POS on http://localhost:8080, network dashboard on http://localhost:8090, issuers on 8091 (First Simple Bank) and 8092 (Union Card Bank). Logs are in `.run/logs/`. |
| `make serve-stop` | Stop it |
| `make test` | `go test ./...` |
| `make up` / `make down` | Full Docker setup (uses the same port 8080, so stop `make serve` first) |

New services go under `cmd/<name>/`. `make serve` builds and starts every binary there, so give each one its own port in `serve.env` and `docker-compose.yml`.

## Learning labels

Every label in the POS and in `Network KT/visuals/` must teach what it means. Give it `data-term="<id>"` with an id from `cmd/pos/web/learn/glossary.json`; each term has a definition, at least one Network KT link (file#heading) and one outside link. Labels app.js renders at runtime are tagged in `cmd/pos/web/learn/pos-terms.js`, and a new field name needs a matching glossary alias. `go test ./cmd/pos/` fails on untagged SVG text, unknown terms, missing links, or doc links to headings that don't exist. Learning pages are served at `/learn/` (hub, doc viewer, glossary).

## Conventions

- Card data: simulation only. Mask PANs in logs (`card.Mask`), never store CVV.
- Messages follow ISO 8583 field numbering (`internal/iso8583`).
- Background reading: `Network KT/` (start with its README; design decisions are in `12-design-decisions.md`).
