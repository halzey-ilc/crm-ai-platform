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

func gateway(t *testing.T) *modelgw.Gateway {
	t.Helper()
	gw, err := modelgw.New(map[string]modelgw.Provider{
		"call_summary":      mock.Provider{},
		"operations_digest": mock.Provider{},
	})
	if err != nil {
		t.Fatal(err)
	}
	return gw
}

const digestInput = `{"language":"ru","thresholds":{"stale_lead_days":3,"stalled_deal_days":14},
 "counts":{"stale_lead":2,"overdue_task":1,"deal_without_next_step":0,"stalled_deal":0},
 "findings":[{"ref":"lead-a","kind":"stale_lead","age_days":4},
             {"ref":"lead-b","kind":"stale_lead","age_days":9},
             {"ref":"task-c","kind":"overdue_task","age_days":2}]}`

func TestBuiltinRegistryHasBothTypes(t *testing.T) {
	reg := testutil.Registry(t)
	for _, typ := range []string{"call.summary.v1", "operations.digest.v1"} {
		if _, ok := reg.Get(typ); !ok {
			t.Errorf("%s not registered", typ)
		}
	}
	if err := reg.ValidateInput("operations.digest.v1", json.RawMessage(digestInput)); err != nil {
		t.Fatalf("valid digest input rejected: %v", err)
	}
	// Free text is not part of the digest contract.
	bad := strings.Replace(digestInput, `"age_days":4}`, `"age_days":4,"name":"Иван Петров"}`, 1)
	if err := reg.ValidateInput("operations.digest.v1", json.RawMessage(bad)); err == nil {
		t.Fatal("unknown fields (e.g. names) must be rejected")
	}
}

func TestMockDigestIsValidAndGrounded(t *testing.T) {
	reg := testutil.Registry(t)
	res, err := reg.Execute(context.Background(), gateway(t), jobs.Job{
		ID: "j1", TenantID: "acme", Type: "operations.digest.v1", Input: json.RawMessage(digestInput),
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	var out struct {
		Headline string `json:"headline"`
		Items    []struct {
			Kind string   `json:"kind"`
			Refs []string `json:"finding_refs"`
		} `json:"items"`
		InsufficientData bool `json:"insufficient_data"`
	}
	_ = json.Unmarshal(res.Output, &out)
	if out.InsufficientData || len(out.Items) != 2 {
		t.Fatalf("unexpected digest: %s", res.Output)
	}
	// Overdue tasks are listed first; stale leads cite the oldest lead first.
	if out.Items[0].Kind != "overdue_task" || out.Items[1].Refs[0] != "lead-b" {
		t.Fatalf("unexpected order: %s", res.Output)
	}
	if !strings.Contains(out.Headline, "3") {
		t.Fatalf("headline must carry the total: %q", out.Headline)
	}
}

func TestEmptyDigestSaysSo(t *testing.T) {
	reg := testutil.Registry(t)
	empty := `{"language":"en","thresholds":{"stale_lead_days":3,"stalled_deal_days":14},
	 "counts":{"stale_lead":0,"overdue_task":0,"deal_without_next_step":0,"stalled_deal":0},"findings":[]}`
	res, err := reg.Execute(context.Background(), gateway(t), jobs.Job{
		ID: "j2", TenantID: "acme", Type: "operations.digest.v1", Input: json.RawMessage(empty),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(res.Output), `"insufficient_data":true`) {
		t.Fatalf("empty input must flag insufficient_data: %s", res.Output)
	}
}

// fixed returns a canned output, standing in for a misbehaving model.
type fixed struct{ out string }

func (fixed) Name() string         { return "fixed" }
func (fixed) Supports(string) bool { return true }
func (f fixed) Invoke(context.Context, modelgw.Request) (modelgw.Response, error) {
	return modelgw.Response{Output: json.RawMessage(f.out), Model: "m"}, nil
}

func TestDigestGroundingRejectsInventedContent(t *testing.T) {
	gen := `"language":"ru","generator":{"provider":"fixed","model":"m","is_mock":true}`
	cases := map[string]string{
		"unknown ref":          `{"schema":"operations_digest.output.v1","headline":"h","items":[{"kind":"stale_lead","text":"t","finding_refs":["lead-zzz"]}],"recommendations":[],"insufficient_data":false,` + gen + `}`,
		"ref under wrong kind": `{"schema":"operations_digest.output.v1","headline":"h","items":[{"kind":"stale_lead","text":"t","finding_refs":["task-c"]}],"recommendations":[],"insufficient_data":false,` + gen + `}`,
		"kind with zero count": `{"schema":"operations_digest.output.v1","headline":"h","items":[{"kind":"stalled_deal","text":"t","finding_refs":["lead-a"]}],"recommendations":[],"insufficient_data":false,` + gen + `}`,
		"recommendation ref":   `{"schema":"operations_digest.output.v1","headline":"h","items":[],"recommendations":[{"text":"t","finding_refs":["nope"]}],"insufficient_data":false,` + gen + `}`,
		"false insufficiency":  `{"schema":"operations_digest.output.v1","headline":"h","items":[],"recommendations":[],"insufficient_data":true,` + gen + `}`,
	}
	reg := testutil.Registry(t)
	for name, out := range cases {
		t.Run(name, func(t *testing.T) {
			gw, _ := modelgw.New(map[string]modelgw.Provider{"operations_digest": fixed{out}})
			_, err := reg.Execute(context.Background(), gw, jobs.Job{
				ID: "j", TenantID: "acme", Type: "operations.digest.v1", Input: json.RawMessage(digestInput),
			})
			var p *jobtypes.PermanentError
			if !jobtypesIsPermanent(err, &p) || p.Code != "output_not_grounded" {
				t.Fatalf("want output_not_grounded, got %v", err)
			}
		})
	}
}

func jobtypesIsPermanent(err error, target **jobtypes.PermanentError) bool {
	if !jobtypes.IsPermanent(err) {
		return false
	}
	p, ok := err.(*jobtypes.PermanentError)
	*target = p
	return ok
}
