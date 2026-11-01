// Command evalrun runs synthetic evaluation cases through the real job types
// (prompts, output contracts, grounding checks) against a model provider, without
// the queue or the database. It is meant for measuring a model on rented or free
// GPU machines (e.g. Kaggle) where only a llama.cpp server is available.
//
//	evalrun -provider llamacpp -llm http://127.0.0.1:8080 -model qwen3.5-4b-q4km \
//	        -out report.json evaluation/datasets/*.jsonl
//
// A case is one JSON line:
//
//	{"id": "kpi-ru-01", "type": "kpi.explanation.v1", "input": {...},
//	 "expect": {"contains": ["конверс"]}}
//
// Lines of the older call-summary format ({"id", "language", "transcript",
// "expect_facts", "expect_next_step"}) run as call.summary.v2.
//
// Retries follow the orchestrator: transient errors are retried up to the job
// type's MaxAttempts, contract and grounding failures fail at once. Only
// synthetic data belongs in these files.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"crmai/contracts"
	"crmai/internal/jobs"
	"crmai/internal/jobtypes"
	"crmai/internal/modelgw"
	"crmai/internal/platform"
)

type expect struct {
	Contains []string `json:"contains"`
	// NextStep, when set, says whether a call summary must return a next step.
	NextStep *bool `json:"next_step"`
	// Insufficient, when set, says whether the output must report insufficient_data.
	Insufficient *bool `json:"insufficient_data"`
	// Tools must each appear among an agent plan's proposed actions.
	Tools []string `json:"tools"`
	// MaxActions caps the number of proposed actions (0: the plan must be empty).
	MaxActions *int `json:"max_actions"`
}

type evalCase struct {
	ID     string          `json:"id"`
	Type   string          `json:"type"`
	Input  json.RawMessage `json:"input"`
	Expect expect          `json:"expect"`

	// older call-summary format
	Language       string   `json:"language"`
	Transcript     string   `json:"transcript"`
	ExpectFacts    []string `json:"expect_facts"`
	ExpectNextStep *bool    `json:"expect_next_step"`

	file string
}

type caseResult struct {
	ID            string          `json:"id"`
	Type          string          `json:"type"`
	File          string          `json:"file"`
	Language      string          `json:"language"`
	Status        string          `json:"status"` // succeeded | failed | bad_case
	ErrorCode     string          `json:"error_code,omitempty"`
	Error         string          `json:"error,omitempty"`
	Attempts      int             `json:"attempts"`
	FirstTryOK    bool            `json:"first_try_ok"`
	LatencyS      float64         `json:"latency_s"`
	ContainsFound *float64        `json:"contains_found,omitempty"`
	Missed        []string        `json:"missed,omitempty"`
	ChecksFailed  []string        `json:"checks_failed,omitempty"`
	Output        json.RawMessage `json:"output,omitempty"`
}

func main() {
	provider := flag.String("provider", "llamacpp", "model provider: llamacpp or mock")
	llmURL := flag.String("llm", "http://127.0.0.1:8080", "llama.cpp server URL")
	model := flag.String("model", "unknown-gguf", "label recorded as the model of each result")
	out := flag.String("out", "", "write the full JSON report here")
	only := flag.String("type", "", "run only cases of this job type")
	requireSuccess := flag.Bool("require-success", false, "exit 1 if any case fails its contract or grounding (CI gate)")
	baseline := flag.String("baseline", "", "earlier report: exit 1 if a case that passed there fails now")
	flag.Parse()
	if flag.NArg() == 0 {
		fmt.Fprintln(os.Stderr, "usage: evalrun [flags] cases.jsonl ...")
		os.Exit(2)
	}
	results, err := run(*provider, *llmURL, *model, *out, *only, flag.Args())
	if err != nil {
		fmt.Fprintln(os.Stderr, "evalrun:", err)
		os.Exit(1)
	}
	var prior []caseResult
	if *baseline != "" {
		if prior, err = loadReport(*baseline); err != nil {
			fmt.Fprintln(os.Stderr, "evalrun:", err)
			os.Exit(1)
		}
	}
	if problems := gate(results, *requireSuccess, prior); len(problems) > 0 {
		for _, p := range problems {
			fmt.Fprintln(os.Stderr, "FAIL", p)
		}
		os.Exit(1)
	}
}

func run(provider, llmURL, model, out, only string, files []string) ([]caseResult, error) {
	schemas, err := contracts.Load()
	if err != nil {
		return nil, err
	}
	types, err := jobtypes.Builtin(schemas)
	if err != nil {
		return nil, err
	}
	gw, err := platform.ModelGateway(types, platform.Providers{
		Enabled:       []string{provider},
		LlamaCppURL:   llmURL,
		LlamaCppModel: model,
		Log:           slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		return nil, err
	}
	cases, err := load(files)
	if err != nil {
		return nil, err
	}
	var results []caseResult
	for _, c := range cases {
		if only != "" && c.Type != only {
			continue
		}
		r := runCase(types, gw, c)
		results = append(results, r)
		fmt.Printf("%-28s %-22s %-9s %-26s tries=%d %6.1fs%s\n", c.ID, c.Type, r.Status, r.ErrorCode,
			r.Attempts, r.LatencyS, foundSuffix(r))
	}
	summary := summarise(results)
	printSummary(summary)
	if out != "" {
		report := map[string]any{
			"provider": provider, "model": model, "finished_at": time.Now().UTC().Format(time.RFC3339),
			"summary": summary, "results": results,
		}
		raw, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			return nil, err
		}
		if err := os.WriteFile(out, append(raw, '\n'), 0o644); err != nil {
			return nil, err
		}
		fmt.Println("report:", out)
	}
	return results, nil
}

