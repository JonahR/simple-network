# simple-network: Proof-of-Concept Plan

The goal is a card network that runs on one machine and behaves like Visa or Mastercard: it routes authorizations, clears transactions, and settles funds between banks. Every participant runs as its own local service, so the message flow matches a real network.

## 1. How a real card network works

The network is a **switch**. It does not issue cards or hold cardholder money. It connects these parties:

| Party | Role |
|---|---|
| **Cardholder** | Holds a card (PAN, expiry, CVV) issued by an issuing bank |
| **Merchant** | Accepts the card, using a terminal or a checkout page |
| **Acquirer** | The merchant's bank. Sends transactions into the network |
| **Network** (us) | Routes messages by BIN, enforces rules, runs clearing and settlement, charges fees |
| **Issuer** | The cardholder's bank. Approves or declines, and owns the credit line |

A purchase goes through three phases:

1. **Authorization** (real time, under 1s): Merchant → Acquirer → Network → Issuer → back. The issuer checks the card, the available credit, and fraud signals, then places a hold.
2. **Clearing** (batch, end of day): the acquirer sends the final transaction records. The network matches them to the authorizations and computes what each bank owes.
3. **Settlement** (batch, T+1): the network nets every position. Issuers pay the network and the network pays acquirers, minus interchange and network fees. The acquirer then pays the merchant, minus its discount rate.

## 2. Architecture for the PoC

```
 ┌──────────┐   ┌──────────┐   ┌─────────────────┐   ┌──────────┐
 │ Merchant │──▶│ Acquirer │──▶│ simple-network  │──▶│  Issuer  │
 │   (POS)  │◀──│   Bank   │◀──│     switch      │◀──│   Bank   │
 └──────────┘   └──────────┘   │ ─ BIN routing   │   └──────────┘
                               │ ─ auth log      │
                               │ ─ clearing      │
                               │ ─ settlement    │
                               └─────────────────┘
```

- **Stack:** Go. Each service is an HTTP server built on the standard library's `net/http` (Go 1.22+ routing). Each service has its own SQLite database through `modernc.org/sqlite`, a pure-Go driver that needs no cgo. Every service runs in its own Docker container from one shared multi-stage `Dockerfile` (`--build-arg SERVICE=<name>`), all managed by `docker-compose.yml` (`make up`). Go's concurrency model and low latency suit a payment switch.
- **Repo layout:**
  ```
  cmd/network/    # switch binary
  cmd/issuer/     # issuer simulator
  cmd/acquirer/   # acquirer simulator
  cmd/pos/        # merchant CLI + load generator
  internal/iso8583/   # message types, MTIs, response codes
  internal/card/      # PAN generation, Luhn, BIN helpers
  internal/...        # shared packages (ledger, signing, store)
  ```
- **Messages:** a JSON model of ISO 8583 to start, with fields such as MTI `0100`/`0110`, PAN, amount, STAN, RRN, response code, and merchant category code. Real ISO 8583 binary encoding can be added later.
- **Each service is an independent process** with its own database. No component reads another component's data directly, which matches how real banks interact.

## 3. Components

### Network switch (core product)
- **BIN table:** maps card-number prefixes to issuer endpoints, for example `4242xx → Issuer A`.
- **Auth routing:** validates the message, checks the Luhn digit, looks up the BIN, forwards the request to the issuer with a timeout, and stand-in declines if the issuer is down.
- **Transaction log:** an immutable record of every message.
- **Clearing engine:** ingests acquirer batch files and matches each record to its auth by RRN/STAN.
- **Settlement engine:** calculates net positions per bank, applies the fee schedule (interchange plus network assessment), and produces settlement reports.
- **Participant registry:** banks, their keys, and their endpoints.

### Issuer simulator
- Card and account database with credit limit, balance, and holds.
- Authorization decisions: valid card, not expired, CVV match, sufficient available credit, and simple fraud rules (velocity, amount threshold).
- Holds released or converted to posted charges during clearing.
- Monthly statement generation (stretch goal).

### Acquirer simulator
- Merchant onboarding (merchant ID, MCC).
- Forwards terminal requests to the network.
- Builds an end-of-day clearing batch.
- Pays merchants after settlement, minus the discount rate.

### Merchant / POS simulator
- CLI or small web page: "charge card X for $Y".
- Load generator for stress testing.

## 4. Transaction types (in order)

1. Purchase authorization (`0100`/`0110`)
2. Reversal / void (`0400`)
3. Clearing / presentment
4. Refund (credit)
5. Partial and incremental auth (hotels, gas)
6. Chargeback / dispute flow

## 5. Milestones

| # | Milestone | Done when |
|---|---|---|
| 0 | Repo bootstrap | Git repo, README, plan ✅ |
| 0.5 | POS terminal (browser) | Builds 0100 requests for keyed, swipe, chip, contactless, mobile wallets, debit + PIN, cash back, prepaid, fleet, HSA/FSA, BNPL virtual cards, and card on file ✅ |
| 1 | Data models | Card, account, merchant, and ISO-style message schemas; Luhn and card generator |
| 2 | Issuer service | Can approve or decline an auth request directly |
| 3 | Network switch | Routes auths by BIN to two or more issuers; transaction log |
| 4 | Acquirer + POS | End-to-end swipe from the CLI shows approved/declined |
| 5 | Clearing | End-of-day batch is matched to auths and issuer holds are posted |
| 6 | Settlement + fees | Net settlement report per bank; interchange and network fees applied |
| 7 | Reversals, refunds, chargebacks | Full transaction lifecycle |
| 8 | Ops dashboard | Web UI with live auth stream, approval rates, and settlement positions |
| 9 | Hardening | Message signing between participants, idempotency, timeouts/stand-in, load test |

## 6. Security (simulated)

This PoC must **never** handle real card data. Use test PANs only.
- Generate fake cards on test BIN ranges.
- Tokenize or mask PANs in logs (show only the last 4 digits).
- Sign messages between participants (HMAC first, mTLS later) to model network trust.
- Keep the PCI-DSS concepts in mind: segmentation, no CVV storage after auth.

## 7. Open questions

- Fee model: a flat network fee, or interchange tables by MCC and card tier?
- Credit only, or debit and prepaid too?
- Should the PoC include a tokenization service (similar to Visa Token Service)?
