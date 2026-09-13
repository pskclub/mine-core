package main

import (
	"testing"
)

// enumOf finds a request property's enum in a generated document, so a test can
// say what it means without walking the shape by hand every time.
func enumOf(t *testing.T, doc *oasDocument, path, method, property string) []any {
	t.Helper()
	return propertyOf(t, doc, path, method, property).Enum
}

// propertyOf is the whole published schema for a request property, for the
// cases that turn on a constraint other than the enum.
func propertyOf(t *testing.T, doc *oasDocument, path, method, property string) *oasSchema {
	t.Helper()

	item, ok := doc.Paths[path]
	if !ok {
		t.Fatalf("no path %s in %v", path, keysOfPaths(doc))
	}
	var op *oasOperation
	switch method {
	case "POST":
		op = item.Post
	case "GET":
		op = item.Get
	}
	if op == nil || op.RequestBody == nil {
		t.Fatalf("%s %s has no request body", method, path)
	}
	media, ok := op.RequestBody.Content["application/json"]
	if !ok || media.Schema == nil {
		t.Fatalf("%s %s has no JSON schema", method, path)
	}
	prop, ok := media.Schema.Properties[property]
	if !ok {
		t.Fatalf("%s %s has no property %q", method, path, property)
	}
	return prop
}

func keysOfPaths(doc *oasDocument) []string {
	out := make([]string, 0, len(doc.Paths))
	for k := range doc.Paths {
		out = append(out, k)
	}
	return out
}

// documentFrom builds the OpenAPI document for a throwaway repository, so one
// test can state a whole validator and read back what was published for it.
func documentFrom(t *testing.T, files map[string]string) *oasDocument {
	t.Helper()

	root := t.TempDir()
	for rel, src := range files {
		writeFixtureFile(t, root, rel, src)
	}

	cfg := testConfig()
	repo := &repoInfo{root: root, moduleName: "example.com/tmp"}
	loaded, err := loadGoFiles(repo.root, cfg)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	res := newResolver(repo, loaded, cfg)
	routes := buildRoutes(repo, loaded, buildControllerMethodRegistry(repo, loaded, cfg), cfg)
	doc, err := buildOpenAPI(routes, res, cfg)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	return doc
}

// routeFile is the registration every case in this file shares.
const routeFile = `package api

import "example.com/tmp/requests"

type Handler struct{}

func (Handler) Create(c Context) error {
	input := &requests.Create{}
	if err := c.BindWithValidate(input); err != nil {
		return err
	}
	return nil
}

type Context interface {
	BindWithValidate(any) error
}

type Server struct{}

func (s *Server) POST(path string, h any, m ...any) any { return nil }

func NewHTTP(e *Server) {
	c := &Handler{}
	e.POST("/things", c.Create)
}
`

// validatorStub is the shape the generator reads, standing in for core/valid.
const validatorStub = `package requests

type Validator struct{}

func (v *Validator) Str(name string, value *string) *StringField { return nil }
func (v *Validator) Int(name string, value *int) *IntField       { return nil }

type StringField struct{}

func (f *StringField) Required() *StringField            { return f }
func (f *StringField) In(options ...string) *StringField { return f }
func (f *StringField) Prefix(p string) *StringField      { return f }

type IntField struct{}

func (f *IntField) Required() *IntField          { return f }
func (f *IntField) In(options ...int) *IntField  { return f }
`