func load(files []string) ([]evalCase, error) {
	var cases []evalCase
	seen := map[string]bool{}
	for _, name := range files {
		f, err := os.Open(name)
		if err != nil {
			return nil, err
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		line := 0
		for sc.Scan() {
			line++
			text := strings.TrimSpace(sc.Text())
			if text == "" || strings.HasPrefix(text, "//") {
				continue
			}
			var c evalCase
			if err := json.Unmarshal([]byte(text), &c); err != nil {
				f.Close()
				return nil, fmt.Errorf("%s:%d: %w", name, line, err)
			}
			if c.Type == "" && c.Transcript != "" {
				c.Type = "call.summary.v2"
				c.Input, _ = json.Marshal(map[string]string{"transcript": c.Transcript, "language": c.Language})
				c.Expect = expect{Contains: c.ExpectFacts, NextStep: c.ExpectNextStep}
			}
			if c.ID == "" || c.Type == "" || len(c.Input) == 0 {
				f.Close()
				return nil, fmt.Errorf("%s:%d: a case needs id, type and input", name, line)
			}
			if seen[c.ID] {
				f.Close()
				return nil, fmt.Errorf("%s:%d: duplicate case id %q", name, line, c.ID)
			}
			seen[c.ID] = true
			c.file = filepath.Base(name)
			cases = append(cases, c)
		}
		f.Close()
		if err := sc.Err(); err != nil {
			return nil, err
		}
	}
	return cases, nil
}

func runCase(types *jobtypes.Registry, gw *modelgw.Gateway, c evalCase) caseResult {
	r := caseResult{ID: c.ID, Type: c.Type, File: c.file, Language: language(c.Input)}
	maxAttempts, timeout, ok := types.Lookup(c.Type)
	if !ok {
		r.Status, r.ErrorCode = "bad_case", "unknown_job_type"
		return r
	}
	if err := types.ValidateInput(c.Type, c.Input); err != nil {
		r.Status, r.ErrorCode, r.Error = "bad_case", "input_contract_violation", err.Error()
		return r
	}
	job := jobs.Job{ID: "eval-" + c.ID, TenantID: "eval", Type: c.Type, Input: c.Input}
	started := time.Now()
	var res jobs.Result
	var err error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		r.Attempts = attempt
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		res, err = types.Execute(ctx, gw, job)
		cancel()
		if err == nil || !modelgw.IsTransient(err) {
			break
		}
	}
	r.LatencyS = float64(time.Since(started).Milliseconds()) / 1000
	if err != nil {
		r.Status, r.ErrorCode, r.Error = "failed", errorCode(err), err.Error()
		return r
	}
	r.Status, r.FirstTryOK, r.Output = "succeeded", r.Attempts == 1, res.Output
	check(&r, c.Expect, res.Output)
	return r
}

func errorCode(err error) string {
	var p *jobtypes.PermanentError
	if errors.As(err, &p) {
		return p.Code
	}
	if modelgw.IsTransient(err) {
		return "transient_exhausted"
	}
	return "error"
}

// check applies the case's expectations. They are rough automatic signals; a
// person still reads the outputs.
func check(r *caseResult, e expect, output json.RawMessage) {
	if len(e.Contains) > 0 {
		text := strings.ToLower(textOf(output))
		found := 0
		for _, want := range e.Contains {
			if strings.Contains(text, strings.ToLower(want)) {
				found++
			} else {
				r.Missed = append(r.Missed, want)
			}
		}
		share := float64(found) / float64(len(e.Contains))
		r.ContainsFound = &share
	}
	var cs struct {
		NextStep     json.RawMessage `json:"next_step"`
		Insufficient *bool           `json:"insufficient_data"`
		Actions      []struct {
			Tool string `json:"tool"`
		} `json:"actions"`
	}
	tools := map[string]bool{}
	_ = json.Unmarshal(output, &cs)
	if e.NextStep != nil {
		has := len(cs.NextStep) > 0 && string(cs.NextStep) != "null"
		if has != *e.NextStep {
			r.ChecksFailed = append(r.ChecksFailed, "next_step")
		}
	}
	if e.Insufficient != nil && (cs.Insufficient == nil || *cs.Insufficient != *e.Insufficient) {
		r.ChecksFailed = append(r.ChecksFailed, "insufficient_data")
	}
	for _, a := range cs.Actions {
		tools[a.Tool] = true
	}
	for _, want := range e.Tools {
		if !tools[want] {
			r.ChecksFailed = append(r.ChecksFailed, "tool:"+want)
		}
	}
	if e.MaxActions != nil && len(cs.Actions) > *e.MaxActions {
		r.ChecksFailed = append(r.ChecksFailed, "max_actions")
	}
}

