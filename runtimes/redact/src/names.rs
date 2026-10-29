//! Personal names in Russian, Kyrgyz and Kazakh texts, by rules (no model).
//!
//! A capitalised word is a name part when it is
//! * a known first name, possibly with a case ending ("Айбек", "Айбеку", "Нургуль");
//! * a patronymic ("Петрович", "Асановна");
//! * a surname ("Садыков", "Асанова", "Петренко"), but only next to a first name or
//!   patronymic, so that ordinary words with similar endings are not taken;
//! * "уулу"/"кызы" after a name part (Kyrgyz patronymic forms).
//!
//! Adjacent parts form one span ("Айбек Садыков", "Иван Петрович Сидоров").
//! Measured only on synthetic sentences (tests/names_eval.rs): names outside the
//! dictionary and unusual spellings are missed. A trained NER model needs
//! labelled real data.

use regex::Regex;
use std::collections::HashSet;
use std::sync::LazyLock;

/// Common first names (lowercase). Not exhaustive by design: a missed name is
/// left in the text; a wrong hit would only replace an ordinary word.
const FIRST_NAMES: &[&str] = &[
    // Kyrgyz and Kazakh
    "айбек",
    "азамат",
    "алмаз",
    "алтынбек",
    "асан",
    "асель",
    "айгерим",
    "айгуль",
    "айжан",
    "айпери",
    "айдана",
    "айсулуу",
    "акылбек",
    "алибек",
    "аман",
    "арген",
    "адилет",
    "бакыт",
    "бактыгуль",
    "бекзат",
    "бектур",
    "болот",
    "гульмира",
    "гульнара",
    "гульзат",
    "данияр",
    "дамир",
    "жамиля",
    "жыпар",
    "жаныл",
    "замир",
    "зарина",
    "индира",
    "канат",
    "калыс",
    "мирлан",
    "медер",
    "мээрим",
    "нурбек",
    "нурлан",
    "нургуль",
    "нурсултан",
    "назгуль",
    "нуржан",
    "рустам",
    "руслан",
    "самат",
    "санжар",
    "сабина",
    "талант",
    "тимур",
    "улан",
    "уметали",
    "чынгыз",
    "чолпон",
    "эрмек",
    "эркин",
    "эльмира",
    "ерлан",
    "ербол",
    "жанар",
    "камила",
    "асылбек",
    "бакыткуль",
    "темирлан",
    "кубат",
    "мурат",
    "марат",
    "эрлан",
    "айбар",
    "айдай",
    "бегимай",
    "бермет",
    "динара",
    "гулия",
    "нуриза",
    // Russian
    "александр",
    "алексей",
    "андрей",
    "анна",
    "анастасия",
    "антон",
    "артём",
    "артем",
    "борис",
    "вадим",
    "валентина",
    "валерий",
    "василий",
    "виктор",
    "виктория",
    "владимир",
    "галина",
    "дарья",
    "денис",
    "дмитрий",
    "евгений",
    "евгения",
    "екатерина",
    "елена",
    "иван",
    "игорь",
    "ирина",
    "кирилл",
    "константин",
    "ксения",
    "лариса",
    "людмила",
    "максим",
    "марина",
    "мария",
    "михаил",
    "наталья",
    "наталия",
    "никита",
    "николай",
    "оксана",
    "олег",
    "ольга",
    "павел",
    "пётр",
    "петр",
    "роман",
    "светлана",
    "сергей",
    "софия",
    "станислав",
    "татьяна",
    "юлия",
    "юрий",
    "яна",
    "ярослав",
];

/// Case endings a first name may carry (Russian and Kyrgyz), added to its stem.
const ENDINGS: &[&str] = &[
    "дын", "дин", "дун", "дүн", "тын", "тин", "тун", "түн", "нын", "нин", "нун", "нүн", "ою", "ей",
    "ой", "ом", "ем", "ым", "га", "ге", "ка", "ке", "ко", "кө", "ды", "ди", "ду", "дү", "ты", "ти",
    "ту", "тү", "ны", "ни", "ну", "нү", "а", "я", "у", "ю", "е", "ы", "и", "й", "",
];

/// Kyrgyz and Kazakh name endings productive enough to recognise names outside
/// the dictionary ("Бакытбек", "Нургуль").
const NAME_SUFFIXES: &[&str] = &["бек", "гуль", "гүл"];

