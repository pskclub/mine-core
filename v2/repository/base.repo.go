// Package repository provides a generic, GORM-backed repository. The context is
// bound once at New (from core.IContext) so query methods do not take a ctx, and
// the fluent chain is copy-on-write so a base query can be branched safely.
//
// It wraps the full GORM query-building and finisher surface; for anything not
// wrapped here, DB() returns the underlying *gorm.DB bound to the context and the
// current query scope.
package repository

import (
	"database/sql"
	"errors"
	"strings"

	"context"

	core "github.com/pskclub/mine-core/v2"
	"github.com/pskclub/mine-core/v2/errmsgs"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Reader is the read-only subset of a repository (for consumer-side mocking).
type Reader[M core.IModel] interface {
	FindOne(conds ...any) (*M, core.IError)
	FindAll(conds ...any) ([]M, core.IError)
	Count() (int64, core.IError)
	Pagination(opts *core.PageOptions) (*core.Page[M], core.IError)
}

// Writer is the write subset of a repository (for consumer-side mocking).
type Writer[M core.IModel] interface {
	Create(m *M) core.IError
	Updates(values any) core.IError
	Delete(conds ...any) core.IError
}

// Repo is the generic repository.
type Repo[M core.IModel] struct {
	ctx context.Context
	db  *gorm.DB
}

// New builds a repository bound to ctx (context is taken from IContext, no need
// to pass it to each method).
func New[M core.IModel](ctx core.IContext) *Repo[M] {
	model := new(M)
	return &Repo[M]{ctx: ctx, db: ctx.DB().Model(model)}
}

// NewWithDB binds a specific *gorm.DB (transaction or named connection).
func NewWithDB[M core.IModel](ctx core.IContext, db *gorm.DB) *Repo[M] {
	model := new(M)
	if db == nil {
		db = ctx.DB()
	}
	return &Repo[M]{ctx: ctx, db: db.Model(model)}
}

func (r *Repo[M]) clone(db *gorm.DB) *Repo[M] {
	return &Repo[M]{ctx: r.ctx, db: db}
}

// branch returns an isolated session cloning the current query scope, so two
// branches off the same base repo never contaminate each other's conditions.
func (r *Repo[M]) branch() *gorm.DB {
	return r.db.Session(&gorm.Session{})
}

// session returns the query scope bound to the repository context (for finishers).
func (r *Repo[M]) session() *gorm.DB {
	return r.db.WithContext(r.ctx)
}

// DB returns the underlying *gorm.DB bound to the context and the current query
// scope — the escape hatch for any GORM feature not wrapped by this repository.
func (r *Repo[M]) DB() *gorm.DB { return r.session() }

// WithContext overrides the bound context (escape hatch for background work).
func (r *Repo[M]) WithContext(ctx context.Context) *Repo[M] {
	return &Repo[M]{ctx: ctx, db: r.db.WithContext(ctx)}
}

// ---------------------------------------------------------------------------
// Chainable (copy-on-write; never mutates the receiver)
// ---------------------------------------------------------------------------

func (r *Repo[M]) Where(query any, args ...any) *Repo[M] {
	return r.clone(r.branch().Where(query, args...))
}
func (r *Repo[M]) Or(query any, args ...any) *Repo[M] {
	return r.clone(r.branch().Or(query, args...))
}
func (r *Repo[M]) Not(query any, args ...any) *Repo[M] {
	return r.clone(r.branch().Not(query, args...))
}
func (r *Repo[M]) Preload(query string, args ...any) *Repo[M] {
	return r.clone(r.branch().Preload(query, args...))
}
func (r *Repo[M]) Joins(query string, args ...any) *Repo[M] {
	return r.clone(r.branch().Joins(query, args...))
}
func (r *Repo[M]) InnerJoins(query string, args ...any) *Repo[M] {
	return r.clone(r.branch().InnerJoins(query, args...))
}
func (r *Repo[M]) Order(value any) *Repo[M]   { return r.clone(r.branch().Order(value)) }
func (r *Repo[M]) Group(name string) *Repo[M] { return r.clone(r.branch().Group(name)) }
func (r *Repo[M]) Having(query any, args ...any) *Repo[M] {
	return r.clone(r.branch().Having(query, args...))
}
func (r *Repo[M]) Select(query any, args ...any) *Repo[M] {
	return r.clone(r.branch().Select(query, args...))
}
func (r *Repo[M]) Omit(cols ...string) *Repo[M]  { return r.clone(r.branch().Omit(cols...)) }
func (r *Repo[M]) Distinct(args ...any) *Repo[M] { return r.clone(r.branch().Distinct(args...)) }
func (r *Repo[M]) Limit(n int) *Repo[M]          { return r.clone(r.branch().Limit(n)) }
func (r *Repo[M]) Offset(n int) *Repo[M]         { return r.clone(r.branch().Offset(n)) }
func (r *Repo[M]) Unscoped() *Repo[M]            { return r.clone(r.branch().Unscoped()) }
func (r *Repo[M]) Table(name string, args ...any) *Repo[M] {
	return r.clone(r.branch().Table(name, args...))
}
func (r *Repo[M]) Attrs(attrs ...any) *Repo[M]  { return r.clone(r.branch().Attrs(attrs...)) }
func (r *Repo[M]) Assign(attrs ...any) *Repo[M] { return r.clone(r.branch().Assign(attrs...)) }
func (r *Repo[M]) Clauses(conds ...clause.Expression) *Repo[M] {
	return r.clone(r.branch().Clauses(conds...))
}

// Scopes composes reusable query functions (GORM scopes).
//
// They are applied here rather than handed to gorm's own Scopes, which keeps
// them on the statement and runs them inside Execute. That is too late for
// Pagination: Count reads and rewrites the SELECT and ORDER BY clauses before
// Execute, so a scope that orders or selects lands in the count query after
// Count has already decided what to do about it —
//
//	SELECT count(*) FROM notes ORDER BY pinned DESC
//
// which sqlite accepts and postgres rejects, i.e. a green test suite and a 500
// on every list route. Applied on the spot, a scope is indistinguishable from
// calling the builder directly, which is what copy-on-write already promises
// everywhere else on this type.
//
// Two consequences worth knowing. A scope's clauses now land where the call
// sits in the chain rather than after everything else, so mixing Order() on the
// chain with a scope that orders can change the sequence of the columns. And a
// scope reading Statement.Dest sees the value at build time — Statement.Model
// is unaffected, New sets it before any scope can run.
func (r *Repo[M]) Scopes(funcs ...func(*gorm.DB) *gorm.DB) *Repo[M] {
	db := r.branch()
	for _, fn := range funcs {
		if fn == nil {
			continue
		}

		db = fn(db)
	}

	return r.clone(db)
}

// ---------------------------------------------------------------------------
// Read finishers
// ---------------------------------------------------------------------------

// FindOne returns the first matching record ordered by primary key, or NOT_FOUND.
func (r *Repo[M]) FindOne(conds ...any) (*M, core.IError) {
	m := new(M)
	if err := r.session().First(m, conds...).Error; err != nil {
		return nil, mapErr(err)
	}
	return m, nil
}

// Take returns a single matching record without an implicit order, or NOT_FOUND.
func (r *Repo[M]) Take(conds ...any) (*M, core.IError) {
	m := new(M)
	if err := r.session().Take(m, conds...).Error; err != nil {
		return nil, mapErr(err)
	}
	return m, nil
}

// Last returns the last matching record ordered by primary key, or NOT_FOUND.
func (r *Repo[M]) Last(conds ...any) (*M, core.IError) {
	m := new(M)
	if err := r.session().Last(m, conds...).Error; err != nil {
		return nil, mapErr(err)
	}
	return m, nil
}

// FindAll returns all matching records.
func (r *Repo[M]) FindAll(conds ...any) ([]M, core.IError) {
	list := make([]M, 0)
	if err := r.session().Find(&list, conds...).Error; err != nil {
		return nil, mapErr(err)
	}
	return list, nil
}

// FindInBatches processes matching records in batches.
func (r *Repo[M]) FindInBatches(dest *[]M, batchSize int, fc func(tx *gorm.DB, batch int) error) core.IError {
	if err := r.session().FindInBatches(dest, batchSize, fc).Error; err != nil {
		return mapErr(err)
	}
	return nil
}

// Count returns the number of matching records.
func (r *Repo[M]) Count() (int64, core.IError) {
	var n int64
	if err := r.session().Count(&n).Error; err != nil {
		return 0, mapErr(err)
	}
	return n, nil
}

// Exists reports whether any record matches the current scope.
func (r *Repo[M]) Exists() (bool, core.IError) {
	n, err := r.Count()
	return n > 0, err
}

// Pluck reads a single column into dest (a pointer to a slice).
func (r *Repo[M]) Pluck(column string, dest any) core.IError {
	if err := r.session().Pluck(column, dest).Error; err != nil {
		return mapErr(err)
	}
	return nil
}

// Scan scans the query result into dest (e.g. a projection struct).
func (r *Repo[M]) Scan(dest any) core.IError {
	if err := r.session().Scan(dest).Error; err != nil {
		return mapErr(err)
	}
	return nil
}

// Row returns a single *sql.Row for the current scope.
func (r *Repo[M]) Row() *sql.Row { return r.session().Row() }

// Rows returns *sql.Rows for the current scope.
func (r *Repo[M]) Rows() (*sql.Rows, error) { return r.session().Rows() }

// Pagination returns a paginated result for the current query scope.
func (r *Repo[M]) Pagination(opts *core.PageOptions) (*core.Page[M], core.IError) {
	list := make([]M, 0)
	page, err := core.Paginate(r.session(), &list, opts)
	if err != nil {
		return nil, mapErr(err)
	}
	return page, nil
}

// ---------------------------------------------------------------------------
// Write finishers
// ---------------------------------------------------------------------------

// Create inserts m.
func (r *Repo[M]) Create(m *M) core.IError {
	if err := r.session().Create(m).Error; err != nil {
		return mapErr(err)
	}
	return nil
}

// CreateInBatches inserts values (a slice) in batches of batchSize.
func (r *Repo[M]) CreateInBatches(values any, batchSize int) core.IError {
	if err := r.session().CreateInBatches(values, batchSize).Error; err != nil {
		return mapErr(err)
	}
	return nil
}

// Save upserts the full record (all fields). Model is rebound to m so its
// primary key drives the update (the base repo is bound to an empty model).
func (r *Repo[M]) Save(m *M) core.IError {
	if err := r.session().Model(m).Save(m).Error; err != nil {
		return mapErr(err)
	}
	return nil
}

// Update sets a single column on the current scope.
func (r *Repo[M]) Update(column string, value any) core.IError {
	if err := r.session().Update(column, value).Error; err != nil {
		return mapErr(err)
	}
	return nil
}

// Updates applies non-zero updates (struct) or a map to the current scope.
func (r *Repo[M]) Updates(values any) core.IError {
	if err := r.session().Updates(values).Error; err != nil {
		return mapErr(err)
	}
	return nil
}

// Delete soft-deletes matching records.
func (r *Repo[M]) Delete(conds ...any) core.IError {
	if err := r.session().Delete(new(M), conds...).Error; err != nil {
		return mapErr(err)
	}
	return nil
}

// HardDelete permanently deletes matching records (ignores soft-delete).
func (r *Repo[M]) HardDelete(conds ...any) core.IError {
	if err := r.session().Unscoped().Delete(new(M), conds...).Error; err != nil {
		return mapErr(err)
	}
	return nil
}

// FindOneOrInit returns the first match, or initializes m (not persisted) from
// the conditions and any Attrs/Assign.
func (r *Repo[M]) FindOneOrInit(m *M, conds ...any) core.IError {
	if err := r.session().FirstOrInit(m, conds...).Error; err != nil {
		return mapErr(err)
	}
	return nil
}

// FindOneOrCreate returns the first match or creates m.
func (r *Repo[M]) FindOneOrCreate(m *M, conds ...any) core.IError {
	if err := r.session().FirstOrCreate(m, conds...).Error; err != nil {
		return mapErr(err)
	}
	return nil
}

// Association returns the GORM association handle for m.column (Append/Replace/
// Delete/Clear/Count).
func (r *Repo[M]) Association(m *M, column string) *gorm.Association {
	return r.session().Model(m).Association(column)
}

// ---------------------------------------------------------------------------
// Raw SQL & transactions
// ---------------------------------------------------------------------------

// Exec runs a raw statement (INSERT/UPDATE/DELETE/DDL).
func (r *Repo[M]) Exec(sql string, values ...any) core.IError {
	if err := r.session().Exec(sql, values...).Error; err != nil {
		return mapErr(err)
	}
	return nil
}

// Raw runs a raw query and scans it into dest.
func (r *Repo[M]) Raw(dest any, sql string, values ...any) core.IError {
	if err := r.session().Raw(sql, values...).Scan(dest).Error; err != nil {
		return mapErr(err)
	}
	return nil
}

// Transaction runs fn in a transaction bound to the repository context.
func (r *Repo[M]) Transaction(fn func(tx *gorm.DB) error) core.IError {
	if err := r.session().Transaction(func(g *gorm.DB) error { return fn(g) }); err != nil {
		return mapErr(err)
	}
	return nil
}

// mapErr converts GORM errors to framework errors without string matching on the
// common cases.
func mapErr(err error) core.IError {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, gorm.ErrRecordNotFound):
		return errmsgs.NotFound
	case strings.Contains(err.Error(), "invalid input syntax for type uuid"):
		// driver-specific fallback; still surfaces as not-found like v1
		return errmsgs.NotFound
	default:
		return core.Wrap(err, "repository").WithCode("DATABASE_ERROR")
	}
}
