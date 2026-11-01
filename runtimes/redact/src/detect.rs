//! Detectors. Each finds candidate spans with a regular expression and then
//! validates them (checksums, plausible dates) to keep false positives low.

use regex::Regex;
use std::collections::BTreeMap;
use std::sync::LazyLock;

/// Kinds of personal data this crate recognises.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Hash, PartialOrd, Ord)]
pub enum Kind {
    Email,
    Card,
    Iban,
    /// Kyrgyz personal identification number (ПИН/ИНН физлица, 14 digits).
    KgPin,
    /// Kazakh individual identification number (ИИН, 12 digits).
    KzIin,
    /// Kyrgyz passport / ID card number (two letters + 7 digits).
    Passport,
    Phone,
    /// Street address, found by rules (see `address`), not by a trained model.
    Address,
    /// Personal name, found by rules (see `names`), not by a trained model.
    Name,
}

impl Kind {
    pub fn label(self) -> &'static str {
        match self {
            Kind::Email => "EMAIL",
            Kind::Card => "CARD",
            Kind::Iban => "IBAN",
            Kind::KgPin => "KG_PIN",
            Kind::KzIin => "KZ_IIN",
            Kind::Passport => "PASSPORT",
            Kind::Phone => "PHONE",
            Kind::Address => "ADDRESS",
            Kind::Name => "NAME",
        }
    }

    pub const ALL: [Kind; 9] = [
        Kind::Email,
        Kind::Card,
        Kind::Iban,
        Kind::KgPin,
        Kind::KzIin,
        Kind::Passport,
        Kind::Phone,
        Kind::Address,
        Kind::Name,
    ];
}

/// A detected span: byte offsets into the input.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Span {
    pub kind: Kind,
    pub start: usize,
    pub end: usize,
}

fn re(pattern: &str) -> Regex {
    Regex::new(pattern).expect("static pattern compiles")
}

static EMAIL: LazyLock<Regex> =
    LazyLock::new(|| re(r"(?i)\b[a-z0-9._%+\-]+@[a-z0-9.\-]+\.[a-z]{2,24}\b"));
// 13-19 digits, optionally grouped by spaces or dashes.
static CARD: LazyLock<Regex> = LazyLock::new(|| re(r"\b\d(?:[ \-]?\d){12,18}\b"));
static IBAN: LazyLock<Regex> = LazyLock::new(|| re(r"\b[A-Z]{2}\d{2}(?:[ ]?[A-Z0-9]){11,30}\b"));
static DIGITS_14: LazyLock<Regex> = LazyLock::new(|| re(r"\b\d{14}\b"));
static DIGITS_12: LazyLock<Regex> = LazyLock::new(|| re(r"\b\d{12}\b"));
static PASSPORT: LazyLock<Regex> = LazyLock::new(|| re(r"\b(?:ID|AN|AC|PE|AL)[ ]?\d{7}\b"));
// International (+996 …, +7 …) or national (0…, 8…) phone numbers with common separators.
static PHONE: LazyLock<Regex> =
    LazyLock::new(|| re(r"(?:\+\d{1,3}[ \-]?)?\(?\d{2,4}\)?(?:[ \-]?\d{2,3}){2,4}"));

fn digits(s: &str) -> Vec<u32> {
    s.chars().filter_map(|c| c.to_digit(10)).collect()
}

/// Luhn checksum (payment cards).
pub fn luhn_ok(number: &str) -> bool {
    let d = digits(number);
    if !(13..=19).contains(&d.len()) {
        return false;
    }
    let sum: u32 = d
        .iter()
        .rev()
        .enumerate()
        .map(|(i, &x)| {
            if i % 2 == 1 {
                let y = x * 2;
                if y > 9 { y - 9 } else { y }
            } else {
                x
            }
        })
        .sum();
    sum % 10 == 0
}

/// ISO 13616 mod-97 check (IBAN).
pub fn iban_ok(candidate: &str) -> bool {
    let s: String = candidate.chars().filter(|c| !c.is_whitespace()).collect();
    if !(15..=34).contains(&s.len()) {
        return false;
    }
    let rearranged = format!("{}{}", &s[4..], &s[..4]);
    let mut rem: u64 = 0;
    for c in rearranged.chars() {
        let v = match c {
            '0'..='9' => c as u64 - '0' as u64,
            'A'..='Z' => c as u64 - 'A' as u64 + 10,
            _ => return false,
        };
        let width = if v >= 10 { 100 } else { 10 };
        rem = (rem * width + v) % 97;
    }
    rem == 1
}

