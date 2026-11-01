//! Street addresses in Russian and Kyrgyz texts, by rules (no model).
//!
//! An address is a street marker with a capitalised (or numbered) name, plus an
//! optional house, building and flat:
//! * prefix markers: "ул. Киевская 95", "проспект Чуй, д. 12/3, кв. 45", "мкр. Джал-29";
//! * Kyrgyz postfix marker: "Ахунбаев көчөсү, 12";
//! * a bare house and flat without a street: "д. 7, кв. 12".
//!
//! Short markers that also abbreviate ordinary words ("пр." = "прочее", "пл." =
//! "платёж") count only with a house number after the name. A city or a region
//! alone is not personal and is left in the text. Measured on synthetic sentences
//! only (tests/redact.rs); unusual spellings are missed.

use regex::Regex;
use std::sync::LazyLock;

// A street name: one to three words, each starting with a capital letter or a digit
// ("Чуй", "Байтик Баатыра", "Джал-29", "7").
const NAME: &str = r"[А-ЯЁӨҮҢA-Z0-9][\w\-.]*(?:\s+[А-ЯЁӨҮҢA-Z][\w\-.]*){0,2}";
// House ("95", "д. 12/3", "дом 5а", "корп. 2") and flat or office ("кв. 45", "оф. 3").
const HOUSE: &str =
    r"(?:,?\s*(?i:д\.|дом)?\s*\d+[а-яa-z]?(?:/\d+)?(?:,?\s*(?i:корп\.|корпус|стр\.)\s*\d+)?)";
const FLAT: &str = r"(?:,?\s*(?i:кв\.|квартира|оф\.|офис)\s*\d+)?";
// Start of text or a non-word character. Not `\b`: a Unicode word boundary on
// Cyrillic text keeps the regex engine off its fast DFA (a 600 KB text took 5 s).
// `find` drops the consumed character from the span.
const START: &str = r"(?:^|[^\w])";

fn build(markers: &str, house_required: bool) -> Regex {
    let house = if house_required {
        HOUSE.to_string()
    } else {
        format!("{HOUSE}?")
    };
    Regex::new(&format!(r"{START}(?i:{markers})\s*{NAME}{house}{FLAT}"))
        .expect("static pattern compiles")
}

// Markers that only mean a street.
static STRONG: LazyLock<Regex> = LazyLock::new(|| {
    build(
        r"ул\.|улица|пр-т|проспект|переулок|бульвар|мкр\.|мкр-н|мкр|микрорайон|ж/м|жилмассив",
        false,
    )
});
// Markers that also abbreviate ordinary words: a house number is required.
static WEAK: LazyLock<Regex> = LazyLock::new(|| {
    build(
        r"пр\.|просп\.|пер\.|пл\.|площадь|наб\.|набережная|б-р|бульв\.|шоссе",
        true,
    )
});
// Kyrgyz: the marker follows a one-word name ("Ахунбаев көчөсү 12"); a longer name
// would swallow the capitalised word before it ("Кардар Ахунбаев көчөсү").
static KY_STREET: LazyLock<Regex> = LazyLock::new(|| {
    Regex::new(&format!(
        r"{START}[А-ЯЁӨҮҢ][\w\-]*\s+(?i:көчөсү|проспекти|кичи району){HOUSE}?{FLAT}"
    ))
    .expect("static pattern compiles")
});
// A house with a flat and no street ("д. 7, кв. 12").
static HOUSE_FLAT: LazyLock<Regex> = LazyLock::new(|| {
    Regex::new(&format!(
        r"{START}(?i:д\.|дом)\s*\d+[а-яa-z]?(?:/\d+)?,?\s*(?i:кв\.|квартира)\s*\d+"
    ))
    .expect("static pattern compiles")
});

/// Byte spans of the addresses found, in text order, possibly overlapping
/// (`detect` keeps the first of overlapping spans).
pub fn find(text: &str) -> Vec<(usize, usize)> {
    let mut out: Vec<(usize, usize)> = Vec::new();
    for re in [&*STRONG, &*WEAK, &*KY_STREET, &*HOUSE_FLAT] {
        for m in re.find_iter(text) {
            let s = m.as_str();
            // The character matched by START and trailing punctuation belong to the
            // sentence, not to the address.
            let body = s.trim_start_matches(|c: char| !c.is_alphanumeric());
            let start = m.start() + (s.len() - body.len());
            let end = m.start() + s.trim_end_matches([',', '.', ' ']).len();
            out.push((start, end));
        }
    }
    out.sort();
    out
}
