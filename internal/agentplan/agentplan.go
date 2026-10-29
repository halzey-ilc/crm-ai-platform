// Package agentplan holds the input model and the rules of an assistant plan
// (capability: agent runtime). The AI Platform never executes anything: it
// proposes actions, restricted to the tools and records the CRM listed for
// this user, and the CRM runs each one only after the user confirms it.
package agentplan

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Subject is a record the user referred to.
type Subject struct {
	Ref   string `json:"ref"`
	Kind  string `json:"kind"`
	Title string `json:"title"`
	// v2, deals only: the current stage and the stages of the deal's pipeline.
	Stage  string   `json:"stage,omitempty"`
	Stages []string `json:"stages,omitempty"`
}

// Input is agent_plan.input.v1 or v2.
type Input struct {
	Language string    `json:"language"`
	Today    string    `json:"today"`
	Request  string    `json:"request"`
	Tools    []string  `json:"tools"`
	Subjects []Subject `json:"subjects"`
}

// Action is one proposed action (fields depend on the tool).
type Action struct {
	Tool       string  `json:"tool"`
	SubjectRef string  `json:"subject_ref"`
	Title      string  `json:"title,omitempty"`
	DueDate    *string `json:"due_date,omitempty"`
	Body       string  `json:"body,omitempty"`
	// v2: schedule_meeting (local time "YYYY-MM-DDTHH:MM") and move_deal_stage.
	Start           string `json:"start,omitempty"`
	DurationMinutes int    `json:"duration_minutes,omitempty"`
	Stage           string `json:"stage,omitempty"`
	Reason          string `json:"reason"`
}

// Meeting length limits for schedule_meeting.
const (
	MinMeetingMinutes = 15
	MaxMeetingMinutes = 480
)

// MarshalJSON writes exactly the fields of the action's tool, as the output
// contract requires (e.g. create_task always carries due_date, maybe null).
func (a Action) MarshalJSON() ([]byte, error) {
	switch a.Tool {
	case "create_task":
		return json.Marshal(struct {
			Tool       string  `json:"tool"`
			SubjectRef string  `json:"subject_ref"`
			Title      string  `json:"title"`
			DueDate    *string `json:"due_date"`
			Reason     string  `json:"reason"`
		}{a.Tool, a.SubjectRef, a.Title, a.DueDate, a.Reason})
	case "log_note":
		return json.Marshal(struct {
			Tool       string `json:"tool"`
			SubjectRef string `json:"subject_ref"`
			Body       string `json:"body"`
			Reason     string `json:"reason"`
		}{a.Tool, a.SubjectRef, a.Body, a.Reason})
	case "schedule_meeting":
		return json.Marshal(struct {
			Tool            string `json:"tool"`
			SubjectRef      string `json:"subject_ref"`
			Title           string `json:"title"`
			Start           string `json:"start"`
			DurationMinutes int    `json:"duration_minutes"`
			Reason          string `json:"reason"`
		}{a.Tool, a.SubjectRef, a.Title, a.Start, a.DurationMinutes, a.Reason})
	case "move_deal_stage":
		return json.Marshal(struct {
			Tool       string `json:"tool"`
			SubjectRef string `json:"subject_ref"`
			Stage      string `json:"stage"`
			Reason     string `json:"reason"`
		}{a.Tool, a.SubjectRef, a.Stage, a.Reason})
	default:
		return json.Marshal(struct {
			Tool       string `json:"tool"`
			SubjectRef string `json:"subject_ref"`
			Reason     string `json:"reason"`
		}{a.Tool, a.SubjectRef, a.Reason})
	}
}

// Summaries say that nothing has been done yet.
var Summaries = map[string]struct{ Some, None string }{
	"ru": {"Предлагаемые действия: %d. Ничего не выполнено, пока вы не подтвердите.", "Не удалось предложить действие по запросу."},
	"ky": {"Сунушталган аракеттер: %d. Сиз ырастамайынча эч нерсе аткарылбайт.", "Суроо боюнча аракет сунуштоо мүмкүн болгон жок."},
	"en": {"Proposed actions: %d. Nothing is done until you confirm.", "No action could be proposed for the request."},
}

// MaxHorizon is how far ahead a proposed due date may be.
const MaxHorizon = 365 * 24 * time.Hour

// Parse decodes an input document.
func Parse(raw json.RawMessage) (Input, error) {
	var in Input
	err := json.Unmarshal(raw, &in)
	return in, err
}

// Allows reports whether the CRM offered this tool to the user.
func (in Input) Allows(tool string) bool {
	for _, t := range in.Tools {
		if t == tool {
			return true
		}
	}
	return false
}

// Subject returns the subject with this ref.
func (in Input) Subject(ref string) (Subject, bool) {
	for _, s := range in.Subjects {
		if s.Ref == ref {
			return s, true
		}
	}
	return Subject{}, false
}

// Check returns why an action breaks the rules, or "".
func (in Input) Check(a Action) string {
	if !in.Allows(a.Tool) {
		return "uses a tool the user was not offered"
	}
	subject, ok := in.Subject(a.SubjectRef)
	if !ok {
		return "targets a record that is not in the input"
	}
	if a.Tool == "request_deal_brief" && subject.Kind != "deal" {
		return "requests a deal brief for a record that is not a deal"
	}
	if strings.TrimSpace(a.Reason) == "" {
		return "has no reason"
	}
	switch a.Tool {
	case "create_task":
		if a.DueDate != nil {
			return in.checkDate(*a.DueDate, time.DateOnly, "due date")
		}
	case "schedule_meeting":
		if strings.TrimSpace(a.Title) == "" {
			return "has no meeting title"
		}
		if a.DurationMinutes < MinMeetingMinutes || a.DurationMinutes > MaxMeetingMinutes {
			return fmt.Sprintf("has a meeting length outside %d..%d minutes", MinMeetingMinutes, MaxMeetingMinutes)
		}
		return in.checkDate(a.Start, "2006-01-02T15:04", "meeting start")
	case "move_deal_stage":
		if subject.Kind != "deal" {
			return "moves a record that is not a deal"
		}
		if a.Stage == subject.Stage {
			return "moves the deal to the stage it is already in"
		}
		if !contains(subject.Stages, a.Stage) {
			return "moves the deal to a stage that is not in its pipeline"
		}
	}
	return ""
}

// checkDate requires value (in layout) to fall between today and a year ahead.
func (in Input) checkDate(value, layout, what string) string {
	today, err1 := time.Parse(time.DateOnly, in.Today)
	at, err2 := time.Parse(layout, value)
	if err1 != nil || err2 != nil {
		return "has an invalid " + what
	}
	if at.Before(today) || at.Sub(today) > MaxHorizon {
		return fmt.Sprintf("has a %s outside %s..+1 year", what, in.Today)
	}
	return ""
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}
