use crmai_redact::detect::{iban_ok, kg_pin_ok, kz_iin_ok, luhn_ok};
use crmai_redact::{Kind, Mode, Redactor, Report, detect, redact_json_fields};

fn placeholders() -> Redactor {
    Redactor::new(Mode::Placeholders).unwrap()
}

fn kinds(text: &str) -> Vec<Kind> {
    detect(text).into_iter().map(|s| s.kind).collect()
}

#[test]
fn checksums() {
    assert!(luhn_ok("4111 1111 1111 1111"));
    assert!(!luhn_ok("4111 1111 1111 1112"));
    assert!(iban_ok("GB82 WEST 1234 5698 7654 32"));
    assert!(!iban_ok("GB82 WEST 1234 5698 7654 33"));
    // KG PIN: sex 1, born 15.03.1990.
    assert!(kg_pin_ok("11503199000123"));
    assert!(!kg_pin_ok("11513199000123")); // month 13
    assert!(!kg_pin_ok("31503199000123")); // sex digit 3
    // KZ IIN with a valid check digit (born 1990-01-15, century/sex 3).
    assert!(kz_iin_ok("900115300013"));
    assert!(!kz_iin_ok("900115300019"));
}

#[test]
fn detects_each_kind_in_russian_text() {
    let text = "Клиент Айбек, тел. +996 555 12-34-56, почта aibek@mail.kg. \
                Карта 4111 1111 1111 1111, ПИН 11503199000123, паспорт ID 1234567, \
                ИИН 900115300013, счёт GB82WEST12345698765432.";
    let mut found = kinds(text);
    found.sort();
    assert_eq!(
        found,
        vec![
            Kind::Email,
            Kind::Card,
            Kind::Iban,
            Kind::KgPin,
            Kind::KzIin,
            Kind::Passport,
            Kind::Phone
        ]
    );
}

#[test]
fn keeps_ordinary_numbers() {
    // Amounts, dates, counts and short codes are not personal data.
    let text = "Сделка на 1 250 000 сом, 25% скидка, встреча 05.11.2026 в 10:00, 3 пользователя, заказ 4567.";
    assert!(
        detect(text).is_empty(),
        "false positives: {:?}",
        detect(text)
    );
}

#[test]
fn placeholders_are_consistent_within_a_document() {
    let mut report = Report::new();
    let out = placeholders().redact(
        "Звонил с +996 555 123 456, потом с 0555 999 888, потом снова с 996555123456.",
        &mut report,
    );
    assert_eq!(
        out,
        "Звонил с [PHONE_1], потом с [PHONE_2], потом снова с [PHONE_1]."
    );
    assert_eq!(report.get("PHONE"), Some(&3));
}

#[test]
fn emails_are_case_insensitive_and_text_survives() {
    let mut report = Report::new();
    let out = placeholders().redact(
        "Пишите Ivan@Mail.KG или ivan@mail.kg — ответим.",
        &mut report,
    );
    assert_eq!(out, "Пишите [EMAIL_1] или [EMAIL_1] — ответим.");
}

#[test]
fn pseudonyms_are_stable_across_documents_and_keyed() {
    let key = vec![7u8; 32];
    let a = Redactor::new(Mode::Pseudonyms(key.clone())).unwrap();
    let b = Redactor::new(Mode::Pseudonyms(vec![8u8; 32])).unwrap();
    let mut r = Report::new();
    let first = a.redact("тел +996 555 123 456", &mut r);
    let second = a.redact("номер 996555123456 снова", &mut r);
    let token = first.trim_start_matches("тел ").to_string();
    assert!(token.starts_with("[PHONE_") && second.contains(&token));
    assert!(
        !b.redact("тел +996 555 123 456", &mut r).contains(&token),
        "another key, another token"
    );
    assert!(
        Redactor::new(Mode::Pseudonyms(vec![1u8; 8])).is_err(),
        "short keys are refused"
    );
}

#[test]
fn redaction_is_idempotent_and_leaves_no_values() {
    let text = "Карта 4111111111111111, тел +77011234567, почта a.b@c.kz";
    let mut r = Report::new();
    let once = placeholders().redact(text, &mut r);
    let twice = placeholders().redact(&once, &mut r);
    assert_eq!(once, twice);
    for secret in ["4111111111111111", "77011234567", "a.b@c.kz"] {
        assert!(!once.contains(secret));
    }
}

#[test]
fn overlapping_candidates_resolve_by_precedence() {
    // A card number is also a long digit run a phone pattern could match: the card wins,
    // and the e-mail wins over the digits inside it.
    let spans = detect("4111 1111 1111 1111 и 79991234567@mail.ru");
    let k: Vec<Kind> = spans.iter().map(|s| s.kind).collect();
    assert_eq!(k, vec![Kind::Card, Kind::Email]);
    assert!(
        spans.windows(2).all(|w| w[0].end <= w[1].start),
        "no overlaps, in order"
    );
}

#[test]
fn large_input_is_linearithmic() {
    // 20 000 values used to take minutes with a quadratic overlap check.
    let text = "тел +996 555 12-34-56, карта 4111 1111 1111 1111. ".repeat(10_000);
    let started = std::time::Instant::now();
    let mut r = Report::new();
    let out = placeholders().redact(&text, &mut r);
    assert_eq!(r.get("PHONE"), Some(&10_000));
    assert_eq!(r.get("CARD"), Some(&10_000));
    assert!(!out.contains("4111"));
    assert!(
        started.elapsed().as_secs() < 5,
        "took {:?}",
        started.elapsed()
    );
}

#[test]
fn jsonl_fields_only() {
    let mut r = Report::new();
    let line =
        r#"{"id":"lead-1","notes":"тел +996 555 123 456","phone_count":2,"source":"whatsapp"}"#;
    let out = redact_json_fields(
        &placeholders(),
        line,
        &["notes".into(), "missing".into()],
        &mut r,
    )
    .unwrap();
    let v: serde_json::Value = serde_json::from_str(&out).unwrap();
    assert_eq!(v["notes"], "тел [PHONE_1]");
    assert_eq!(v["id"], "lead-1");
    assert_eq!(v["phone_count"], 2);
    assert!(redact_json_fields(&placeholders(), "not json", &["notes".into()], &mut r).is_err());
}
