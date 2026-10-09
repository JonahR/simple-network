# simple-network

A proof-of-concept credit card network, similar to Visa or Mastercard, that runs on a local machine. It simulates issuers, acquirers, merchants, and the network switch that connects them: authorization, clearing, and settlement.

See [PLAN.md](PLAN.md) for the architecture and roadmap, and [Network KT](Network%20KT/README.md) for a full knowledge transfer on how card networks work.

> ⚠️ Simulation only. Never use real card numbers.

## Running

For local development, serve the latest good commit of `main`:

```sh
make serve   # builds and tests each new commit of main, then restarts on it
```

It never reads your working tree, so only commits merged into `main` that build and pass tests are served. Service settings live in `serve.env`; logs are in `.run/logs/`. Stop it with `make serve-stop`.

Or run everything in Docker:

```sh
make up      # docker compose up --build -d
```

Either way, open the POS terminal at **http://localhost:8080**, the acquirer back office at **http://localhost:8081**, and the network dashboard at **http://localhost:8090**. `make serve` and `make up` use the same ports, so run only one at a time.

| Service | Port | Status |
|---|---|---|
| `pos` (merchant terminal) | 8080 | ✅ Builds 0100 auth requests and sends them via the acquirer |
| `acquirer` (merchant's bank) | 8081 | ✅ Back office; forwards POS sales to the network (POS → acquirer → network) |
| `network` (switch) | 8090 | ✅ Switch + dashboard |
| `issuer` (cardholder banks) | 8091 / 8092 | ✅ FSB / UCB |

Other commands: `make down`, `make logs`, `make test`, `make run-pos` (runs the POS from your working tree, without Docker), and `make hooks` (enables a pre-commit hook that blocks commits that don't build or pass tests).