// TestEnumFromConstants is the point of resolving rule arguments: a service
// names its allowed values once and refers to them, because the handler
// branches on the same constants and a literal repeated in the validator is a
// literal that drifts. Reading only literals published no enum at all for those
// fields — and the page still looked complete, which is why it went unnoticed.
func TestEnumFromConstants(t *testing.T) {
	doc := documentFrom(t, map[string]string{
		"api/thing.module.go": routeFile,
		"requests/valid.go":   validatorStub,
		"consts/consts.go": `package consts

type Status string

const (
	StatusActive   = "ACTIVE"
	StatusInactive = Status("INACTIVE")
)

// AllowedKinds is a var rather than a const, which is how a set of values is as
// often written.
var KindPrimary = "PRIMARY"
`,
		"requests/create.request.go": `package requests

import "example.com/tmp/consts"

const roleAdmin = "ADMIN"

type Create struct {
	Role   *string ` + "`json:\"role\"`" + `
	Status *string ` + "`json:\"status\"`" + `
	Kind   *string ` + "`json:\"kind\"`" + `
}

func (r *Create) Valid(v *Validator) {
	v.Str("role", r.Role).Required().In(roleAdmin, "MEMBER")
	v.Str("status", r.Status).In(consts.StatusActive, string(consts.StatusInactive))
	v.Str("kind", r.Kind).In(consts.KindPrimary)
}
`,
	})

	for _, tc := range []struct {
		property string
		want     []any
		why      string
	}{
		{"role", []any{"ADMIN", "MEMBER"}, "a constant beside the request, mixed with a literal"},
		{"status", []any{"ACTIVE", "INACTIVE"}, "an imported constant, and one written as a conversion"},
		{"kind", []any{"PRIMARY"}, "a package-level var"},
	} {
		got := enumOf(t, doc, "/things", "POST", tc.property)
		if len(got) != len(tc.want) {
			t.Errorf("%s: enum = %v, want %v (%s)", tc.property, got, tc.want, tc.why)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("%s: enum = %v, want %v (%s)", tc.property, got, tc.want, tc.why)
				break
			}
		}
	}
}

// TestEnumFromNumericConstants covers the other kind of enum. It used to read
// only an integer literal, so a numeric status written as a named constant
// documented no allowed values either.
func TestEnumFromNumericConstants(t *testing.T) {
	doc := documentFrom(t, map[string]string{
		"api/thing.module.go": routeFile,
		"requests/valid.go":   validatorStub,
		"requests/create.request.go": `package requests

const (
	priorityLow  = 1
	priorityHigh = 9
)

type Create struct {
	Priority *int ` + "`json:\"priority\"`" + `
}

func (r *Create) Valid(v *Validator) {
	v.Int("priority", r.Priority).Required().In(priorityLow, priorityHigh)
}
`,
	})

	got := enumOf(t, doc, "/things", "POST", "priority")
	if len(got) != 2 || got[0] != 1 || got[1] != 9 {
		t.Errorf("enum = %v, want [1 9]", got)
	}
}

// TestEnumSkipsWhatItCannotRead keeps the failure honest. An argument whose
// value is not knowable from the source — a function call — is left out rather
// than published as an empty string, which would document a value the server
// rejects. The options beside it still make it through: some of the allowed
// values beats none.
func TestEnumSkipsWhatItCannotRead(t *testing.T) {
	doc := documentFrom(t, map[string]string{
		"api/thing.module.go": routeFile,
		"requests/valid.go":   validatorStub,
		"requests/create.request.go": `package requests

func computed() string { return "X" }

type Create struct {
	Role *string ` + "`json:\"role\"`" + `
}

func (r *Create) Valid(v *Validator) {
	v.Str("role", r.Role).In("ADMIN", computed())
}
`,
	})

	got := enumOf(t, doc, "/things", "POST", "role")
	if len(got) != 1 || got[0] != "ADMIN" {
		t.Errorf("enum = %v, want only the value that could be read", got)
	}
}

