//! Name detection measured on SYNTHETIC sentences written for this test (no
//! real customer data). Names are marked [[like this]]. The numbers say how the
//! rules behave on these sentences, not on real CRM notes.

use crmai_redact::{Kind, detect};

const CASES: &[&str] = &[
    "Звонил [[Айбек Садыков]], просит перезвонить в пятницу.",
    "Договорились с [[Нургуль Асановой]] о встрече.",
    "Клиент [[Иван Петрович]] ждёт коммерческое предложение.",
    "Передал документы [[Бакытбеку]] лично.",
    "[[Асан уулу Бакыт]] подписал договор.",
    "Встреча с [[Айгерим]] перенесена на вторник.",
    "Менеджер [[Ольга Сергеевна Ким]] согласовала скидку.",
    "[[Мирлан Токтогулов]] интересуется тарифом Бизнес.",
    "Перезвонил [[Дмитрию]], он не отвечает.",
    "Оплату внесёт [[Жамиля кызы]] до конца месяца.",
    "Клиент из Бишкека, офис на улице Киевской.",
    "Москва и Алматы ждут демонстрацию продукта.",
    "Понедельник подходит для звонка, Вторник занят.",
    "Тариф Бизнес стоит дороже тарифа Старт.",
    "Договор подписан, Счёт отправлен на почту.",
    "Отдел продаж ждёт ответа от бухгалтерии.",
    "Звонок от [[Татьяны Ивановой]] по поводу интеграции с 1С.",
    "[[Канат]] сказал, что бюджет пока не утверждён.",
    "Компания Ромашка просит счёт на оплату.",
    "[[Эрмек Беков]] и [[Чолпон Алиева]] будут на встрече.",
    "Обсудили с [[Николаем]] сроки поставки.",
    "Клиент хочет подключить WhatsApp и Telegram.",
    "Директор [[Самат Жумабеков]] утвердит закупку.",
    "Курс обучения Excel начнётся в октябре.",
    "Напомнить [[Динаре]] про акт сверки.",
];

/// Gold spans (byte offsets in the unmarked text) and the unmarked text.
fn parse(case: &str) -> (String, Vec<(usize, usize)>) {
    let mut text = String::new();
    let mut gold = Vec::new();
    let mut rest = case;
    while let Some(open) = rest.find("[[") {
        text.push_str(&rest[..open]);
        let close = rest[open..].find("]]").expect("closed marker") + open;
        let start = text.len();
        text.push_str(&rest[open + 2..close]);
        gold.push((start, text.len()));
        rest = &rest[close + 2..];
    }
    text.push_str(rest);
    (text, gold)
}

/// Written after the rules were tuned on CASES; never used to tune them. Kept
/// with its misses: names outside the dictionary are expected to be missed.
const HOLDOUT: &[&str] = &[
    "Сегодня звонила [[Мария Кузнецова]], просит счёт.",
    "Передайте [[Алексею]] договор.",
    "[[Гульмира]] из отдела закупок согласовала сумму.",
    "[[Тилек Осмонов]] хочет демо.",
    "Встретились с [[Эльвирой]] в офисе.",
    "Заказ оформлен на [[Айдай Мамытову]].",
    "Белый дом и Ак-Орда были в новостях.",
    "Оплата через Мбанк прошла успешно.",
    "Ответственный: [[Руслан]].",
    "Звонок [[Сыдыкову Бактыбеку]] назначен на завтра.",
    "Отправил КП в Газпром и Лукойл.",
    "[[Калинин Андрей Викторович]] подтвердил заказ.",
];

/// Precision and recall over the cases, and what went wrong.
fn measure(cases: &[&str]) -> (f64, f64, Vec<String>) {
    let (mut tp, mut fp, mut fn_) = (0, 0, 0);
    let mut misses = Vec::new();
    for case in cases {
        let (text, gold) = parse(case);
        let found: Vec<(usize, usize)> = detect(&text)
            .into_iter()
            .filter(|s| s.kind == Kind::Name)
            .map(|s| (s.start, s.end))
            .collect();
        for g in &gold {
            if found.contains(g) {
                tp += 1;
            } else {
                fn_ += 1;
                misses.push(format!("missed {:?} in {text:?}", &text[g.0..g.1]));
            }
        }
        for f in &found {
            if !gold.contains(f) {
                fp += 1;
                misses.push(format!("wrong {:?} in {text:?}", &text[f.0..f.1]));
            }
        }
    }
    let precision = tp as f64 / (tp + fp).max(1) as f64;
    let recall = tp as f64 / (tp + fn_).max(1) as f64;
    (precision, recall, misses)
}

#[test]
fn names_on_synthetic_sentences() {
    let (precision, recall, misses) = measure(CASES);
    println!("synthetic names (tuning set): precision {precision:.2}, recall {recall:.2}");
    for m in &misses {
        println!("  {m}");
    }
    // Floors for these sentences; real notes will do worse.
    assert!(precision >= 0.9, "precision {precision:.2}: {misses:?}");
    assert!(recall >= 0.85, "recall {recall:.2}: {misses:?}");
}

#[test]
fn names_on_holdout_sentences() {
    let (precision, recall, misses) = measure(HOLDOUT);
    println!("synthetic names (holdout): precision {precision:.2}, recall {recall:.2}");
    for m in &misses {
        println!("  {m}");
    }
    // Lower floors: this set was not used to tune the rules.
    assert!(precision >= 0.8, "precision {precision:.2}: {misses:?}");
    assert!(recall >= 0.6, "recall {recall:.2}: {misses:?}");
}

#[test]
fn names_are_replaced_consistently() {
    use crmai_redact::{Mode, Redactor, Report};
    let r = Redactor::new(Mode::Placeholders).unwrap();
    let mut report = Report::new();
    let out = r.redact(
        "Айбек Садыков позвонил. Потом айбек садыков написал на ivan@mail.kg.",
        &mut report,
    );
    assert!(out.starts_with("[NAME_1] позвонил."), "{out}");
    assert!(out.contains("[EMAIL_1]"), "{out}");
    assert_eq!(report.get("NAME"), Some(&1), "{out}");
}