/// The part of a name that stays when it declines: "дмитрий" -> "дмитри",
/// "николай" -> "никола", "татьяна" -> "татьян"; other names stay whole.
fn stem(name: &str) -> &str {
    for tail in ["й", "а", "я"] {
        if let Some(s) = name.strip_suffix(tail) {
            return s;
        }
    }
    name
}

/// Every first name and its declension stem, for one lookup per candidate.
static KNOWN: LazyLock<HashSet<&'static str>> =
    LazyLock::new(|| FIRST_NAMES.iter().flat_map(|n| [*n, stem(n)]).collect());

static WORD: LazyLock<Regex> = LazyLock::new(|| {
    Regex::new(r"[А-ЯЁӨҮҢA-Z][а-яёөүңa-z]+(?:-[А-ЯЁӨҮҢA-Z][а-яёөүңa-z]+)?").unwrap()
});
static PATRONYMIC: LazyLock<Regex> =
    LazyLock::new(|| Regex::new(r"(?:ович|евич|ична|овна|евна)(?:а|у|ем|ом|е|ы|ой)?$").unwrap());
static SURNAME: LazyLock<Regex> = LazyLock::new(|| {
    Regex::new(r"(?:ов|ев|ёв|ин|ын|ова|ева|ёва|ина|ына|ский|ская|енко|бек|беков|бекова)(?:а|у|ым|ой|ой|е|ым)?$")
        .unwrap()
});

#[derive(Clone, Copy, PartialEq)]
enum Part {
    First,
    Patronymic,
    Surname,
    Other,
}

fn first_name(word: &str) -> bool {
    let w = word.to_lowercase();
    ENDINGS.iter().any(|e| {
        w.strip_suffix(e).is_some_and(|base| {
            KNOWN.contains(base)
                || NAME_SUFFIXES
                    .iter()
                    .any(|s| base.len() > s.len() + 2 && base.ends_with(s))
        })
    })
}

fn classify(word: &str) -> Part {
    if first_name(word) {
        Part::First
    } else if PATRONYMIC.is_match(&word.to_lowercase()) {
        Part::Patronymic
    } else if SURNAME.is_match(&word.to_lowercase()) {
        Part::Surname
    } else {
        Part::Other
    }
}

/// Byte ranges of personal names in text.
pub fn find(text: &str) -> Vec<(usize, usize)> {
    let words: Vec<(usize, usize, Part)> = WORD
        .find_iter(text)
        .map(|m| (m.start(), m.end(), classify(m.as_str())))
        .collect();
    // Kyrgyz "уулу"/"кызы" are lowercase; treat "<Name> уулу" as one span.
    let suffix = |end: usize| -> usize {
        for marker in [" уулу", " кызы"] {
            if text[end..].starts_with(marker) {
                return end + marker.len();
            }
        }
        end
    };
    // Parts of one name are separated by a space, or by "уулу"/"кызы" ("Асан уулу Бакыт").
    let adjacent = |a_end: usize, b_start: usize| {
        matches!(text.get(a_end..b_start), Some(" " | " уулу " | " кызы "))
    };
    let marked = |end: usize| suffix(end) != end;

    let mut out = Vec::new();
    let mut i = 0;
    while i < words.len() {
        // A name is a run of adjacent name parts; any other word ends it. A word
        // followed by "уулу"/"кызы" counts as a name part even if unknown, and so
        // does the word right after a patronymic ("Ольга Сергеевна Ким").
        let is_part = |w: &(usize, usize, Part)| w.2 != Part::Other || marked(w.1);
        if !is_part(&words[i]) {
            i += 1;
            continue;
        }
        let mut j = i;
        while j + 1 < words.len()
            && adjacent(words[j].1, words[j + 1].0)
            && (is_part(&words[j + 1]) || words[j].2 == Part::Patronymic)
        {
            j += 1;
        }
        let run = &words[i..=j];
        // Surnames alone are too ambiguous: the run needs a first name, a
        // patronymic or a Kyrgyz marker.
        let anchored = run
            .iter()
            .any(|w| matches!(w.2, Part::First | Part::Patronymic) || marked(w.1));
        if anchored {
            out.push((run[0].0, suffix(run[run.len() - 1].1)));
        }
        i = j + 1;
    }
    out
}