// TestRuleWithNoArgumentsIsNotFatal covers the guard on reading argument zero.
// A validator mid-edit — In() with the options not typed yet — must not take
// the generator down with an index panic: a tool that crashes on half-written
// source is a tool nobody runs while writing.
func TestRuleWithNoArgumentsIsNotFatal(t *testing.T) {
	doc := documentFrom(t, map[string]string{
		"api/thing.module.go": routeFile,
		"requests/valid.go":   validatorStub,
		"requests/create.request.go": `package requests

type Create struct {
	Role *string ` + "`json:\"role\"`" + `
	Slug *string ` + "`json:\"slug\"`" + `
}

func (r *Create) Valid(v *Validator) {
	v.Str("role", r.Role).In()
	v.Str("slug", r.Slug).Prefix()
}
`,
	})

	if enum := enumOf(t, doc, "/things", "POST", "role"); len(enum) != 0 {
		t.Errorf("enum = %v, want none: the rule states no options", enum)
	}
}

// wantEnum compares an enum against what the validator states, in order: the
// order the options are written in is the order a reader sees them, and the
// first one is also the sample value the request is generated with.
func wantEnum(t *testing.T, got []any, want []any, why string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("enum = %v, want %v (%s)", got, want, why)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("enum = %v, want %v (%s)", got, want, why)
		}
	}
}

// chainedStub adds to validatorStub the pieces the fully chained style needs:
// a constructor, a terminator, and the entry methods hanging off a field so one
// chain can state more than one field.
const chainedStub = `package requests

type Validator struct{}

func New(ctx any) *Validator { return nil }

func (v *Validator) Str(name string, value *string) *StringField { return nil }
func (v *Validator) Int(name string, value *int) *IntField       { return nil }
func (v *Validator) Valid() error                                { return nil }

type StringField struct{}

func (f *StringField) Required() *StringField                  { return f }
func (f *StringField) Trim() *StringField                      { return f }
func (f *StringField) Lower() *StringField                     { return f }
func (f *StringField) Max(n int) *StringField                  { return f }
func (f *StringField) In(options ...string) *StringField       { return f }
func (f *StringField) Str(n string, v *string) *StringField    { return f }
func (f *StringField) Int(n string, v *int) *IntField          { return nil }
func (f *StringField) Valid() error                            { return nil }

type IntField struct{}

func (f *IntField) Required() *IntField                     { return f }
func (f *IntField) Min(n int) *IntField                     { return f }
func (f *IntField) In(options ...int) *IntField             { return f }
func (f *IntField) Str(n string, v *string) *StringField    { return nil }
func (f *IntField) Valid() error                            { return nil }
`

// TestRulesFromChainedValidator covers the form the framework's own docs show —
// one return statement holding one chain that states every field. Read as a
// statement ending at the first entry it met, that method published nothing at
// all: not the enum, not the bounds, not even which fields were required. Every
// request written the documented way documented no rules, and the page still
// looked complete, which is what kept it quiet.
func TestRulesFromChainedValidator(t *testing.T) {
	doc := documentFrom(t, map[string]string{
		"api/thing.module.go": routeFile,
		"requests/valid.go":   chainedStub,
		"requests/create.request.go": `package requests

type Create struct {
	Role  *string ` + "`json:\"role\"`" + `
	Score *int    ` + "`json:\"score\"`" + `
	Note  *string ` + "`json:\"note\"`" + `
}

func (r *Create) Valid(ctx any) error {
	return New(ctx).
		Str("role", r.Role).Required().In("ADMIN", "MEMBER").
		Int("score", r.Score).Required().In(1, 9).
		Str("note", r.Note).Trim().Lower().Max(500).
		Valid()
}
`,
	})

	wantEnum(t, enumOf(t, doc, "/things", "POST", "role"), []any{"ADMIN", "MEMBER"}, "the first field of the chain")
	wantEnum(t, enumOf(t, doc, "/things", "POST", "score"), []any{1, 9}, "a numeric field mid-chain")

	note := propertyOf(t, doc, "/things", "POST", "note")
	if note.MaxLength == nil || *note.MaxLength != 500 {
		t.Errorf("note maxLength = %v, want 500: the last field of the chain states it", note.MaxLength)
	}
}

