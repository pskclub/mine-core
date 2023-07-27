package repository

import (
	"context"
	"database/sql"
	core "github.com/pskclub/mine-core"
	"github.com/stretchr/testify/mock"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// MockIRepository is a mock of IRepository interface.
type MockRepository[M IModel] struct {
	mock.Mock
}

func NewMock[M IModel]() *MockRepository[M] {
	return &MockRepository[M]{}
}

func (m *MockRepository[M]) FindAll(conds ...interface{}) ([]M, core.IError) {
	args := m.Called(conds...)
	return args.Get(0).([]M), core.MockIError(args, 1)
}

func (m *MockRepository[M]) FindOne(conds ...interface{}) (*M, core.IError) {
	args := m.Called(conds...)
	return args.Get(0).(*M), core.MockIError(args, 1)
}

func (m *MockRepository[M]) Count() (int64, core.IError) {
	args := m.Called()
	return args.Get(0).(int64), core.MockIError(args, 1)
}

func (m *MockRepository[M]) Create(values interface{}) core.IError {
	args := m.Called(values)
	return core.MockIError(args, 0)
}

func (m *MockRepository[M]) Updates(values interface{}) core.IError {
	args := m.Called(values)
	return core.MockIError(args, 0)
}

func (m *MockRepository[M]) Delete(conds ...interface{}) core.IError {
	args := m.Called(conds...)
	return core.MockIError(args, 0)
}

func (m *MockRepository[M]) HardDelete(conds ...interface{}) core.IError {
	args := m.Called(conds...)
	return core.MockIError(args, 0)
}

func (m *MockRepository[M]) Pagination(pageOptions *core.PageOptions) (*Pagination[M], core.IError) {
	args := m.Called(pageOptions)
	return args.Get(0).(*Pagination[M]), core.MockIError(args, 1)
}

func (m *MockRepository[M]) Save(values interface{}) core.IError {
	args := m.Called(values)
	return core.MockIError(args, 0)
}

func (m *MockRepository[M]) Where(query interface{}, args ...interface{}) IRepository[M] {
	varargs := append([]interface{}{query}, args...)
	m.Called(varargs...)
	return m
}

func (m *MockRepository[M]) Preload(query string, args ...interface{}) IRepository[M] {
	varargs := []interface{}{query}
	for _, a := range args {
		varargs = append(varargs, a)
	}

	m.Called(varargs...)
	return m
}

func (m *MockRepository[M]) Unscoped() IRepository[M] {
	m.Called()
	return m
}

func (m *MockRepository[M]) Exec(sql string, values ...interface{}) core.IError {
	varargs := []interface{}{sql}
	for _, a := range values {
		varargs = append(varargs, a)
	}
	args := m.Called(varargs...)
	return core.MockIError(args, 0)
}

func (m *MockRepository[M]) Group(name string) IRepository[M] {
	m.Called(name)
	return m
}

func (m *MockRepository[M]) Joins(query string, args ...interface{}) IRepository[M] {
	varargs := []interface{}{query}
	for _, a := range args {
		varargs = append(varargs, a)
	}
	m.Called(varargs...)
	return m
}

func (m *MockRepository[M]) Order(value interface{}) IRepository[M] {
	m.Called(value)
	return m
}

func (m *MockRepository[M]) Distinct(args ...interface{}) IRepository[M] {
	m.Called(args...)
	return m
}

func (m *MockRepository[M]) Update(column string, value interface{}) IRepository[M] {
	m.Called(column, value)
	return m
}

func (m *MockRepository[M]) Select(query interface{}, args ...interface{}) IRepository[M] {
	varargs := []interface{}{query}
	for _, a := range args {
		varargs = append(varargs, a)
	}
	m.Called(varargs...)
	return m
}

func (m *MockRepository[M]) Omit(columns ...string) IRepository[M] {
	varargs := []interface{}{}
	for _, a := range columns {
		varargs = append(varargs, a)
	}

	m.Called(varargs...)
	return m
}

func (m *MockRepository[M]) Limit(limit int) IRepository[M] {
	m.Called(limit)
	return m
}

func (m *MockRepository[M]) Offset(offset int) IRepository[M] {
	m.Called(offset)
	return m
}

func (m *MockRepository[M]) Association(column string) core.IError {
	args := m.Called(column)
	return core.MockIError(args, 0)
}

func (m *MockRepository[M]) Attrs(attrs ...interface{}) IRepository[M] {
	m.Called(attrs...)
	return m
}

func (m *MockRepository[M]) Assign(attrs ...interface{}) IRepository[M] {
	m.Called(attrs...)
	return m
}

func (m *MockRepository[M]) Pluck(column string, desc interface{}) core.IError {
	args := m.Called(column, desc)
	return core.MockIError(args, 0)
}

func (m *MockRepository[M]) Scan(dest interface{}) core.IError {
	args := m.Called(dest)
	return core.MockIError(args, 0)
}

func (m *MockRepository[M]) Row() *sql.Row {
	args := m.Called()
	return args.Get(0).(*sql.Row)
}

func (m *MockRepository[M]) Rows() (*sql.Rows, error) {
	args := m.Called()
	return args.Get(0).(*sql.Rows), args.Error(1)
}

func (m *MockRepository[M]) Raw(dest interface{}, sql string, values ...interface{}) core.IError {
	varargs := []interface{}{dest, sql}
	for _, a := range values {
		varargs = append(varargs, a)
	}
	args := m.Called(varargs...)
	return core.MockIError(args, 0)
}

func (m *MockRepository[M]) Clauses(conds ...clause.Expression) IRepository[M] {
	varargs := []interface{}{}
	for _, a := range conds {
		varargs = append(varargs, a)
	}

	m.Called(varargs...)
	return m
}

func (m *MockRepository[M]) WithContext(ctx context.Context) IRepository[M] {
	m.Called(ctx)
	return m
}

func (m *MockRepository[M]) NewSession() IRepository[M] {
	m.Called()
	return m
}

func (m *MockRepository[M]) FindInBatches(dest interface{}, batchSize int, fc func(tx *gorm.DB, batch int) error) *gorm.DB {
	args := m.Called(dest, batchSize, fc)
	return args.Get(0).(*gorm.DB)
}

func (m *MockRepository[M]) FindOneOrInit(dest interface{}, conds ...interface{}) core.IError {
	varargs := []interface{}{dest}
	for _, a := range conds {
		varargs = append(varargs, a)
	}
	args := m.Called(varargs...)
	return core.MockIError(args, 0)
}

func (m *MockRepository[M]) FindOneOrCreate(dest interface{}, conds ...interface{}) core.IError {
	varargs := []interface{}{dest}
	for _, a := range conds {
		varargs = append(varargs, a)
	}
	args := m.Called(varargs...)
	return core.MockIError(args, 0)
}