fn plausible_date(day: u32, month: u32, year: u32) -> bool {
    let days = match month {
        1 | 3 | 5 | 7 | 8 | 10 | 12 => 31,
        4 | 6 | 9 | 11 => 30,
        2 if (year % 4 == 0 && year % 100 != 0) || year % 400 == 0 => 29,
        2 => 28,
        _ => return false,
    };
    (1..=days).contains(&day) && (1900..=2100).contains(&year)
}

/// Kyrgyz PIN: sex digit (1 or 2), birth date DDMMYYYY, 5 more digits.
pub fn kg_pin_ok(s: &str) -> bool {
    let d = digits(s);
    if d.len() != 14 || !(d[0] == 1 || d[0] == 2) {
        return false;
    }
    let day = d[1] * 10 + d[2];
    let month = d[3] * 10 + d[4];
    let year = d[5] * 1000 + d[6] * 100 + d[7] * 10 + d[8];
    plausible_date(day, month, year)
}

/// Kazakh IIN: birth date YYMMDD, century/sex digit 1-6, then a check digit.
pub fn kz_iin_ok(s: &str) -> bool {
    let d = digits(s);
    if d.len() != 12 || !(1..=6).contains(&d[6]) {
        return false;
    }
    let century = match d[6] {
        1 | 2 => 1800,
        3 | 4 => 1900,
        _ => 2000,
    };
    let year = century + d[0] * 10 + d[1];
    if !plausible_date(d[4] * 10 + d[5], d[2] * 10 + d[3], year.max(1900)) {
        return false;
    }
    let weights1 = [1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11];
    let weights2 = [3, 4, 5, 6, 7, 8, 9, 10, 11, 1, 2];
    let check = |w: &[u32; 11]| d[..11].iter().zip(w).map(|(a, b)| a * b).sum::<u32>() % 11;
    let mut c = check(&weights1);
    if c == 10 {
        c = check(&weights2);
        if c == 10 {
            return false;
        }
    }
    c == d[11]
}

fn phone_ok(s: &str) -> bool {
    let n = digits(s).len();
    // Long enough to be a phone number, short enough not to be a card or an id.
    (9..=13).contains(&n)
}

/// Every detected span, longest-and-earliest first, without overlaps.
/// Order of precedence when spans overlap: emails, then checksummed ids, then phones,
/// then addresses, then names.
pub fn detect(text: &str) -> Vec<Span> {
    let mut spans: Vec<Span> = Vec::new();
    let mut push = |kind: Kind, re: &Regex, valid: &dyn Fn(&str) -> bool| {
        for m in re.find_iter(text) {
            if valid(m.as_str()) {
                spans.push(Span {
                    kind,
                    start: m.start(),
                    end: m.end(),
                });
            }
        }
    };
    push(Kind::Email, &EMAIL, &|_| true);
    push(Kind::Iban, &IBAN, &iban_ok);
    push(Kind::Card, &CARD, &luhn_ok);
    push(Kind::KgPin, &DIGITS_14, &kg_pin_ok);
    push(Kind::KzIin, &DIGITS_12, &kz_iin_ok);
    push(Kind::Passport, &PASSPORT, &|_| true);
    push(Kind::Phone, &PHONE, &phone_ok);
    // Before names: a street named after a person ("ул. Байтик Баатыра") is an address.
    for (start, end) in crate::address::find(text) {
        spans.push(Span {
            kind: Kind::Address,
            start,
            end,
        });
    }
    // Names last: on overlap a checksummed id or a phone wins.
    for (start, end) in crate::names::find(text) {
        spans.push(Span {
            kind: Kind::Name,
            start,
            end,
        });
    }

    // Keep the first-registered (higher precedence) span when two overlap. Kept
    // spans never overlap each other, so a new span can only collide with the kept
    // span that starts closest before its end: one ordered-map lookup, O(n log n).
    let mut kept: BTreeMap<usize, Span> = BTreeMap::new();
    for s in spans {
        let collides = kept
            .range(..s.end)
            .next_back()
            .is_some_and(|(_, k)| k.end > s.start);
        if !collides {
            kept.insert(s.start, s);
        }
    }
    kept.into_values().collect()
}
