package jobtypes_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"crmai/internal/jobs"
	"crmai/internal/jobtypes"
	"crmai/internal/modelgw"
	"crmai/internal/modelgw/mock"
	"crmai/internal/testutil"
)

const coachInput = `{"language":"ru","today":"2026-10-29",
 "deal":{"ref":"deal:1","title":"Внедрение CRM для ОсОО Арча","status":"open","stage":"Переговоры",
         "stage_position":3,"stage_count":5,"probability":60,"amount":450000,"currency":"KGS",
         "expected_close_date":"2026-10-20","age_days":41},
 "history":[
  {"ref":"activity:a","kind":"note","date":"2026-10-01","title":null,"text":"Клиент просит скидку 10% и интеграцию с 1С.","status":null},
  {"ref":"activity:b","kind":"task","date":"2026-10-25","title":"Отправить КП","text":null,"status":"overdue"},
  {"ref":"meeting:c","kind":"meeting","date":"2026-11-03","title":"Демо для директора","text":null,"status":"scheduled"}]}`

func runCoach(t *testing.T, gw *modelgw.Gateway, input string) (jobs.Result, error) {
	t.Helper()
	return testutil.Registry(t).Execute(context.Background(), gw, jobs.Job{
		ID: "c1", TenantID: "acme", Type: "deal.coach.v1", Input: json.RawMessage(input),
	})
}

func coachGateway(t *testing.T) *modelgw.Gateway {
	t.Helper()
	gw, err := modelgw.New(map[string]modelgw.Provider{"deal_coach": mock.Provider{}})
	if err != nil {
		t.Fatal(err)
	}
	return gw
}

func TestMockCoachFlagsRisksFromCRMFields(t *testing.T) {
	res, err := runCoach(t, coachGateway(t), coachInput)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	out := string(res.Output)
	for _, want := range []string{"Ожидаемая дата закрытия прошла", "Задача просрочена: Отправить КП", `"insufficient_data":false`} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q: %s", want, out)
		}
	}

	empty := strings.Replace(coachInput, coachInput[strings.Index(coachInput, `"history":[`):], `"history":[]}`, 1)
	res, err = runCoach(t, coachGateway(t), empty)
	if err != nil || !strings.Contains(string(res.Output), `"insufficient_data":true`) {
		t.Fatalf("a deal without history must be flagged: %v %s", err, res.Output)
	}
}

func TestCoachGroundingRejectsInventedFactsAndRefs(t *testing.T) {
	gen := `"insufficient_data":false,"language":"ru","generator":{"provider":"fixed","model":"m","is_mock":false}`
	sit := `"situation":[{"text":"Сделка на этапе 3 из 5, вероятность 60%.","refs":["deal:1"]}]`
	cases := map[string]string{
		"invented discount": `{"schema":"deal_coach.output.v1",` + sit + `,"risks":[{"text":"Клиент хочет скидку 15%.","refs":["activity:a"]}],"questions":[],"next_step":null,` + gen + `}`,
		"computed number":   `{"schema":"deal_coach.output.v1",` + sit + `,"risks":[],"questions":[],"next_step":{"text":"Предложить 405000 сом со скидкой.","refs":["deal:1"]},` + gen + `}`,
		"unknown ref":       `{"schema":"deal_coach.output.v1",` + sit + `,"risks":[{"text":"Конкурент предложил дешевле.","refs":["activity:zzz"]}],"questions":[],"next_step":null,` + gen + `}`,
		"false empty":       `{"schema":"deal_coach.output.v1",` + sit + `,"risks":[],"questions":[],"next_step":null,"insufficient_data":true,"language":"ru","generator":{"provider":"fixed","model":"m","is_mock":false}}`,
	}
	for name, out := range cases {
		t.Run(name, func(t *testing.T) {
			gw, _ := modelgw.New(map[string]modelgw.Provider{"deal_coach": fixed{out}})
			_, err := runCoach(t, gw, coachInput)
			var p *jobtypes.PermanentError
			if !jobtypesIsPermanent(err, &p) || p.Code != "output_not_grounded" {
				t.Fatalf("want output_not_grounded, got %v", err)
			}
		})
	}
	// Numbers from the deal, the dates and the notes are fine.
	ok := `{"schema":"deal_coach.output.v1",` + sit + `,"risks":[{"text":"Клиент просит скидку 10%; дата закрытия 20 октября прошла.","refs":["activity:a","deal:1"]}],"questions":[{"text":"Готов ли клиент к сумме 450000 KGS?","refs":["deal:1"]}],"next_step":{"text":"Подготовить демо на 3 ноября.","refs":["meeting:c"]},` + gen + `}`
	gw, _ := modelgw.New(map[string]modelgw.Provider{"deal_coach": fixed{ok}})
	if _, err := runCoach(t, gw, coachInput); err != nil {
		t.Fatalf("grounded brief rejected: %v", err)
	}
}
