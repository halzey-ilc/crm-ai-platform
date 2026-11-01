package agentplan

import (
	"strings"
	"time"
)

// AlreadyProposed reports whether a assistant turn of history already proposed
// this action: the CRM writes those turns as "Proposed: <tool> <values>", so an
// action repeats one when its tool and its distinguishing value (start, due
// date, stage, title; none for request_deal_brief) both appear in one turn.
// A changed value ("better on Tuesday") is a new proposal and is kept.
func AlreadyProposed(history []Turn, a Action) bool {
	value := ""
	switch a.Tool {
	case "schedule_meeting":
		value = a.Start
	case "create_task":
		if a.DueDate != nil {
			value = *a.DueDate
		} else {
			value = a.Title
		}
	case "move_deal_stage":
		value = a.Stage
	case "log_note":
		value = a.Body
	}
	for _, t := range history {
		if t.Role == "assistant" && strings.Contains(t.Text, a.Tool) &&
			(value == "" || strings.Contains(strings.ToLower(t.Text), strings.ToLower(value))) {
			return true
		}
	}
	return false
}

// askedFor holds the words that must be in a request before a note or a deal brief
// is proposed. The model added both unasked ("Срок — до пятницы" got a brief and a
// note in 2 of 3 runs); the other tools carry values the request has to give anyway.
var askedFor = map[string][]string{
	"log_note":           {"заметк", "запиш", "записа", "зафиксир", "note", "record", "write down", "эскертме", "жазып", "жазыңыз", "жаз"},
	"request_deal_brief": {"бриф", "справк", "сводк", "саммари", "brief", "summary", "кыскача", "маалымдама"},
}

// Requested reports whether the latest request asks for a tool that is only
// proposed on demand (log_note, request_deal_brief); other tools always pass.
func Requested(tool, request string) bool {
	words, gated := askedFor[tool]
	if !gated {
		return true
	}
	lower := strings.ToLower(request)
	for _, w := range words {
		if strings.Contains(lower, w) {
			return true
		}
	}
	return false
}

// Relative day words (lowercase stems, so case endings match: "пятницу", "пятницы").
var dayOffsets = map[string]int{
	"сегодня": 0, "today": 0, "бүгүн": 0,
	"послезавтра": 2, "day after tomorrow": 2, "бүрсүгүнү": 2,
	"завтра": 1, "tomorrow": 1, "эртең": 1,
}

var weekdayStems = map[string]time.Weekday{
	"понедельник": time.Monday, "вторник": time.Tuesday, "сред": time.Wednesday, "четверг": time.Thursday,
	"пятниц": time.Friday, "суббот": time.Saturday, "воскресень": time.Sunday,
	"monday": time.Monday, "tuesday": time.Tuesday, "wednesday": time.Wednesday, "thursday": time.Thursday,
	"friday": time.Friday, "saturday": time.Saturday, "sunday": time.Sunday,
	"дүйшөмбү": time.Monday, "шейшемби": time.Tuesday, "шаршемби": time.Wednesday, "бейшемби": time.Thursday,
	"жума күн": time.Friday, "ишемби": time.Saturday, "жекшемби": time.Sunday,
}

// DateHints resolves the day words of a request to dates, so the model copies a
// date instead of counting weekdays (a 4B model turned "до пятницы" into a
// Saturday in most runs). A weekday means its next occurrence after today.
// Returns word -> "YYYY-MM-DD Weekday"; empty when today is unreadable.
func DateHints(today, text string) map[string]string {
	t, err := time.Parse("2006-01-02", today)
	if err != nil {
		return nil
	}
	lower := strings.ToLower(text)
	out := map[string]string{}
	day := func(d time.Time) string { return d.Format("2006-01-02") + " " + d.Weekday().String() }
	// "послезавтра" contains "завтра": match the longer words first and blank them out.
	for _, w := range []string{"послезавтра", "day after tomorrow", "бүрсүгүнү", "сегодня", "today", "бүгүн", "завтра", "tomorrow", "эртең"} {
		if strings.Contains(lower, w) {
			out[w] = day(t.AddDate(0, 0, dayOffsets[w]))
			lower = strings.ReplaceAll(lower, w, " ")
		}
	}
	for stem, wd := range weekdayStems {
		if strings.Contains(lower, stem) {
			ahead := (int(wd) - int(t.Weekday()) + 7) % 7
			if ahead == 0 {
				ahead = 7
			}
			out[stem] = day(t.AddDate(0, 0, ahead))
		}
	}
	return out
}
