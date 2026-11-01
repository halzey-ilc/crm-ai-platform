// Command playground serves a local web page for trying the job types by hand:
// pick a synthetic example, edit its input, send it to the running gateway and
// read the result. It mints short-lived dev tokens itself (like devtoken), so it
// refuses to run with AI_ENV=production and listens on loopback only.
//
//	set -a; source .env; set +a
//	go run ./tools/playground          # then open http://127.0.0.1:8095
//
// Only synthetic data belongs here: whatever is typed into the page is sent to
// the model and stored as a job of the playground tenant.
package main

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"crmai/internal/auth"
)

//go:embed index.html
var static embed.FS

type example struct {
	ID       string          `json:"id"`
	Type     string          `json:"type"`
	Language string          `json:"language,omitempty"`
	Input    json.RawMessage `json:"input"`
}

type server struct {
	gateway string
	tenant  string
	key     []byte
	iss     string
	aud     string
	client  *http.Client
	cases   []example
}

func main() {
	addr := flag.String("addr", "127.0.0.1:8095", "listen address (loopback only)")
	gateway := flag.String("gateway", "http://127.0.0.1:8090", "gateway URL")
	tenant := flag.String("tenant", "playground", "tenant the jobs are created for")
	datasets := flag.String("datasets", "evaluation/datasets", "directory with synthetic *.jsonl examples")
	flag.Parse()

	if os.Getenv("AI_ENV") == "production" {
		log.Fatal("playground: refusing to run with AI_ENV=production")
	}
	key := os.Getenv("AI_AUTH_HS256_KEY")
	if len(key) < 32 {
		log.Fatal("playground: AI_AUTH_HS256_KEY must be set (>= 32 bytes): set -a; source .env; set +a")
	}
	if err := requireLoopback(*addr); err != nil {
		log.Fatal("playground: ", err)
	}
	files, _ := filepath.Glob(filepath.Join(*datasets, "*.jsonl"))
	cases, err := loadExamples(files)
	if err != nil {
		log.Fatal("playground: ", err)
	}
	s := &server{
		gateway: strings.TrimRight(*gateway, "/"), tenant: *tenant, key: []byte(key),
		iss: envOr("AI_AUTH_ISSUER", "crm-platform"), aud: envOr("AI_AUTH_AUDIENCE", "crm-ai-platform"),
		client: &http.Client{Timeout: 30 * time.Second}, cases: cases,
	}
	log.Printf("playground: %d examples, gateway %s, open http://%s", len(cases), s.gateway, *addr)
	srv := &http.Server{Addr: *addr, Handler: s.routes(), ReadHeaderTimeout: 10 * time.Second}
	log.Fatal(srv.ListenAndServe())
}

func envOr(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

// requireLoopback keeps the page (and the tokens it mints) off the network.
func requireLoopback(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return err
	}
	if host == "localhost" {
		return nil
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return nil
	}
	return fmt.Errorf("listen address %q is not loopback", addr)
}

// loadExamples reads evaluation cases, skipping blank and // comment lines. Lines
// of the older call-summary format become call.summary.v2 inputs, as in evalrun.
func loadExamples(files []string) ([]example, error) {
	var out []example
	for _, name := range files {
		f, err := os.Open(name)
		if err != nil {
			return nil, err
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		for n := 1; sc.Scan(); n++ {
			line := strings.TrimSpace(sc.Text())
			if line == "" || strings.HasPrefix(line, "//") {
				continue
			}
			var c struct {
				example
				Transcript string `json:"transcript"`
			}
			if err := json.Unmarshal([]byte(line), &c); err != nil {
				f.Close()
				return nil, fmt.Errorf("%s:%d: %w", name, n, err)
			}
			if c.Type == "" && c.Transcript != "" {
				c.Type = "call.summary.v2"
				c.Input, _ = json.Marshal(map[string]string{"transcript": c.Transcript, "language": c.Language})
			}
			if c.Language == "" {
				var in struct {
					Language string `json:"language"`
				}
				_ = json.Unmarshal(c.Input, &in)
				c.Language = in.Language
			}
			if c.ID != "" && c.Type != "" && len(c.Input) > 0 {
				out = append(out, c.example)
			}
		}
		f.Close()
		if err := sc.Err(); err != nil {
			return nil, err
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Type < out[j].Type })
	return out, nil
}

var jobID = regexp.MustCompile(`^[0-9a-fA-F-]{8,64}$`)

func (s *server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /", http.FileServerFS(static))
	mux.HandleFunc("GET /api/examples", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(s.cases)
	})
	mux.HandleFunc("POST /api/run", s.run)
	mux.HandleFunc("GET /api/jobs/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if !jobID.MatchString(id) {
			http.Error(w, "bad job id", http.StatusBadRequest)
			return
		}
		s.forward(w, http.MethodGet, "/v1/jobs/"+id, nil, "")
	})
	return mux
}

func (s *server) run(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Type  string          `json:"type"`
		Input json.RawMessage `json:"input"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256<<10))
	if err := dec.Decode(&body); err != nil || body.Type == "" || len(body.Input) == 0 {
		http.Error(w, "need {type, input} with input as a JSON object", http.StatusBadRequest)
		return
	}
	var obj map[string]any
	if json.Unmarshal(body.Input, &obj) != nil {
		http.Error(w, "input must be a JSON object", http.StatusBadRequest)
		return
	}
	payload, _ := json.Marshal(body)
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	s.forward(w, http.MethodPost, "/v1/jobs", payload, "playground-"+hex.EncodeToString(b))
}

// forward calls the gateway with a fresh one-minute token and copies its answer.
func (s *server) forward(w http.ResponseWriter, method, path string, body []byte, idemKey string) {
	tok, err := auth.Mint(auth.MintRequest{
		Key: s.key, Issuer: s.iss, Audience: s.aud, Subject: "user:playground", Tenant: s.tenant,
		Scopes: []string{auth.ScopeJobsCreate, auth.ScopeJobsRead}, TTL: time.Minute,
	})
	if err != nil {
		http.Error(w, "cannot mint a token", http.StatusInternalServerError)
		return
	}
	req, _ := http.NewRequest(method, s.gateway+path, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	if idemKey != "" {
		req.Header.Set("Idempotency-Key", idemKey)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		var netErr net.Error
		msg := "gateway unreachable: is the stack running (make up-llm)?"
		if errors.As(err, &netErr) && netErr.Timeout() {
			msg = "gateway timed out"
		}
		http.Error(w, msg, http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, io.LimitReader(resp.Body, 4<<20))
}
