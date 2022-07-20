package core

import (
	"github.com/DATA-DOG/go-sqlmock"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

type MockDatabase struct {
	Gorm *gorm.DB
	Mock sqlmock.Sqlmock
}

func NewMockDatabase() *MockDatabase {
	db, sqlMock, _ := sqlmock.New()
	sqlMock.ExpectQuery("SELECT VERSION()").
		WillReturnRows(sqlmock.NewRows([]string{"VERSION()"}).AddRow("5.7.33"))
	newDB, _ := gorm.Open(mysql.New(mysql.Config{
		Conn: db,
	}), nil)

	return &MockDatabase{
		Gorm: newDB,
		Mock: sqlMock,
	}
}
