//! Personal-data redaction for the CRM AI Platform.
//!
//! Used before data leaves its boundary: exports for training (Python toolkit)
//! and, later, text sent to any external provider. Two replacement modes:
//!
//! * **placeholders** - `[PHONE_1]`, `[EMAIL_2]`: numbered per document, the same
//!   value gets the same number, so the text keeps its meaning;
//! * **pseudonyms** - `[PHONE_3f2a91c0]`: an HMAC-SHA256 of the value under a
//!   secret key, so the same customer gets the same token across documents
//!   (needed to train on histories) without the value being recoverable.
//!
//! Reports contain counts per kind only, never the values.
//! Not covered: personal names and addresses (they need a NER model).

pub mod detect;

use hmac::{Hmac, Mac};
use sha2::Sha256;
use std::collections::{BTreeMap, HashMap};

pub use detect::{Kind, Span, detect};

/// How detected values are replaced.
#[derive(Clone)]
pub enum Mode {
    Placeholders,
    /// Keyed pseudonyms; the key must be secret and stable across runs.
    Pseudonyms(Vec<u8>),
}

/// Counts of replaced values per kind.
pub type Report = BTreeMap<&'static str, usize>;

/// Redacts one document.
pub struct Redactor {
    mode: Mode,
}

impl Redactor {
    /// Pseudonym keys shorter than 32 bytes are rejected: they could be brute-forced.
    pub fn new(mode: Mode) -> Result<Self, String> {
        if let Mode::Pseudonyms(key) = &mode {
            if key.len() < 32 {
                return Err("pseudonym key must be at least 32 bytes".into());
            }
        }
        Ok(Self { mode })
    }

    /// Returns the redacted text and adds what was replaced to `report`.
    pub fn redact(&self, text: &str, report: &mut Report) -> String {
        let spans = detect(text);
        if spans.is_empty() {
            return text.to_owned();
        }
        let mut numbering: HashMap<(Kind, String), usize> = HashMap::new();
        let mut next: HashMap<Kind, usize> = HashMap::new();
        let mut out = String::with_capacity(text.len());
        let mut cursor = 0;
        for s in spans {
            out.push_str(&text[cursor..s.start]);
            let value = normalise(s.kind, &text[s.start..s.end]);
            let token = match &self.mode {
                Mode::Placeholders => {
                    let n = *numbering.entry((s.kind, value)).or_insert_with(|| {
                        let counter = next.entry(s.kind).or_insert(0);
                        *counter += 1;
                        *counter
                    });
                    format!("[{}_{}]", s.kind.label(), n)
                }
                Mode::Pseudonyms(key) => {
                    let mut mac = Hmac::<Sha256>::new_from_slice(key).expect("any key length");
                    mac.update(s.kind.label().as_bytes());
                    mac.update(b"\0");
                    mac.update(value.as_bytes());
                    let digest = mac.finalize().into_bytes();
                    let hex: String = digest[..4].iter().map(|b| format!("{b:02x}")).collect();
                    format!("[{}_{}]", s.kind.label(), hex)
                }
            };
            out.push_str(&token);
            *report.entry(s.kind.label()).or_insert(0) += 1;
            cursor = s.end;
        }
        out.push_str(&text[cursor..]);
        out
    }
}

/// The same person written differently (`+996 555 12-34-56` vs `996555123456`,
/// `Ivan@Mail.kg` vs `ivan@mail.kg`) must map to the same token.
fn normalise(kind: Kind, raw: &str) -> String {
    match kind {
        Kind::Email => raw.to_lowercase(),
        Kind::Passport | Kind::Iban => raw.chars().filter(|c| !c.is_whitespace()).collect(),
        _ => raw.chars().filter(char::is_ascii_digit).collect(),
    }
}

/// Redacts the given string fields of one JSON object (a JSONL line).
/// Fields that are missing or not strings are left untouched.
pub fn redact_json_fields(
    redactor: &Redactor,
    line: &str,
    fields: &[String],
    report: &mut Report,
) -> Result<String, String> {
    let mut value: serde_json::Value =
        serde_json::from_str(line).map_err(|_| "line is not valid JSON".to_string())?;
    let object = value.as_object_mut().ok_or("line is not a JSON object")?;
    for field in fields {
        if let Some(serde_json::Value::String(s)) = object.get_mut(field) {
            *s = redactor.redact(s, report);
        }
    }
    Ok(value.to_string())
}
