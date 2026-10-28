package kpinum_test

import (
	"encoding/json"
	"reflect"
	"testing"

	"crmai/internal/kpinum"
)

const input = `{"language":"ru",
 "period":{"type":"month","start":"2026-10-01","end":"2026-11-01"},
 "previous_period":{"type":"month","start":"2026-09-01","end":"2026-10-01"},
 "metrics":[
  {"code":"revenue_won","name":"Выручка","unit":"money","current":1250000,"previous":1000000,"change":250000,"change_pct":25},
  {"code":"win_rate","name":"Процент побед","unit":"percent","current":32.47,"previous":40,"change":-7.53,"change_pct":-18.83},
  {"code":"deals_won","name":"Выиграно сделок","unit":"count","current":12,"previous":null,"change":null,"change_pct":null}]}`

func parse(t *testing.T) kpinum.Input {
	t.Helper()
	in, err := kpinum.Parse(json.RawMessage(input))
	if err != nil {
		t.Fatal(err)
	}
	return in
}

func TestNumbersInHandlesLocalFormats(t *testing.T) {
	got := kpinum.NumbersIn("Выручка 1 250 000 ₽ (+25%), процент побед 32,5% — минус 7.53 п.п.")
	want := []float64{1250000, 25, 32.5, 7.53}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestOnlyInputNumbersAreAllowed(t *testing.T) {
	a := parse(t).AllowedNumbers()
	for _, ok := range []string{
		"Выручка выросла до 1 250 000 с 1000000, рост 25%.",
		"Процент побед упал на 7,5 п.п. до 32,5% (−18.8%).", // rounding of input values
		"Октябрь 2026 против сентября.",                     // period dates
		"Выиграно 12 сделок.",
	} {
		if bad := a.Unknown(ok); bad != nil {
			t.Errorf("%q: unexpected unknown numbers %v", ok, bad)
		}
	}
	for _, invented := range []string{
		"Выручка выросла на 30%.",       // computed or invented, not in input
		"Средний чек составил 104 167.", // the model divided
		"Сделок выиграно на 3 больше.",  // there is no previous value
	} {
		if a.Unknown(invented) == nil {
			t.Errorf("%q must be rejected", invented)
		}
	}
}

func TestInsufficient(t *testing.T) {
	if parse(t).Insufficient() {
		t.Fatal("input with values is sufficient")
	}
	empty, _ := kpinum.Parse(json.RawMessage(`{"metrics":[{"code":"deals_won","current":0},{"code":"win_rate","current":null}]}`))
	if !empty.Insufficient() {
		t.Fatal("all zero or null current values are insufficient")
	}
}