// textOf concatenates every string value of a JSON document except the
// generator block, so expectations match what a user would read.
func textOf(raw json.RawMessage) string {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return ""
	}
	var b strings.Builder
	var walk func(any)
	walk = func(x any) {
		switch t := x.(type) {
		case string:
			b.WriteString(t)
			b.WriteByte('\n')
		case []any:
			for _, e := range t {
				walk(e)
			}
		case map[string]any:
			for k, e := range t {
				if k != "generator" {
					walk(e)
				}
			}
		}
	}
	walk(v)
	return b.String()
}

func language(input json.RawMessage) string {
	var v struct {
		Language string `json:"language"`
	}
	_ = json.Unmarshal(input, &v)
	return v.Language
}

func foundSuffix(r caseResult) string {
	var s string
	if r.ContainsFound != nil {
		s += fmt.Sprintf("  found=%.2f", *r.ContainsFound)
	}
	if len(r.ChecksFailed) > 0 {
		s += "  checks_failed=" + strings.Join(r.ChecksFailed, ",")
	}
	return s
}

type typeSummary struct {
	Type           string            `json:"type"`
	Cases          int               `json:"cases"`
	Succeeded      int               `json:"succeeded"`
	FirstTry       int               `json:"first_try_ok"`
	BadCases       int               `json:"bad_cases"`
	Errors         map[string]int    `json:"errors"`
	ChecksFailed   int               `json:"checks_failed"`
	ContainsMean   *float64          `json:"contains_found_mean"`
	LatencyMedianS *float64          `json:"latency_median_s"`
	LatencyMaxS    *float64          `json:"latency_max_s"`
	ByLanguageOK   map[string]string `json:"by_language"`
}

func summarise(results []caseResult) []typeSummary {
	by := map[string][]caseResult{}
	for _, r := range results {
		by[r.Type] = append(by[r.Type], r)
	}
	names := make([]string, 0, len(by))
	for t := range by {
		names = append(names, t)
	}
	sort.Strings(names)
	var out []typeSummary
	for _, t := range names {
		s := typeSummary{Type: t, Errors: map[string]int{}, ByLanguageOK: map[string]string{}}
		var lat, found []float64
		langTotal, langOK := map[string]int{}, map[string]int{}
		for _, r := range by[t] {
			s.Cases++
			switch r.Status {
			case "succeeded":
				s.Succeeded++
				if r.FirstTryOK {
					s.FirstTry++
				}
				if len(r.ChecksFailed) > 0 {
					s.ChecksFailed++
				}
				if r.ContainsFound != nil {
					found = append(found, *r.ContainsFound)
				}
			case "bad_case":
				s.BadCases++
			default:
				s.Errors[r.ErrorCode]++
			}
			if r.Status != "bad_case" {
				lat = append(lat, r.LatencyS)
				langTotal[r.Language]++
				if r.Status == "succeeded" {
					langOK[r.Language]++
				}
			}
		}
		for l, n := range langTotal {
			s.ByLanguageOK[l] = fmt.Sprintf("%d/%d", langOK[l], n)
		}
		if len(found) > 0 {
			m := mean(found)
			s.ContainsMean = &m
		}
		if len(lat) > 0 {
			slices.Sort(lat)
			med, mx := median(lat), lat[len(lat)-1]
			s.LatencyMedianS, s.LatencyMaxS = &med, &mx
		}
		out = append(out, s)
	}
	return out
}

func printSummary(sums []typeSummary) {
	fmt.Println()
	fmt.Printf("%-22s %5s %4s %9s %6s %8s %10s %10s  %s\n",
		"type", "cases", "ok", "first_try", "checks", "found", "median_s", "max_s", "errors / by language")
	for _, s := range sums {
		fmt.Printf("%-22s %5d %4d %9d %6d %8s %10s %10s  %v %v\n",
			s.Type, s.Cases, s.Succeeded, s.FirstTry, s.ChecksFailed, opt(s.ContainsMean),
			opt(s.LatencyMedianS), opt(s.LatencyMaxS), s.Errors, s.ByLanguageOK)
		if s.BadCases > 0 {
			fmt.Printf("%-22s %d case(s) do not match the input contract (fix the dataset)\n", "", s.BadCases)
		}
	}
}

func opt(v *float64) string {
	if v == nil {
		return "-"
	}
	return fmt.Sprintf("%.2f", *v)
}

func mean(xs []float64) float64 {
	var t float64
	for _, x := range xs {
		t += x
	}
	return t / float64(len(xs))
}

func median(sorted []float64) float64 {
	n := len(sorted)
	if n%2 == 1 {
		return sorted[n/2]
	}
	return (sorted[n/2-1] + sorted[n/2]) / 2
}