// TestRulesFromMultiFieldStatement is the same defect in the statement form: a
// chain does not stop stating fields because it was written as a statement.
func TestRulesFromMultiFieldStatement(t *testing.T) {
	doc := documentFrom(t, map[string]string{
		"api/thing.module.go": routeFile,
		"requests/valid.go":   chainedStub,
		"requests/create.request.go": `package requests

type Create struct {
	Role *string ` + "`json:\"role\"`" + `
	Note *string ` + "`json:\"note\"`" + `
}

func (r *Create) Valid(ctx any) error {
	v := New(ctx)
	v.Str("role", r.Role).Required().In("ADMIN", "MEMBER").
		Str("note", r.Note).In("A", "B")
	return v.Valid()
}
`,
	})

	wantEnum(t, enumOf(t, doc, "/things", "POST", "role"), []any{"ADMIN", "MEMBER"}, "the field the chain opened with")
	wantEnum(t, enumOf(t, doc, "/things", "POST", "note"), []any{"A", "B"}, "the field it continued with")
}

// TestRulesResolveInTheirOwnFile pins where a rule's arguments are looked up.
// Rules resolved through the struct declaration's imports, so a request whose
// Valid method lives in a sibling file — which is how a long validator is kept
// out of the type declaration — resolved every imported constant to nothing.
func TestRulesResolveInTheirOwnFile(t *testing.T) {
	doc := documentFrom(t, map[string]string{
		"api/thing.module.go": routeFile,
		"requests/valid.go":   validatorStub,
		"service/search.go": `package service

const (
	SearchPartial = "partial"
	SearchExact   = "exact"
)
`,
		"requests/create.request.go": `package requests

type Create struct {
	Role *string ` + "`json:\"role\"`" + `
}
`,
		"requests/create.valid.go": `package requests

import "example.com/tmp/service"

func (r *Create) Valid(v *Validator) {
	v.Str("role", r.Role).In(service.SearchPartial, service.SearchExact)
}
`,
	})

	wantEnum(t, enumOf(t, doc, "/things", "POST", "role"), []any{"partial", "exact"}, "constants imported by the file the rules are in")
}

// TestEnumFromPackageNamedUnlikeItsDirectory covers the identifier a qualified
// constant is written under. It was guessed from the import path, so a
// `services/` directory declaring `package service` resolved nothing —
// silently, since an unreadable argument is skipped rather than reported.
func TestEnumFromPackageNamedUnlikeItsDirectory(t *testing.T) {
	doc := documentFrom(t, map[string]string{
		"api/thing.module.go": routeFile,
		"requests/valid.go":   validatorStub,
		"services/search.go": `package service

const (
	SearchPartial = "partial"
	SearchExact   = "exact"
)
`,
		"requests/create.request.go": `package requests

import "example.com/tmp/services"

type Create struct {
	Role *string ` + "`json:\"role\"`" + `
}

func (r *Create) Valid(v *Validator) {
	v.Str("role", r.Role).In(service.SearchPartial, service.SearchExact)
}
`,
	})

	wantEnum(t, enumOf(t, doc, "/things", "POST", "role"), []any{"partial", "exact"}, "the package's own name, not its directory's")
}

// TestEnumFromSpreadSlice covers the other way a set of allowed values is
// written: named once as a slice, ranged over elsewhere in the service, and
// spread into the rule. It states the same enum as listing the options.
func TestEnumFromSpreadSlice(t *testing.T) {
	doc := documentFrom(t, map[string]string{
		"api/thing.module.go": routeFile,
		"requests/valid.go":   validatorStub,
		"service/search.go": `package service

const SearchExact = "exact"

var SearchTypes = []string{"partial", SearchExact}
`,
		"requests/create.request.go": `package requests

import "example.com/tmp/service"

type Create struct {
	Role *string ` + "`json:\"role\"`" + `
}

func (r *Create) Valid(v *Validator) {
	v.Str("role", r.Role).In(service.SearchTypes...)
}
`,
	})

	wantEnum(t, enumOf(t, doc, "/things", "POST", "role"), []any{"partial", "exact"}, "the elements the slice holds, constants included")
}

