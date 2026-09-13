package requests

import "example.com/fixture/consts"

// minPasswordLength is a constant so the fixture covers reading a rule argument
// that is named rather than written as a number.
const minPasswordLength = 8

// roleAdmin and roleMember cover the same-package case: an In rule written in
// terms of constants declared beside the request rather than imported.
const (
	roleAdmin  = "ADMIN"
	roleMember = "MEMBER"
)

type UserCreate struct {
	Email    *string `json:"email"`
	FullName *string `json:"full_name"`
	Password *string `json:"password"`
	Role     *string `json:"role"`
}

func (r *UserCreate) Valid(v *Validator) {
	v.Str("email", r.Email).Required().Email()
	v.Str("full_name", r.FullName).Required().Length(2, 100)
	v.Str("password", r.Password).Required().Length(minPasswordLength, 72)
	v.Str("role", r.Role).Required().In(roleAdmin, roleMember)
}

type UserSearch struct {
	Keyword *string `json:"-" query:"keyword"`
	Status  *string `json:"-" query:"status"`
}

func (r *UserSearch) Valid(v *Validator) {
	v.Str("keyword", r.Keyword).Length(2, 50)
	v.Str("status", r.Status).In(consts.StatusActive, string(consts.StatusInactive))
}

// UserImport is bound from a form, where a field may be named differently than
// it is in JSON — `note` states only the one name and is read under it either
// way.
type UserImport struct {
	Mode      *string `form:"mode" json:"import_mode"`
	Overwrite *bool   `form:"overwrite" json:"overwrite"`
	Note      *string `json:"note"`
}

func (r *UserImport) Valid(v *Validator) {
	v.Str("mode", r.Mode).Required().In("MERGE", "REPLACE")
}

// Validator stands in for the framework's validator: the generator only reads
// the shape of the calls, never their behaviour.
type Validator struct{}

func (v *Validator) Str(name string, value *string) *StringField { return nil }

type StringField struct{}

func (f *StringField) Required() *StringField            { return f }
func (f *StringField) Email() *StringField               { return f }
func (f *StringField) Length(min, max int) *StringField  { return f }
func (f *StringField) In(options ...string) *StringField { return f }
