package platform_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"crmai/contracts"
	"crmai/internal/jobtypes"
	"crmai/internal/modelgw"
	"crmai/internal/platform"
)

// llm answers every chat request with an empty agent plan and counts calls.
func llm(t *testing.T, calls *atomic.Int32) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{"content": `{"can_do":false,"actions":[]}`}, "finish_reason": "stop"}},
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestLargeModelServesOnlyTheListedTasks(t *testing.T) {
	schemas, _ := contracts.Load()
	reg, err := jobtypes.Builtin(schemas)
	if err != nil {
		t.Fatal(err)
	}
	var small, large atomic.Int32
	smallSrv, largeSrv := llm(t, &small), llm(t, &large)
	gw, err := platform.ModelGateway(reg, platform.Providers{
		Enabled: []string{"llamacpp"}, LlamaCppURL: smallSrv.URL, LlamaCppModel: "small",
		LlamaCppLargeURL: largeSrv.URL, LlamaCppLargeModel: "large", LargeTasks: []string{"agent_plan_v3"},
	})
	if err != nil {
		t.Fatal(err)
	}
	input := json.RawMessage(`{"language":"ru","today":"2026-10-31","request":"удали всё","tools":["create_task"],
	 "subjects":[{"ref":"deal:11111111-1111-4111-8111-111111111111","kind":"deal","title":"A"}]}`)
	resp, err := gw.Invoke(context.Background(), modelgw.Request{Task: "agent_plan_v3", Input: input})
	if err != nil {
		t.Fatal(err)
	}
	if large.Load() != 1 || small.Load() != 0 || !strings.Contains(string(resp.Output), `"model":"large"`) {
		t.Fatalf("agent_plan_v3 must go to the large model: small=%d large=%d %s", small.Load(), large.Load(), resp.Output)
	}
	if _, err := gw.Invoke(context.Background(), modelgw.Request{Task: "agent_plan_v2", Input: input}); err != nil {
		t.Fatal(err)
	}
	if small.Load() != 1 {
		t.Fatalf("other tasks stay on the default model: small=%d", small.Load())
	}

	if _, err := platform.ModelGateway(reg, platform.Providers{Enabled: []string{"llamacpp"}, LlamaCppURL: smallSrv.URL,
		LlamaCppLargeURL: largeSrv.URL, LargeTasks: []string{"no_such_task"}}); err == nil {
		t.Fatal("an unknown task must be a startup error")
	}
	if _, err := platform.ModelGateway(reg, platform.Providers{Enabled: []string{"mock"},
		LargeTasks: []string{"agent_plan_v3"}}); err == nil {
		t.Fatal("large tasks without a large URL must be a startup error")
	}
}
