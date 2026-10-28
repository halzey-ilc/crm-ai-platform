package contracts_test

import (
	"errors"
	"strings"
	"testing"

	"crmai/contracts"
)

func TestLoadCompilesEverySchema(t *testing.T) {
	s, err := contracts.Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"call_summary.input.v1", "call_summary.output.v1"} {
		if !s.Has(id) {
			t.Errorf("schema %s missing", id)
		}
	}
}

func TestValidateAcceptsAndRejects(t *testing.T) {
	s, _ := contracts.Load()
	if err := s.Validate("call_summary.input.v1", []byte(`{"transcript":"Добрый день.","language":"ru"}`)); err != nil {
		t.Fatalf("valid input rejected: %v", err)
	}
	cases := map[string]string{
		"missing language": `{"transcript":"x"}`,
		"bad language":     `{"transcript":"x","language":"de"}`,
		"unknown field":    `{"transcript":"x","language":"ru","tenant_id":"other"}`,
		"empty transcript": `{"transcript":"","language":"ru"}`,
		"not json":         `{`,
	}
	for name, doc := range cases {
		t.Run(name, func(t *testing.T) {
			err := s.Validate("call_summary.input.v1", []byte(doc))
			var ve *contracts.ValidationError
			if !errors.As(err, &ve) {
				t.Fatalf("want ValidationError, got %v", err)
			}
		})
	}
}

func TestValidationErrorNeverEchoesValues(t *testing.T) {
	s, _ := contracts.Load()
	secret := "PASSPORT-1234567"
	err := s.Validate("call_summary.input.v1", []byte(`{"transcript":"x","language":"`+secret+`"}`))
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("error must not contain the value: %v", err)
	}
}
