package valid

import (
	core "github.com/pskclub/mine-core/v2"
	"gorm.io/gorm"
)

// Scope adds conditions to a validation query. Compose several to express complex
// rules (tenant scoping, soft-delete filters, excluding the current row, …).
type Scope = func(*gorm.DB) *gorm.DB

// Except excludes a row from a Unique check — typically the record being updated,
// so its own value doesn't count as a duplicate:
//
//	v.Str("email", r.Email).Unique("users", "email", valid.Except("id", userID))
func Except(column string, value any) Scope {
	return func(db *gorm.DB) *gorm.DB { return db.Where(column+" != ?", value) }
}

// Cond adds an arbitrary WHERE condition (query string or map, like gorm.Where).
//
//	v.Str("email", r.Email).Unique("users", "email",
//	    valid.Cond("tenant_id = ?", tenantID),
//	    valid.Cond("deleted_at IS NULL"))
func Cond(query any, args ...any) Scope {
	return func(db *gorm.DB) *gorm.DB { return db.Where(query, args...) }
}

// countWith counts rows in table where column = value, after applying scopes.
func (f *StringField) countWith(table, column string, scopes []Scope) (int64, error) {
	q := f.v.ctx.DB().Table(table).Where(column+" = ?", *f.value)
	for _, s := range scopes {
		q = s(q)
	}
	var count int64
	err := q.Count(&count).Error
	return count, err
}

// Unique fails when the value already exists in table.column. Pass Scopes for
// complex conditions (Except, Cond, or any func(*gorm.DB) *gorm.DB). A query
// failure is surfaced as 500 (never silently passed).
func (f *StringField) Unique(table, column string, scopes ...Scope) *StringField {
	if f.skip() {
		return f
	}
	count, err := f.countWith(table, column, scopes)
	if err != nil {
		f.v.errs.dbErr = err
		return f.fail("VALIDATION_ERROR", nil)
	}
	if count > 0 {
		return f.fail("UNIQUE", nil)
	}
	return f
}

// Exists fails when the value is absent from table.column (with optional Scopes).
func (f *StringField) Exists(table, column string, scopes ...Scope) *StringField {
	if f.skip() {
		return f
	}
	count, err := f.countWith(table, column, scopes)
	if err != nil {
		f.v.errs.dbErr = err
		return f.fail("VALIDATION_ERROR", nil)
	}
	if count == 0 {
		return f.fail("NOT_EXISTS", nil)
	}
	return f
}

// MongoUnique fails when a document matching filter exists in collection.
func (f *StringField) MongoUnique(collection string, filter any) *StringField {
	if f.skip() {
		return f
	}
	n, err := f.v.ctx.DBMongo().Count(collection, filter)
	if err != nil {
		f.v.errs.dbErr = err
		return f.fail("VALIDATION_ERROR", nil)
	}
	if n > 0 {
		return f.fail("UNIQUE", nil)
	}
	return f
}

// MongoExists fails when no document matching filter exists in collection.
func (f *StringField) MongoExists(collection string, filter any) *StringField {
	if f.skip() {
		return f
	}
	n, err := f.v.ctx.DBMongo().Count(collection, filter)
	if err != nil {
		f.v.errs.dbErr = err
		return f.fail("VALIDATION_ERROR", nil)
	}
	if n == 0 {
		return f.fail("NOT_EXISTS", nil)
	}
	return f
}

// Check runs a custom predicate with access to the context (DB, cache, …) — the
// escape hatch for validation logic the built-in rules don't cover. Returning a
// non-nil error is treated as an infra failure (500, like a DB error); returning
// ok=false records a violation with code.
//
//	v.Str("coupon", r.Coupon).Check("COUPON_INVALID", func(ctx core.IContext, code string) (bool, error) {
//	    n, err := repository.New[Coupon](ctx).Where("code = ? AND used = false", code).Count()
//	    return n > 0, err
//	})
func (f *StringField) Check(code string, fn func(ctx core.IContext, value string) (bool, error)) *StringField {
	if f.skip() {
		return f
	}
	ok, err := fn(f.v.ctx, *f.value)
	if err != nil {
		f.v.errs.dbErr = err
		return f.fail("VALIDATION_ERROR", nil)
	}
	if !ok {
		return f.fail(code, nil)
	}
	return f
}

// CheckDB is a Validator-level custom check for a field not tied to a string
// value (cross-field / composite DB rules). ok=false records code on field; a
// non-nil error is a 500.
func (v *Validator) CheckDB(field, code string, fn func(ctx core.IContext) (bool, error)) *Validator {
	ok, err := fn(v.ctx)
	if err != nil {
		v.errs.dbErr = err
		v.add(field, "VALIDATION_ERROR", nil)
		return v
	}
	if !ok {
		v.add(field, code, nil)
	}
	return v
}
