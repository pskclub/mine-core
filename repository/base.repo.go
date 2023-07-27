package repository

import (
	"context"
	"database/sql"
	"errors"
	core "github.com/pskclub/mine-core"
	"github.com/pskclub/mine-core/errmsgs"
	"gorm.io/gorm/clause"

	"gorm.io/gorm"
)

type IRepository[M IModel] interface {
	FindAll(conds ...any) ([]M, core.IError)                                               // Function to find all records that match the given conditions
	FindOne(conds ...any) (*M, core.IError)                                                // Function to find the first record that matches the given conditions
	Count() (int64, core.IError)                                                           // Function to count the number of records
	Create(values any) core.IError                                                         // Function to insert a value into the database
	Updates(values any) core.IError                                                        // Function to update attributes with callbacks
	Delete(conds ...any) core.IError                                                       // Function to delete a value that matches the given conditions
	HardDelete(conds ...any) core.IError                                                   // Function to hard delete a value that matches the given conditions
	Pagination(pageOptions *core.PageOptions) (*Pagination[M], core.IError)                // Function to perform pagination on the records
	Save(values any) core.IError                                                           // Function to set values on a model
	Where(query any, args ...any) IRepository[M]                                           // Function to filter records based on a query
	Preload(query string, args ...any) IRepository[M]                                      // Function to preload associations
	Unscoped() IRepository[M]                                                              // Function to apply an unscoped query
	Exec(sql string, values ...any) core.IError                                            // Function to execute raw SQL queries
	Group(name string) IRepository[M]                                                      // Function to group records
	Joins(query string, args ...any) IRepository[M]                                        // Function to perform joins
	Order(value any) IRepository[M]                                                        // Function to order the records
	Distinct(args ...any) IRepository[M]                                                   // Function to specify distinct fields for querying
	Update(column string, value any) IRepository[M]                                        // Function to update a column with a value
	Select(query any, args ...any) IRepository[M]                                          // Function to select specific columns
	Omit(columns ...string) IRepository[M]                                                 // Function to omit specific columns
	Limit(limit int) IRepository[M]                                                        // Function to limit the number of records
	Offset(offset int) IRepository[M]                                                      // Function to specify the offset of records
	Association(column string) core.IError                                                 // Function to retrieve an association
	FindInBatches(dest any, batchSize int, fc func(tx *gorm.DB, batch int) error) *gorm.DB // Function to find records in batches
	FindOneOrInit(dest any, conds ...any) core.IError                                      // Function to find the first record that matches the given conditions or initialize a new one
	FindOneOrCreate(dest any, conds ...any) core.IError                                    // Function to find the first record that matches the given conditions or create a new one
	Attrs(attrs ...any) IRepository[M]                                                     // Function to set attributes on a model
	Assign(attrs ...any) IRepository[M]                                                    // Function to assign attributes to a model
	Pluck(column string, desc any) core.IError                                             // Function to retrieve a specific column value
	Scan(dest any) core.IError                                                             // Function to scan query results into a destination
	Row() *sql.Row                                                                         // Function to retrieve a single row
	Rows() (*sql.Rows, error)                                                              // Function to retrieve multiple rows
	Raw(dest any, sql string, values ...any) core.IError                                   // Function to execute a raw SQL query
	Clauses(conds ...clause.Expression) IRepository[M]                                     // Function to apply additional query clauses
	WithContext(ctx context.Context) IRepository[M]                                        // Function to set the context used for future queries
	NewSession() IRepository[M]                                                            // Function to create a new session for this query
}

type BaseRepository[M IModel] struct {
	ctx core.IContext
	db  *gorm.DB
}

func New[M IModel](ctx core.IContext) IRepository[M] {
	item := new(M)
	return &BaseRepository[M]{ctx: ctx, db: ctx.DB().Model(item)}
}

func NewWithDB[M IModel](ctx core.IContext, db *gorm.DB) IRepository[M] {
	item := new(M)
	newDB := db
	if newDB == nil {
		newDB = ctx.DB()
	}
	return &BaseRepository[M]{ctx: ctx, db: newDB.Model(item)}
}

