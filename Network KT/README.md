# Network KT: Card Network Knowledge Transfer

This folder explains how a card network like Visa or Mastercard works, what it supports, and the decisions you face when you build one. Read it in order. By the end you should be able to:

- Draw the four-party model from memory and say who owes whom after a purchase.
- Trace one transaction through authorization, clearing, settlement, and a dispute, and name the message at each step.
- Explain where every cent of the merchant discount goes.
- Read an ISO 8583 message and its response codes.
- Know which problems belong to the network and which belong to issuers, acquirers, and processors.
- Make (and defend) the design choices for `simple-network`.

> ⚠️ Fee levels, rule timeframes, and regulations below are typical industry values given for learning. Real networks publish operating rules and fee schedules that change often. Treat the numbers as realistic examples, not as quotes.

## Reading path

| # | Document | What you learn | Time |
|---|---|---|---|
| 01 | [What Is a Card Network](01-what-is-a-card-network.md) | The switch, four-party vs three-party, what a network is *not* | 10 min |
| 02 | [Participants and Roles](02-participants-and-roles.md) | Issuers, acquirers, processors, PayFacs, gateways, token requestors | 15 min |
| 03 | [Transaction Lifecycle](03-transaction-lifecycle.md) | Auth → clearing → settlement → funding, end to end | 15 min |
| 04 | [Authorization Deep Dive](04-authorization-deep-dive.md) | ISO 8583, MTIs, data elements, response codes, stand-in, auth variants | 25 min |
| 05 | [Clearing and Settlement](05-clearing-and-settlement.md) | Batch files, matching, netting, settlement banks, a worked example | 20 min |
| 06 | [Economics and Fees](06-economics-and-fees.md) | MDR, interchange, assessments, a $100 waterfall, who earns what | 15 min |
| 07 | [Cards, BINs, and Tokens](07-cards-bins-and-tokens.md) | PAN anatomy, Luhn, BIN routing, EMV, CVV, network tokenization | 20 min |
| 08 | [Exceptions and Disputes](08-exceptions-and-disputes.md) | Reversals, refunds, chargebacks, representment, arbitration | 20 min |
| 09 | [Risk, Fraud, and Security](09-risk-fraud-and-security.md) | PCI DSS, keys and HSMs, 3-D Secure, fraud scoring, liability shifts | 20 min |
| 10 | [Rules, Governance, and Regulation](10-rules-governance-and-regulation.md) | Operating rules, membership, compliance programs, Durbin, EU IFR | 15 min |
| 11 | [Network Products and Services](11-network-products-and-services.md) | The services a network sells beyond the basic switch | 15 min |
| 12 | [Design Decisions for simple-network](12-design-decisions.md) | ADR-style decisions, recommendations, anti-patterns | 30 min |
| 13 | [Glossary](13-glossary.md) | Every acronym in one place | reference |

## Visuals

The documents embed Mermaid diagrams, which render on GitHub and in most Markdown viewers. Standalone SVG posters live in [`visuals/`](visuals/):

| Visual | Shows |
|---|---|
| [`four-party-model.svg`](visuals/four-party-model.svg) | Participants, message flow, and money flow |
| [`transaction-timeline.svg`](visuals/transaction-timeline.svg) | Auth, clearing, settlement, and funding across T+0 to T+2 |
| [`money-waterfall.svg`](visuals/money-waterfall.svg) | Where $100 goes |
| [`pan-anatomy.svg`](visuals/pan-anatomy.svg) | The parts of a card number |
| [`network-capability-map.svg`](visuals/network-capability-map.svg) | Everything a network supports, grouped into layers |

![Four-party model](visuals/four-party-model.svg)

## Five ideas to remember

1. **The network moves messages and rules, not money it owns.** It routes authorizations, computes who owes whom, and tells settlement banks to move funds. Issuers carry the credit risk. Acquirers carry the merchant risk.
2. **Authorization is a promise. Clearing is the claim. Settlement is the payment.** These are three different steps, run at three different times, and each one can fail on its own.
3. **Every message must be traceable and idempotent.** STAN, RRN, and the original data elements let any party match, reverse, or dispute a transaction days or months later.
4. **The rulebook is the product.** Liability rules, dispute rights, and fee schedules make strangers trust each other. The software only enforces them.
5. **The network makes money on scale.** Its fees are fractions of a cent. Uptime and throughput are business requirements, not just engineering goals.
