// Package contracts embeds the versioned JSON Schemas that define job inputs
// and model outputs, and validates documents against them.
//
// Schema identifiers are "<name>.v<N>" (the file name without ".json"). A
// published version is immutable: an incompatible change ships as a new file
// with the next version number.
package contracts

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"path"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

//go:embed api/*.json model-schemas/*.json
var files embed.FS

const baseURL = "https://contracts.crmai.invalid/"

// Schemas is a set of compiled schemas keyed by schema id.
type Schemas struct {
	compiled map[string]*jsonschema.Schema
}

// Load compiles every embedded schema. It fails if any schema is invalid or
// two files declare the same id.
func Load() (*Schemas, error) {
	c := jsonschema.NewCompiler()
	ids := map[string]string{}
	err := fs.WalkDir(files, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".json") {
			return err
		}
		raw, err := files.ReadFile(p)
		if err != nil {
			return err
		}
		doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
		if err != nil {
			return fmt.Errorf("contracts: %s: %w", p, err)
		}
		id := strings.TrimSuffix(path.Base(p), ".json")
		if prev, dup := ids[id]; dup {
			return fmt.Errorf("contracts: schema id %q declared by %s and %s", id, prev, p)
		}
		ids[id] = p
		return c.AddResource(baseURL+p, doc)
	})
	if err != nil {
		return nil, err
	}
	s := &Schemas{compiled: make(map[string]*jsonschema.Schema, len(ids))}
	for id, p := range ids {
		sch, err := c.Compile(baseURL + p)
		if err != nil {
			return nil, fmt.Errorf("contracts: compile %s: %w", id, err)
		}
		s.compiled[id] = sch
	}
	return s, nil
}

// Has reports whether a schema id is known.
func (s *Schemas) Has(id string) bool {
	_, ok := s.compiled[id]
	return ok
}

// IDs returns the known schema ids.
func (s *Schemas) IDs() []string {
	out := make([]string, 0, len(s.compiled))
	for id := range s.compiled {
		out = append(out, id)
	}
	return out
}

// ValidationError reports that a document does not satisfy a schema. Its
// message describes the failing locations but never echoes document values.
type ValidationError struct {
	SchemaID string
	Problems []string
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("document does not match %s: %s", e.SchemaID, strings.Join(e.Problems, "; "))
}

// Validate checks raw JSON against the schema with the given id.
func (s *Schemas) Validate(id string, raw json.RawMessage) error {
	sch, ok := s.compiled[id]
	if !ok {
		return fmt.Errorf("contracts: unknown schema %q", id)
	}
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return &ValidationError{SchemaID: id, Problems: []string{"not valid JSON"}}
	}
	if err := sch.Validate(inst); err != nil {
		return &ValidationError{SchemaID: id, Problems: problems(err)}
	}
	return nil
}

// problems flattens a jsonschema error into "location: kind" strings. Only
// instance locations and the failing keyword are kept, so values never leak
// into logs or API errors.
func problems(err error) []string {
	ve, ok := err.(*jsonschema.ValidationError)
	if !ok {
		return []string{"invalid document"}
	}
	var out []string
	var walk func(e *jsonschema.ValidationError)
	walk = func(e *jsonschema.ValidationError) {
		if len(e.Causes) == 0 {
			loc := "/" + strings.Join(e.InstanceLocation, "/")
			kw := "constraint"
			if e.ErrorKind != nil {
				if k := e.ErrorKind.KeywordPath(); len(k) > 0 {
					kw = strings.Join(k, "/")
				}
			}
			out = append(out, loc+": "+kw)
			return
		}
		for _, c := range e.Causes {
			walk(c)
		}
	}
	walk(ve)
	if len(out) > 10 {
		out = append(out[:10], "...")
	}
	return out
}