// FindAll find records that match given conditions
func (m *BaseRepository[M]) FindAll(conds ...any) ([]M, core.IError) {
	list := make([]M, 0)
	err := m.getDBInstance().Find(&list, conds...).Error
	if err != nil {
		return nil, m.ctx.NewError(err, errmsgs.DBError)
	}

	return list, nil
}

// FindOne find first record that match given conditions, order by primary key
func (m *BaseRepository[M]) FindOne(conds ...any) (*M, core.IError) {
	item := new(M)
	err := m.getDBInstance().First(item, conds...).Error
	if errors.Is(gorm.ErrRecordNotFound, err) {
		return nil, m.ctx.NewError(err, errmsgs.NotFound)
	}

	if err != nil {
		return nil, m.ctx.NewError(err, errmsgs.DBError)
	}

	return item, nil
}

func (m *BaseRepository[M]) Count() (int64, core.IError) {
	var count int64
	err := m.getDBInstance().Count(&count).Error
	if err != nil {
		return 0, m.ctx.NewError(err, errmsgs.DBError)
	}

	return count, nil
}

// Create insert the value into database
func (m *BaseRepository[M]) Create(values any) core.IError {
	err := m.getDBInstance().Create(values).Error
	if errors.Is(err, gorm.ErrEmptySlice) {
		return nil
	}
	if err != nil {
		return m.ctx.NewError(err, errmsgs.DBError)
	}

	return nil
}

// Update update attributes with callbacks, refer: https://gorm.io/docs/update.html#Update-Changed-Fields
func (m *BaseRepository[M]) Updates(values any) core.IError {
	var err error
	err = m.getDBInstance().Updates(values).Error
	if errors.Is(err, gorm.ErrEmptySlice) {
		return nil
	}
	if err != nil {
		return m.ctx.NewError(err, errmsgs.DBError)
	}

	return nil
}

// Delete value match given conditions, if the value has primary key, then will including the primary key as condition
func (m *BaseRepository[M]) Delete(conds ...any) core.IError {
	item := new(M)
	err := m.getDBInstance().Delete(item, conds...).Error
	if err != nil {
		return m.ctx.NewError(err, errmsgs.DBError)
	}

	return nil
}

// HardDelete delete value match given conditions, if the value has primary key, then will including the primary key as condition
func (m *BaseRepository[M]) HardDelete(conds ...any) core.IError {
	item := new(M)
	err := m.getDBInstance().Unscoped().Delete(item, conds...).Error
	if err != nil {
		return m.ctx.NewError(err, errmsgs.DBError)
	}

	return nil
}

func (m *BaseRepository[M]) Pagination(pageOptions *core.PageOptions) (*Pagination[M], core.IError) {
	list := make([]M, 0)
	pageRes, err := core.Paginate(m.getDBInstance(), &list, pageOptions)
	if err != nil {
		return nil, m.ctx.NewError(err, errmsgs.DBError)
	}

	return &Pagination[M]{
		Limit: pageRes.Limit,
		Page:  pageRes.Page,
		Total: pageRes.Total,
		Count: pageRes.Count,
		Items: list,
	}, nil
}

func (m *BaseRepository[M]) Save(values any) core.IError {
	model := new(M)
	err := m.getDBInstance().Model(model).Save(values).Error
	if errors.Is(err, gorm.ErrEmptySlice) {
		return nil
	}
	if err != nil {
		return m.ctx.NewError(err, errmsgs.DBError)
	}

	return nil
}

func (m *BaseRepository[M]) getDBInstance() *gorm.DB {
	return m.db
}

func (m *BaseRepository[M]) NewSession() IRepository[M] {
	m.db = m.db.Session(&gorm.Session{NewDB: true}).Model(new(M))
	return m
}

func (m *BaseRepository[M]) Where(query any, args ...any) IRepository[M] {
	m.db = m.db.Where(query, args...)
	return m
}

func (m *BaseRepository[M]) Preload(query string, args ...any) IRepository[M] {
	m.db = m.db.Preload(query, args...)
	return m
}

func (m *BaseRepository[M]) Unscoped() IRepository[M] {
	m.db = m.db.Unscoped()
	return m
}

// Exec execute raw sql
func (m *BaseRepository[M]) Exec(sql string, values ...any) core.IError {
	err := m.db.Exec(sql, values...).Error
	if err != nil {
		return m.ctx.NewError(err, errmsgs.DBError)
	}

	return nil
}

