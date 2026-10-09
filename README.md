# simple-network

A proof-of-concept credit card network, similar to Visa or Mastercard, that runs on a local machine. It simulates issuers, acquirers, merchants, and the network switch that connects them: authorization, clearing, and settlement.

See [PLAN.md](PLAN.md) for the architecture and roadmap, and [Network KT](Network%20KT/README.md) for a full knowledge transfer on how card networks work.

> ⚠️ Simulation only. Never use real card numbers.

## Running

Everything runs in Docker:

```sh
make up      # docker compose up --build -d
```

Then open the POS terminal at **http://localhost:8080**.

| Service | Port | Status |
|---|---|---|
| `pos` (merchant terminal) | 8080 | ✅ Builds 0100 auth requests; not yet sent to the network |
| `acquirer`, `network`, `issuer` | — | Planned |

Other commands: `make down`, `make logs`, `make test`, and `make run-pos` (runs the POS without Docker).
