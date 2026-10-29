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

func runSummaryV2(t *testing.T, gw *modelgw.Gateway, transcript string) (jobs.Result, error) {
	t.Helper()
	in, _ := json.Marshal(map[string]string{"transcript": transcript, "language": "ru"})
	return testutil.Registry(t).Execute(context.Background(), gw, jobs.Job{
		ID: "s2", TenantID: "acme", Type: "call.summary.v2", Input: in,
	})
}

func TestCallSummaryV2FlagsTextsWithNothingToSummarise(t *testing.T) {
	gw, _ := modelgw.New(map[string]modelgw.Provider{"call_summary_v2": mock.Provider{}})
	res, err := runSummaryV2(t, gw, "Не дозвонился.")
	if err != nil {
		t.Fatal(err)
	}
	out := string(res.Output)
	if !strings.Contains(out, `"insufficient_data":true`) || !strings.Contains(out, "Недостаточно данных") ||
		!strings.Contains(out, `"key_points":[]`) || res.SchemaID != "call_summary.output.v2" {
		t.Fatalf("a too-short note must be reported as insufficient: %s", out)
	}
	res, err = runSummaryV2(t, gw, "Клиенту нужна интеграция с 1С. Договорились, что я перезвоню в пятницу.")
	if err != nil || !strings.Contains(string(res.Output), `"insufficient_data":false`) {
		t.Fatalf("a real note must be summarised: %v %s", err, res.Output)
	}
}

func TestCallSummaryV2GroundingRejectsInventedSummaries(t *testing.T) {
	gen := `"language":"ru","generator":{"provider":"fixed","model":"m","is_mock":false}`
	cases := map[string]struct{ transcript, out string }{
		"short text summarised": {"Не дозвонился.",
			`{"schema":"call_summary.output.v2","summary":"Клиент заинтересован.","key_points":[],"next_step":null,"insufficient_data":false,` + gen + `}`},
		"free text without facts": {"Клиенту нужна интеграция с 1С и обучение сотрудников.",
			`{"schema":"call_summary.output.v2","summary":"Клиент готов купить.","key_points":[],"next_step":null,"insufficient_data":true,` + gen + `}`},
		"flag hides facts": {"Клиенту нужна интеграция с 1С и обучение сотрудников.",
			`{"schema":"call_summary.output.v2","summary":"Недостаточно данных: в заметках звонка нет фактов для резюме.","key_points":[{"text":"1С","evidence_quote":"интеграция с 1С"}],"next_step":null,"insufficient_data":true,` + gen + `}`},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			gw, _ := modelgw.New(map[string]modelgw.Provider{"call_summary_v2": fixed{tc.out}})
			_, err := runSummaryV2(t, gw, tc.transcript)
			var p *jobtypes.PermanentError
			if !jobtypesIsPermanent(err, &p) || p.Code != "output_not_grounded" {
				t.Fatalf("want output_not_grounded, got %v", err)
			}
		})
	}
}
