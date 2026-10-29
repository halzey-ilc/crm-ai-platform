//! `crmai-redact` - redact personal data from text or JSONL on stdin.
//!
//! ```text
//! crmai-redact text  [--key-file PATH]                < notes.txt  > notes.redacted.txt
//! crmai-redact jsonl --fields notes,text [--key-file PATH] < data.jsonl > data.redacted.jsonl
//! ```
//!
//! Without `--key-file` values become numbered placeholders per document; with
//! it, keyed pseudonyms stable across documents. A JSON report with counts per
//! kind (never values) is written to stderr. Exit code 2 on bad usage or input.

use crmai_redact::{Mode, Redactor, Report, redact_json_fields};
use std::io::{self, BufRead, Read, Write};
use std::process::ExitCode;

fn usage() -> ExitCode {
    eprintln!(
        "usage: crmai-redact text [--key-file PATH]\n       crmai-redact jsonl --fields a,b [--key-file PATH]"
    );
    ExitCode::from(2)
}

fn main() -> ExitCode {
    let args: Vec<String> = std::env::args().skip(1).collect();
    let Some(command) = args.first().cloned() else {
        return usage();
    };
    let mut key_file = None;
    let mut fields: Vec<String> = Vec::new();
    let mut i = 1;
    while i < args.len() {
        match (args[i].as_str(), args.get(i + 1)) {
            ("--key-file", Some(v)) => key_file = Some(v.clone()),
            ("--fields", Some(v)) => {
                fields = v
                    .split(',')
                    .map(|f| f.trim().to_string())
                    .filter(|f| !f.is_empty())
                    .collect()
            }
            _ => return usage(),
        }
        i += 2;
    }

    let mode = match key_file {
        None => Mode::Placeholders,
        Some(path) => match std::fs::read(&path) {
            Ok(mut key) => {
                while key.last().is_some_and(|b| b.is_ascii_whitespace()) {
                    key.pop();
                }
                Mode::Pseudonyms(key)
            }
            Err(_) => {
                eprintln!("crmai-redact: cannot read the key file");
                return ExitCode::from(2);
            }
        },
    };
    let redactor = match Redactor::new(mode) {
        Ok(r) => r,
        Err(e) => {
            eprintln!("crmai-redact: {e}");
            return ExitCode::from(2);
        }
    };

    let mut report = Report::new();
    let stdout = io::stdout();
    let mut out = io::BufWriter::new(stdout.lock());
    match command.as_str() {
        "text" => {
            let mut input = String::new();
            if io::stdin().read_to_string(&mut input).is_err() {
                eprintln!("crmai-redact: input is not UTF-8 text");
                return ExitCode::from(2);
            }
            let _ = out.write_all(redactor.redact(&input, &mut report).as_bytes());
        }
        "jsonl" => {
            if fields.is_empty() {
                return usage();
            }
            for (n, line) in io::stdin().lock().lines().enumerate() {
                let Ok(line) = line else {
                    eprintln!("crmai-redact: line {} is not UTF-8", n + 1);
                    return ExitCode::from(2);
                };
                if line.trim().is_empty() {
                    continue;
                }
                match redact_json_fields(&redactor, &line, &fields, &mut report) {
                    Ok(redacted) => {
                        let _ = writeln!(out, "{redacted}");
                    }
                    Err(e) => {
                        eprintln!("crmai-redact: line {}: {e}", n + 1);
                        return ExitCode::from(2);
                    }
                }
            }
        }
        _ => return usage(),
    }
    let _ = out.flush();
    let report_json = serde_json::to_string(&report).unwrap_or_else(|_| "{}".into());
    eprintln!("{{\"redacted\":{report_json}}}");
    ExitCode::SUCCESS
}
