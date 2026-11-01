# crmai-redact (Rust)

Finds and replaces personal data before it leaves its boundary: dataset exports
for training (`training/toolkit`) and, later, text sent to an external provider.
It exists because [AI_DATA_GOVERNANCE.md](../../docs/AI_DATA_GOVERNANCE.md)
forbids personal data in training sets and external calls without a policy.

| Kind | Detection | Validation |
|---|---|---|
| `EMAIL` | address pattern | — |
| `PHONE` | `+996 …`, `+7 …`, `0…`, `8…` with spaces, dashes, brackets | 9–13 digits |
| `CARD` | 13–19 digits, grouped or not | Luhn |
| `IBAN` | country code + check digits + 11–30 chars | ISO 13616 mod-97 |
| `KG_PIN` | 14 digits | sex digit 1/2 + valid birth date DDMMYYYY |
| `KZ_IIN` | 12 digits | birth date YYMMDD, century digit 1–6, check digit |
| `PASSPORT` | `ID`/`AN`/`AC`/`PE`/`AL` + 7 digits | — |
| `ADDRESS` | street marker + capitalised name + house/flat (rules in `src/address.rs`): `ул. Киевская 95, кв. 12`, `проспект Чуй, д. 12/3`, `мкр. Джал-29`, `Ахунбаев көчөсү, 12`, `д. 7, кв. 12` | ambiguous short markers (`пр.`, `пл.`, `пер.`, `наб.`) need a house number; a city alone is kept |
| `NAME` | capitalised words (rules in `src/names.rs`) | a known first name (with case endings), a -бек/-гуль name, a patronymic or «уулу»/«кызы» must be in the run |

When candidates overlap, the more specific kind wins (e-mail > checksummed ids > phone >
address > name), so «ул. Байтик Баатыра 7» is one address, not a name.

**Names and addresses are rule-based.** Names outside the dictionary and unusual address
spellings are missed; on synthetic holdout sentences name precision is 1.00 and recall 0.78
(`cargo test --test names_eval -- --nocapture`). Free text must still be treated as
containing personal data until a NER model trained on labelled real examples exists.

## Usage

```bash
make redact-build
echo "тел. +996 555 12-34-56, Aibek@Mail.kg" | runtimes/redact/target/release/crmai-redact text
# тел. [PHONE_1], [EMAIL_1]          stderr: {"redacted":{"EMAIL":1,"PHONE":1}}

crmai-redact jsonl --fields notes,text --key-file /run/secrets/redact_key < in.jsonl > out.jsonl
# with a key (>= 32 bytes): [PHONE_f050ea5e] - the same value gets the same token in every file
```

- **Placeholders** (`[PHONE_1]`) are numbered per document; the same value gets the same
  number, so the text keeps its meaning.
- **Pseudonyms** are an HMAC-SHA256 of the normalised value under a secret key. The same
  customer can be followed across rows, but the value cannot be recovered without brute
  force over the key.
- Values are normalised first, so `+996 555 12-34-56` and `996555123456` are the same
  person, as are `Ivan@Mail.kg` and `ivan@mail.kg`.
- The report goes to stderr and holds counts only, never values.

## Measured

- Tests: `make redact-test` — 13 tests (incl. addresses and look-alikes such as «и пр. Также»)
  covering checksums, each kind in Russian text,
  ordinary numbers (amounts, dates, times) left alone, consistency, keyed pseudonyms,
  idempotence, overlaps, JSONL fields, and a 20 000-value input.
- 50 MB of synthetic Russian text with ~860 000 values: **23 s (2.2 MB/s)** on the dev
  laptop under load, no raw value left in the output.
- The first version took 30 min on the same input: its overlap check was quadratic. It is
  now an ordered-map lookup, O(n log n).
- With addresses (2026-11-01), release build, 20 MB of mixed ru/ky text with ~350 000 values:
  **8.5 s** (the version without addresses: 9.6 s on the same text). Address patterns avoid
  `\b`: a Unicode word boundary on Cyrillic keeps the regex engine off its DFA and made
  a 600 KB text take 5 s.
