package whispercpp_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"crmai/contracts"
	"crmai/internal/jobtypes"
	"crmai/internal/modelgw"
	"crmai/internal/modelgw/whispercpp"
)

type audioStore map[string]whispercpp.Audio

func (s audioStore) GetAudio(_ context.Context, tenant, ref string) (whispercpp.Audio, bool, error) {
	a, ok := s[tenant+"/"+ref]
	return a, ok, nil
}

// verbose_json as whisper-server returns it with diarize on a stereo file.
const stereoAnswer = `{"task":"transcribe","language":"russian","duration":9.5,"text":"...",
 "segments":[
  {"id":0,"start":0.0,"end":2.4,"text":" Здравствуйте, компания Техносфера.","speaker":"0"},
  {"id":1,"start":2.4,"end":3.0,"text":" [BLANK_AUDIO]","speaker":"?"},
  {"id":2,"start":3.0,"end":6.1,"text":"  Добрый день, нужна интеграция с 1С. ","speaker":"1"},
  {"id":3,"start":6.1,"end":9.5,"text":" Договорились, перезвоню в пятницу.","speaker":"?"}]}`

func whisper(t *testing.T, status int, body string, seen *map[string]string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/inference" {
			http.NotFound(w, r)
			return
		}
		if seen != nil {
			if err := r.ParseMultipartForm(1 << 20); err != nil {
				t.Errorf("not a multipart form: %v", err)
			}
			m := map[string]string{}
			for k, v := range r.MultipartForm.Value {
				m[k] = v[0]
			}
			f, h, err := r.FormFile("file")
			if err == nil {
				b, _ := io.ReadAll(f)
				m["file"], m["filename"] = string(b), h.Filename
			}
			*seen = m
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func invoke(p whispercpp.Provider, input string) (modelgw.Response, error) {
	return p.Invoke(context.Background(), modelgw.Request{Task: whispercpp.TaskTranscribe, TenantID: "t1", Input: json.RawMessage(input)})
}

var store = audioStore{"t1/call-1": {Bytes: []byte("ID3fake-mp3"), ContentType: "audio/mpeg"}}

func TestStereoRecordingBecomesSpeakerSegments(t *testing.T) {
	var seen map[string]string
	srv := whisper(t, 200, stereoAnswer, &seen)
	p := whispercpp.Provider{URL: srv.URL, Model: "whisper-small", Audio: store}
	input := `{"audio_ref":"call-1","language":"ru","channels":"manager_left"}`
	resp, err := invoke(p, input)
	if err != nil {
		t.Fatal(err)
	}
	if seen["file"] != "ID3fake-mp3" || seen["filename"] != "recording.mp3" || seen["language"] != "ru" ||
		seen["response_format"] != "verbose_json" || seen["diarize"] != "true" {
		t.Fatalf("the recording and options must be sent as whisper-server expects: %v", seen)
	}
	schemas, _ := contracts.Load()
	if err := schemas.Validate("call_transcript.output.v1", resp.Output); err != nil {
		t.Fatalf("contract: %v\n%s", err, resp.Output)
	}
	def := jobtypes.CallTranscribeV1()
	if err := def.Ground(json.RawMessage(input), resp.Output); err != nil {
		t.Fatalf("grounding: %v\n%s", err, resp.Output)
	}
	var out struct {
		Segments []whispercpp.Segment `json:"segments"`
		Text     string               `json:"text"`
	}
	_ = json.Unmarshal(resp.Output, &out)
	want := []string{"manager", "customer", "unknown"}
	if len(out.Segments) != 3 {
		t.Fatalf("non-speech must be dropped, 3 segments left: %s", resp.Output)
	}
	for i, s := range out.Segments {
		if s.Speaker != want[i] {
			t.Fatalf("segment %d: speaker %q, want %q (left channel = manager)", i, s.Speaker, want[i])
		}
	}
	if out.Segments[1].Text != "Добрый день, нужна интеграция с 1С." || !strings.HasPrefix(out.Text, "Здравствуйте") {
		t.Fatalf("whitespace must be normalised and text joined: %+v", out)
	}

	// Manager on the right channel swaps the labels.
	p2 := p
	resp, _ = invoke(p2, `{"audio_ref":"call-1","language":"ru","channels":"manager_right"}`)
	_ = json.Unmarshal(resp.Output, &out)
	if out.Segments[0].Speaker != "customer" || out.Segments[1].Speaker != "manager" {
		t.Fatalf("manager_right must swap speakers: %+v", out.Segments)
	}
}

func TestMonoRecordingHasNoSpeakersAndNoDiarization(t *testing.T) {
	var seen map[string]string
	srv := whisper(t, 200, `{"duration":3,"segments":[{"start":0,"end":3,"text":"Алло"}]}`, &seen)
	p := whispercpp.Provider{URL: srv.URL, Audio: store}
	input := `{"audio_ref":"call-1","language":"ru","channels":"mono"}`
	resp, err := invoke(p, input)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := seen["diarize"]; ok {
		t.Fatal("a mono recording must not be diarized")
	}
	if err := jobtypes.CallTranscribeV1().Ground(json.RawMessage(input), resp.Output); err != nil {
		t.Fatalf("grounding: %v\n%s", err, resp.Output)
	}
	if !strings.Contains(string(resp.Output), `"speaker":"unknown"`) {
		t.Fatalf("mono speakers are unknown: %s", resp.Output)
	}
}

func TestSilenceIsInsufficientData(t *testing.T) {
	srv := whisper(t, 200, `{"duration":4,"segments":[{"start":0,"end":4,"text":" [BLANK_AUDIO]"}]}`, nil)
	resp, err := invoke(whispercpp.Provider{URL: srv.URL, Audio: store}, `{"audio_ref":"call-1","language":"en","channels":"mono"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(resp.Output), `"insufficient_data":true`) || !strings.Contains(string(resp.Output), `"segments":[]`) {
		t.Fatalf("no speech must be reported honestly: %s", resp.Output)
	}
}

func TestKyrgyzNeedsItsOwnModel(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))
	defer srv.Close()
	_, err := invoke(whispercpp.Provider{URL: srv.URL, Audio: store}, `{"audio_ref":"call-1","language":"ky","channels":"mono"}`)
	if err == nil || modelgw.IsTransient(err) || called || !strings.Contains(err.Error(), "Kyrgyz") {
		t.Fatalf("without a Kyrgyz model the job must fail at once, without a call: called=%v err=%v", called, err)
	}

	var seen map[string]string
	ky := whisper(t, 200, `{"duration":2,"segments":[{"start":0,"end":2,"text":"Саламатсызбы"}]}`, &seen)
	resp, err := invoke(whispercpp.Provider{URL: srv.URL, KyURL: ky.URL, KyLanguage: "kk", Audio: store},
		`{"audio_ref":"call-1","language":"ky","channels":"mono"}`)
	if err != nil || seen["language"] != "kk" || !strings.Contains(string(resp.Output), `"language":"ky"`) {
		t.Fatalf("Kyrgyz must go to its own server with its language token: %v %v %s", err, seen, resp.Output)
	}
}

func TestFailureClassification(t *testing.T) {
	missing := whispercpp.Provider{URL: "http://unused", Audio: store}
	if _, err := invoke(missing, `{"audio_ref":"gone","language":"ru","channels":"mono"}`); err == nil || modelgw.IsTransient(err) {
		t.Fatalf("a missing recording cannot be fixed by a retry: %v", err)
	}
	busy := whisper(t, 503, "", nil)
	if _, err := invoke(whispercpp.Provider{URL: busy.URL, Audio: store}, `{"audio_ref":"call-1","language":"ru","channels":"mono"}`); !modelgw.IsTransient(err) {
		t.Fatalf("an overloaded server is transient: %v", err)
	}
	bad := whisper(t, 400, "bad audio", nil)
	if _, err := invoke(whispercpp.Provider{URL: bad.URL, Audio: store}, `{"audio_ref":"call-1","language":"ru","channels":"mono"}`); err == nil || modelgw.IsTransient(err) {
		t.Fatalf("a rejected file is permanent: %v", err)
	}
	broken := audioStoreErr{}
	if _, err := invoke(whispercpp.Provider{URL: busy.URL, Audio: broken}, `{"audio_ref":"call-1","language":"ru","channels":"mono"}`); !modelgw.IsTransient(err) {
		t.Fatalf("a database error is transient: %v", err)
	}
}

type audioStoreErr struct{}

func (audioStoreErr) GetAudio(context.Context, string, string) (whispercpp.Audio, bool, error) {
	return whispercpp.Audio{}, false, errors.New("connection reset")
}