func (m *BaseRepository[M]) Group(name string) IRepository[M] {
	m.db = m.db.Group(name)
	return m
}

func (m *BaseRepository[M]) Joins(query string, args ...any) IRepository[M] {
	m.db = m.db.Joins(query, args...)
	return m
}

func (m *BaseRepository[M]) Order(value any) IRepository[M] {
	m.db = m.db.Order(value)
	return m
}

// Distinct specify distinct fields that you want querying
func (m *BaseRepository[M]) Distinct(args ...any) IRepository[M] {
	m.db = m.db.Distinct(args...)
	return m
}

func (m *BaseRepository[M]) Update(column string, value any) IRepository[M] {
	m.db = m.db.Update(column, value)
	return m
}

func (m *BaseRepository[M]) Select(query any, args ...any) IRepository[M] {
	m.db = m.db.Select(query, args...)
	return m
}

func (m *BaseRepository[M]) Omit(columns ...string) IRepository[M] {
	m.db = m.db.Omit(columns...)
	return m
}

func (m *BaseRepository[M]) Limit(limit int) IRepository[M] {
	m.db = m.db.Limit(limit)
	return m
}

func (m *BaseRepository[M]) Offset(offset int) IRepository[M] {
	m.db = m.db.Offset(offset)
	return m
}

func (m *BaseRepository[M]) Association(column string) core.IError {
	err := m.db.Association(column).Error
	if err != nil {
		return m.ctx.NewError(err, errmsgs.DBError)
	}

	return nil
}

func (m *BaseRepository[M]) Attrs(attrs ...any) IRepository[M] {
	m.db = m.db.Attrs(attrs...)
	return m
}

func (m *BaseRepository[M]) Assign(attrs ...any) IRepository[M] {
	m.db = m.db.Assign(attrs...)
	return m
}

func (m *BaseRepository[M]) Pluck(column string, desc any) core.IError {
	err := m.db.Pluck(column, desc).Error
	if err != nil {
		return m.ctx.NewError(err, errmsgs.DBError)
	}

	return nil
}

func (m *BaseRepository[M]) Scan(dest any) core.IError {
	err := m.db.Scan(dest).Error
	if err != nil {
		return m.ctx.NewError(err, errmsgs.DBError)
	}

	return nil
}

func (m *BaseRepository[M]) Row() *sql.Row {
	return m.db.Row()
}

func (m *BaseRepository[M]) Rows() (*sql.Rows, error) {
	return m.db.Rows()
}
func (m *BaseRepository[M]) Raw(dest any, sql string, values ...any) core.IError {
	err := m.db.Raw(sql, values...).Scan(dest).Error
	if errors.Is(gorm.ErrRecordNotFound, err) {
		return m.ctx.NewError(err, errmsgs.NotFound)
	}

	if err != nil {
		return m.ctx.NewError(err, errmsgs.DBError)
	}

	return nil
}

func (m *BaseRepository[M]) Clauses(conds ...clause.Expression) IRepository[M] {
	m.db = m.db.Clauses(conds...)
	return m
}

func (m *BaseRepository[M]) FindInBatches(dest any, batchSize int, fc func(tx *gorm.DB, batch int) error) *gorm.DB {
	return m.db.FindInBatches(dest, batchSize, fc)
}

func (m *BaseRepository[M]) FindOneOrInit(dest interface{}, conds ...any) core.IError {
	err := m.db.FirstOrInit(dest, conds...).Error
	if errors.Is(gorm.ErrRecordNotFound, err) {
		return m.ctx.NewError(err, errmsgs.NotFound)
	}

	if err != nil {
		return m.ctx.NewError(err, errmsgs.DBError)
	}

	return nil
}

func (m *BaseRepository[M]) FindOneOrCreate(dest any, conds ...any) core.IError {
	err := m.db.FirstOrCreate(dest, conds...).Error
	if errors.Is(gorm.ErrRecordNotFound, err) {
		return m.ctx.NewError(err, errmsgs.NotFound)
	}

	if err != nil {
		return m.ctx.NewError(err, errmsgs.DBError)
	}

	return nil
}
func (m *BaseRepository[M]) WithContext(ctx context.Context) IRepository[M] {
	m.db = m.db.WithContext(ctx)

	return m
}
