package core

import (
	"fmt"
	"github.com/sirupsen/logrus"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"reflect"
)

type Database struct {
	Name     string
	Host     string
	User     string
	Password string
	Port     string
}

func NewDatabase(env *ENVConfig) *Database {
	return &Database{
		Name:     env.DBName,
		Host:     env.DBHost,
		User:     env.DBUser,
		Password: env.DBPassword,
		Port:     env.DBPort,
	}
}

// ConnectDB to connect Database
func (db *Database) Connect() (*gorm.DB, error) {
	dsn := fmt.Sprintf("%v:%v@tcp(%v:%v)/%v?charset=utf8&parseTime=True&loc=Local&multiStatements=True&loc=UTC",
		db.User, db.Password, db.Host, db.Port, db.Name,
	)

	logLevel := logger.Silent
	if NewEnv().Config().LogLevel == logrus.DebugLevel {
		logLevel = logger.Info
	}

	if NewEnv().Config().LogLevel == logrus.ErrorLevel {
		logLevel = logger.Error
	}

	newDB, err := gorm.Open(mysql.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logLevel),
	})

	if err != nil {
		return nil, err
	}

	return newDB, nil
}

func Paginate(db *gorm.DB, model interface{}, options *PageOptions) (*PageResponse, error) {
	if options.Page == 0 {
		options.Page = 1
	}

	offset := (options.Page - 1) * options.Limit

	if len(options.OrderBy) > 0 {
		for _, o := range options.OrderBy {
			db = db.Order(o)
		}
	}

	err := db.Offset(int(offset)).Limit(int(options.Limit)).Find(model).Error
	if err != nil {
		return nil, err
	}

	var totalCount int64
	err = db.Model(model).Count(&totalCount).Error
	if err != nil {
		return nil, err
	}

	return &PageResponse{
		Total: totalCount,
		Limit: options.Limit,
		Count: int64(reflect.ValueOf(model).Elem().Len()),
		Page:  options.Page,
		Q:     options.Q,
	}, nil
}