// TestEnumFollowsConstantToConstant covers a name that stands for another name.
// Only a literal was read, so an option defined in terms of another constant
// dropped out of the enum while the options beside it stayed — publishing a
// shorter list of allowed values than the server accepts, which is worse than
// publishing none.
func TestEnumFollowsConstantToConstant(t *testing.T) {
	doc := documentFrom(t, map[string]string{
		"api/thing.module.go": routeFile,
		"requests/valid.go":   validatorStub,
		"service/search.go": `package service

const searchPartial = "partial"

const (
	SearchPartial = searchPartial
	SearchExact   = "exact"
)
`,
		"requests/create.request.go": `package requests

import "example.com/tmp/service"

type Create struct {
	Role *string ` + "`json:\"role\"`" + `
}

func (r *Create) Valid(v *Validator) {
	v.Str("role", r.Role).In(service.SearchPartial, service.SearchExact)
}
`,
	})

	wantEnum(t, enumOf(t, doc, "/things", "POST", "role"), []any{"partial", "exact"}, "a constant named in terms of another")
}

// TestEnumFromTypedConstantMethod covers how a typed constant reaches a rule
// that takes strings: it has to be widened at the call site, and String() is
// how a type that carries the method is widened.
func TestEnumFromTypedConstantMethod(t *testing.T) {
	doc := documentFrom(t, map[string]string{
		"api/thing.module.go": routeFile,
		"requests/valid.go":   validatorStub,
		"service/search.go": `package service

type SearchType string

const (
	SearchPartial SearchType = "partial"
	SearchExact   SearchType = "exact"
)

func (s SearchType) String() string { return string(s) }
`,
		"requests/create.request.go": `package requests

import "example.com/tmp/service"

type Create struct {
	Role *string ` + "`json:\"role\"`" + `
}

func (r *Create) Valid(v *Validator) {
	v.Str("role", r.Role).In(service.SearchPartial.String(), service.SearchExact.String())
}
`,
	})

	wantEnum(t, enumOf(t, doc, "/things", "POST", "role"), []any{"partial", "exact"}, "a typed constant widened by its String method")
}

// TestRulesFromPackageQualifiedChain is the shape a real request has: the chain
// opens on another package's constructor, so its innermost link is a call this
// knows nothing about. That link states no field and must not swallow the ones
// after it.
func TestRulesFromPackageQualifiedChain(t *testing.T) {
	doc := documentFrom(t, map[string]string{
		"api/thing.module.go": routeFile,
		"valid/valid.go": `package valid

type Validator struct{}

func New(ctx any) *Validator { return nil }

func (v *Validator) Str(name string, value *string) *StringField { return nil }
func (v *Validator) Valid() error                                { return nil }

type StringField struct{}

func (f *StringField) Trim() *StringField                    { return f }
func (f *StringField) Lower() *StringField                   { return f }
func (f *StringField) In(options ...string) *StringField     { return f }
func (f *StringField) Str(n string, v *string) *StringField  { return f }
func (f *StringField) Valid() error                          { return nil }
`,
		"service/search.go": `package service

const (
	SearchTypePartial = "partial"
	SearchTypePrefix  = "prefix"
	SearchTypeExact   = "exact"
)
`,
		"requests/create.request.go": `package requests

import (
	"example.com/tmp/service"
	"example.com/tmp/valid"
)

type Create struct {
	SearchType *string ` + "`json:\"search_type\"`" + `
}

func (r *Create) Valid(ctx any) error {
	v := valid.New(ctx)
	v.Str("search_type", r.SearchType).Trim().Lower().In(
		service.SearchTypePartial, service.SearchTypePrefix, service.SearchTypeExact,
	)
	return v.Valid()
}
`,
	})

	wantEnum(t, enumOf(t, doc, "/things", "POST", "search_type"),
		[]any{"partial", "prefix", "exact"}, "a chain opened on an imported constructor")
}
