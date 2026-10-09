# 13: Glossary

| Term | Meaning | See |
|---|---|---|
| **AAV** | Accountholder Authentication Value: Mastercard's 3DS proof (the counterpart of Visa's CAVV) | [09](09-risk-fraud-and-security.md) |
| **ACS** | Access Control Server: the issuer's 3DS authentication server | [09](09-risk-fraud-and-security.md) |
| **Acquirer** | The merchant's bank. Brings transactions into the network and pays merchants. | [02](02-participants-and-roles.md) |
| **AFT** | Account Funding Transaction: pulls funds from a card to fund another account | [11](11-network-products-and-services.md) |
| **ARN** | Acquirer Reference Number: the lifetime ID of a clearing record | [05](05-clearing-and-settlement.md) |
| **ARQC / ARPC** | Chip cryptogram from the card / the issuer's response cryptogram | [07](07-cards-bins-and-tokens.md) |
| **Assessment** | A network fee charged as a percentage of volume | [06](06-economics-and-fees.md) |
| **ATC** | Application Transaction Counter on a chip card | [07](07-cards-bins-and-tokens.md) |
| **Auth code** | 6-character approval code from the issuer (DE38) | [04](04-authorization-deep-dive.md) |
| **AVS** | Address Verification Service: compares the billing address | [09](09-risk-fraud-and-security.md) |
| **BIN / IIN** | Bank / Issuer Identification Number: the first 6–8 digits of the PAN | [07](07-cards-bins-and-tokens.md) |
| **Bitmap** | The ISO 8583 header that says which data elements are present | [04](04-authorization-deep-dive.md) |
| **CAVV** | Cardholder Authentication Verification Value: Visa's 3DS proof | [09](09-risk-fraud-and-security.md) |
| **Chargeback** | An issuer-initiated reversal of a settled transaction under dispute rules | [08](08-exceptions-and-disputes.md) |
| **CIT / MIT** | Cardholder-initiated / merchant-initiated transaction | [03](03-transaction-lifecycle.md) |
| **Clearing** | Exchanging final transaction records and calculating obligations | [05](05-clearing-and-settlement.md) |
| **CNP / CP** | Card-not-present / card-present | [03](03-transaction-lifecycle.md) |
| **Cutoff** | The time that closes a clearing or settlement cycle | [05](05-clearing-and-settlement.md) |
| **CVV / CVV2 / iCVV** | Card verification values (stripe / printed / chip) | [07](07-cards-bins-and-tokens.md) |
| **DCC** | Dynamic Currency Conversion: the merchant converts at checkout | [05](05-clearing-and-settlement.md) |
| **DE** | Data Element: a numbered ISO 8583 field | [04](04-authorization-deep-dive.md) |
| **Detokenization** | Mapping a network token back to the real PAN | [07](07-cards-bins-and-tokens.md) |
| **DS** | Directory Server: the network's 3DS router | [09](09-risk-fraud-and-security.md) |
| **Dual-message** | Separate authorization and clearing messages | [03](03-transaction-lifecycle.md) |
| **DUKPT** | Derived Unique Key Per Transaction: terminal key management | [09](09-risk-fraud-and-security.md) |
| **Durbin Amendment** | US law capping debit interchange for large issuers and requiring routing choice | [06](06-economics-and-fees.md) |
| **ECI** | E-commerce indicator: the authentication result in the auth message | [09](09-risk-fraud-and-security.md) |
| **EMV** | The chip card standard (Europay, Mastercard, Visa), managed by EMVCo | [07](07-cards-bins-and-tokens.md) |
| **Exposure** | A member's unsettled net-debit position | [05](05-clearing-and-settlement.md) |
| **Floor limit** | The amount below which a transaction may be accepted without online auth (now usually zero) | [05](05-clearing-and-settlement.md) |
| **Funding** | The acquirer paying the merchant | [03](03-transaction-lifecycle.md) |
| **HSM** | Hardware Security Module: tamper-resistant key storage and crypto | [09](09-risk-fraud-and-security.md) |
| **Incremental auth** | Increasing an existing authorization hold | [04](04-authorization-deep-dive.md) |
| **Interchange** | Fee paid by the acquirer to the issuer per transaction, set by the network | [06](06-economics-and-fees.md) |
| **ISO 8583** | The international card-message standard | [04](04-authorization-deep-dive.md) |
| **ISO / MSP** | Independent Sales Organization: sells merchant accounts for an acquirer | [02](02-participants-and-roles.md) |
| **Issuer** | The cardholder's bank. Approves transactions and owns the credit risk. | [02](02-participants-and-roles.md) |
| **Liability shift** | A rule moving fraud loss to the party with weaker technology or authentication | [08](08-exceptions-and-disputes.md) |
| **Luhn** | Mod-10 check digit algorithm | [07](07-cards-bins-and-tokens.md) |
| **MAC** | Message Authentication Code | [09](09-risk-fraud-and-security.md) |
| **MCC** | Merchant Category Code (4 digits, ISO 18245) | [04](04-authorization-deep-dive.md) |
| **MDR** | Merchant Discount Rate: the total fee the merchant pays | [06](06-economics-and-fees.md) |
| **MID / TID** | Merchant ID / Terminal ID | [02](02-participants-and-roles.md) |
| **MTI** | Message Type Indicator (e.g., `0100`) | [04](04-authorization-deep-dive.md) |
| **Netting** | Combining many obligations into one net amount per member | [05](05-clearing-and-settlement.md) |
| **Network token** | A network-issued PAN substitute restricted to a domain | [07](07-cards-bins-and-tokens.md) |
| **OCT** | Original Credit Transaction: a push payment to a card | [11](11-network-products-and-services.md) |
| **On-us** | Transaction where the issuer and acquirer are the same bank | [05](05-clearing-and-settlement.md) |
| **Open-to-buy** | Available credit = limit − balance − holds | [03](03-transaction-lifecycle.md) |
| **PAN** | Primary Account Number: the card number | [07](07-cards-bins-and-tokens.md) |
| **Partial approval** | Issuer approves less than the requested amount | [04](04-authorization-deep-dive.md) |
| **PayFac** | Payment facilitator: a master merchant with sub-merchants | [02](02-participants-and-roles.md) |
| **PCI DSS** | Payment Card Industry Data Security Standard | [09](09-risk-fraud-and-security.md) |
| **Pre-arbitration / Arbitration** | Late dispute stages, ending in a network decision | [08](08-exceptions-and-disputes.md) |
| **Presentment** | A clearing record submitted by the acquirer | [05](05-clearing-and-settlement.md) |
| **Principal member** | A bank licensed directly by the network | [10](10-rules-governance-and-regulation.md) |
| **Processing code** | DE3: the transaction type (purchase, refund, cash…) | [04](04-authorization-deep-dive.md) |
| **Reason code** | Code stating why a chargeback was filed | [08](08-exceptions-and-disputes.md) |
| **Representment** | The acquirer's response contesting a chargeback | [08](08-exceptions-and-disputes.md) |
| **Reversal** | Undoing an authorization before clearing (`0400`/`0420`) | [08](08-exceptions-and-disputes.md) |
| **RRN** | Retrieval Reference Number (DE37) | [04](04-authorization-deep-dive.md) |
| **SCA** | Strong Customer Authentication (EU PSD2) | [09](09-risk-fraud-and-security.md) |
| **Settlement** | The actual movement of funds between members | [05](05-clearing-and-settlement.md) |
| **Single-message** | One message that both authorizes and clears (PIN debit, ATM) | [03](03-transaction-lifecycle.md) |
| **STAN** | System Trace Audit Number (DE11) | [04](04-authorization-deep-dive.md) |
| **STIP** | Stand-In Processing: the network decides for an unavailable issuer | [04](04-authorization-deep-dive.md) |
| **Token requestor** | An entity that asks the network for tokens (a wallet, merchant, or gateway) | [07](07-cards-bins-and-tokens.md) |
| **TSP** | Token Service Provider: the network's token vault and service | [07](07-cards-bins-and-tokens.md) |
| **Value date** | The date funds actually move | [05](05-clearing-and-settlement.md) |
| **ZMK / ZPK** | Zone Master Key / Zone PIN Key | [09](09-risk-fraud-and-security.md) |

## Common MCCs (for test data)

| MCC | Category |
|---|---|
| 4111 | Local commuter transport |
| 4121 | Taxis / rideshare |
| 4814 | Telecom services |
| 4900 | Utilities |
| 5411 | Grocery stores / supermarkets |
| 5541 / 5542 | Service stations / automated fuel dispensers |
| 5732 | Electronics stores |
| 5812 | Restaurants |
| 5814 | Fast food |
| 5999 | Miscellaneous retail |
| 6011 | ATM cash disbursement |
| 7011 | Hotels / lodging |
| 7512 | Car rental |
| 7995 | Gambling (high risk) |
| 8398 | Charitable organizations |
