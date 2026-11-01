package agentplan

import "testing"

func TestDateHints(t *testing.T) {
	// 2026-10-31 is a Saturday.
	cases := map[string]map[string]string{
		"Срок — до пятницы":            {"пятниц": "2026-11-06 Friday"},
		"Лучше во вторник":             {"вторник": "2026-11-03 Tuesday"},
		"перезвонить послезавтра":      {"послезавтра": "2026-11-02 Monday"},
		"Remind me tomorrow":           {"tomorrow": "2026-11-01 Sunday"},
		"в субботу":                    {"суббот": "2026-11-07 Saturday"}, // today is Saturday: next week
		"Шейшемби күнү жолугушуу":      {"шейшемби": "2026-11-03 Tuesday"},
		"Без дат, просто заметка":      {},
		"завтра и в четверг, в среду?": {"завтра": "2026-11-01 Sunday", "четверг": "2026-11-05 Thursday", "сред": "2026-11-04 Wednesday"},
	}
	for text, want := range cases {
		got := DateHints("2026-10-31", text)
		if len(got) != len(want) {
			t.Fatalf("%q: got %v, want %v", text, got, want)
		}
		for k, v := range want {
			if got[k] != v {
				t.Fatalf("%q: %s = %q, want %q", text, k, got[k], v)
			}
		}
	}
	if DateHints("not a date", "завтра") != nil {
		t.Fatal("an unreadable today gives no hints")
	}
}

func TestAlreadyProposed(t *testing.T) {
	h := []Turn{
		{Role: "user", Text: "Move the deal to Negotiation and schedule a meeting"},
		{Role: "assistant", Text: "Proposed: move_deal_stage to Negotiation; schedule_meeting 2026-11-02T15:00, 60 min"},
	}
	for _, c := range []struct {
		a    Action
		want bool
	}{
		{Action{Tool: "move_deal_stage", Stage: "Negotiation"}, true},
		{Action{Tool: "move_deal_stage", Stage: "Closed"}, false},
		{Action{Tool: "schedule_meeting", Start: "2026-11-02T15:00"}, true},
		{Action{Tool: "schedule_meeting", Start: "2026-11-03T15:00"}, false}, // rescheduled
		{Action{Tool: "request_deal_brief"}, false},                          // never proposed
		{Action{Tool: "create_task", Title: "Call back"}, false},
	} {
		if got := AlreadyProposed(h, c.a); got != c.want {
			t.Errorf("%+v: got %v, want %v", c.a, got, c.want)
		}
	}
	for req, want := range map[string]bool{
		"Срок — до пятницы": false, "Запиши заметку": true, "Эскертме жазып кой": true,
		"Prepare a brief for this deal": true, "Подготовь справку по сделке": true,
	} {
		gotNote, gotBrief := Requested("log_note", req), Requested("request_deal_brief", req)
		if (gotNote || gotBrief) != want {
			t.Errorf("%q: note=%v brief=%v, want one of them %v", req, gotNote, gotBrief, want)
		}
	}
	if !Requested("schedule_meeting", "что угодно") {
		t.Error("tools with values are not gated")
	}
	// The user's own words never count as a proposal.
	if AlreadyProposed([]Turn{{Role: "user", Text: "request_deal_brief please"}}, Action{Tool: "request_deal_brief"}) {
		t.Error("only assistant turns are proposals")
	}
}
