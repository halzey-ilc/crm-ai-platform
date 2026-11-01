"""Knowledge search on paraphrased questions: does the right document reach the passages
given to the model? Synthetic documents only. Runs against the local stack (make up-llm).

    python3 -I evaluation/kb_search_eval.py --token "$TOKEN" [--gw http://127.0.0.1:8090]

Uploads the documents for a fresh tenant (one as DOCX through /v1/knowledge/files), asks each
question through /v1/knowledge/ask, reads the passages the job got (job input, read with the
owner role in the local database container), cancels the job (no model call is needed) and
prints recall: the share of questions whose expected document is among the passages.
"""
import argparse
import json
import pathlib
import subprocess
import sys
import urllib.parse
import urllib.request
import uuid

DOCS = {
    "returns": ("Правила возврата", "returns.docx"),  # from internal/extract/testdata
    "discounts": ("Скидки", "Скидка для новых клиентов составляет не более 10 процентов от суммы заказа.\n\n"
                  "Скидку свыше 10 процентов утверждает руководитель отдела продаж. Постоянным клиентам "
                  "с оборотом больше миллиона сомов в год предоставляется скидка 15 процентов."),
    "delivery": ("Доставка", "Доставка по Бишкеку бесплатна при заказе от 5000 сомов. В Ош и другие "
                 "города товар отправляется транспортной компанией за счёт покупателя.\n\n"
                 "Срок доставки по городу — один-два рабочих дня после оплаты."),
    "vacation": ("Отпуска", "Ежегодный оплачиваемый отпуск составляет 28 календарных дней. Заявление на "
                 "отпуск подаётся руководителю не позднее чем за две недели."),
    "hours": ("Режим работы", "Офис работает с понедельника по пятницу с 9:00 до 18:00, обед с 13:00 до "
              "14:00. В субботу и воскресенье офис закрыт, заявки принимаются через сайт."),
    "payment": ("Оплата", "Счёт должен быть оплачен в течение пяти банковских дней. При просрочке "
                "заказ отменяется, а резерв товара снимается."),
}

# Paraphrased on purpose: few or none of the document's own words.
QUESTIONS = [
    ("Сколько времени у покупателя, чтобы сдать товар обратно?", "returns"),
    ("Когда клиент получит деньги назад?", "returns"),
    ("Какая максимальная уценка для новичка?", "discounts"),
    ("Кто согласует большую скидку?", "discounts"),
    ("Сколько стоит привезти заказ в Ош?", "delivery"),
    ("Как быстро курьер привезёт покупку по столице?", "delivery"),
    ("Сколько дней можно отдыхать за год?", "vacation"),
    ("Заранее ли надо предупредить начальника, что ухожу отдыхать?", "vacation"),
    ("В какие часы можно прийти в контору?", "hours"),
    ("Работаете ли вы в выходные?", "hours"),
    ("Что будет, если клиент не заплатил вовремя?", "payment"),
    ("Кардар товарды канча күндө кайтара алат?", "returns"),
]

DOCX = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"


def call(gw, token, method, path, body=None, ctype="application/json", key=None):
    req = urllib.request.Request(gw + path, data=body, method=method)
    req.add_header("Authorization", "Bearer " + token)
    if body is not None:
        req.add_header("Content-Type", ctype)
    if key:
        req.add_header("Idempotency-Key", key)
    with urllib.request.urlopen(req, timeout=120) as r:
        raw = r.read()
        return json.loads(raw) if raw else None


def job_passages(job_id):
    sql = f"select input from ai_job where id = '{uuid.UUID(job_id)}'"
    out = subprocess.run(["docker", "exec", "crm-ai-platform-postgres-1", "sh", "-c",
                          f'psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -At -c "{sql}"'],
                         capture_output=True, text=True, check=True).stdout
    return [p["ref"].split("#")[0] for p in json.loads(out)["passages"]]


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--token", required=True)
    ap.add_argument("--gw", default="http://127.0.0.1:8090")
    ap.add_argument("--upload", action="store_true", help="upload the documents first")
    args = ap.parse_args()
    root = pathlib.Path(__file__).resolve().parent.parent
    if args.upload:
        for ref, (title, body) in DOCS.items():
            if body.endswith(".docx"):
                data = (root / "internal/extract/testdata" / body).read_bytes()
                q = urllib.parse.urlencode({"title": title, "language": "ru", "access_tags": "all"})
                r = call(args.gw, args.token, "PUT", f"/v1/knowledge/files/{ref}?{q}", data, DOCX)
            else:
                doc = {"title": title, "language": "ru", "access_tags": ["all"], "text": body}
                r = call(args.gw, args.token, "PUT", f"/v1/knowledge/documents/{ref}", json.dumps(doc).encode())
            print(f"uploaded {ref}: passages={r['passages']} semantic={r['semantic']}", file=sys.stderr)
    # With few documents every one of them fits in the passages, so recall says
    # little: rank matters (the model reads the first passages most reliably).
    top1, rr = 0, 0.0
    for question, want in QUESTIONS:
        body = json.dumps({"question": question, "language": "ru", "access_tags": ["all"]}).encode()
        job = call(args.gw, args.token, "POST", "/v1/knowledge/ask", body, key="kbeval-" + uuid.uuid4().hex[:16])
        refs = job_passages(job["id"])
        call(args.gw, args.token, "POST", f"/v1/jobs/{job['id']}/cancel", b"{}", key=None)
        rank = refs.index(want) + 1 if want in refs else None
        top1 += rank == 1
        rr += 1 / rank if rank else 0
        print(f"{'rank ' + str(rank) if rank else 'MISS  '} {question:60.60} -> {refs[:4]}")
    n = len(QUESTIONS)
    print(f"first place: {top1}/{n}, MRR: {rr / n:.2f}")


if __name__ == "__main__":
    main()
