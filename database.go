package core

import (
	"errors"
	"fmt"
	"github.com/sirupsen/logrus"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlserver"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"net/http"
	"reflect"
)

type KeywordCondition string
type KeywordType string

const (
	MustMatch KeywordType = "must_match"
	Wildcard  KeywordType = "wildcard"

	And KeywordCondition = "and"
	Or  KeywordCondition = "or"
)

const (
	DatabaseDriverPOSTGRES = "postgres"
	DatabaseDriverMSSQL    = "mssql"
	DatabaseDriverMYSQL    = "mysql"
)

type KeywordConditionWrapper struct {
	Condition      KeywordCondition
	KeywordOptions []KeywordOptions
}

type KeywordOptions struct {
	Type  KeywordType
	Key   string
	Value string
}

type Database struct {
	Driver   string
	DSN      string
	Name     string
	Host     string
	User     string
	Password string
	Port     string
	config   *gorm.Config
}

func NewDatabase(env *ENVConfig) *Database {
	return &Database{
		Driver:   env.DBDriver,
		DSN:      env.DBDsn,
		Name:     env.DBName,
		Host:     env.DBHost,
		User:     env.DBUser,
		Password: env.DBPassword,
		Port:     env.DBPort,
		config:   &gorm.Config{},
	}
}

func NewDatabaseWithConfig(env *ENVConfig, config *gorm.Config) *Database {
	return &Database{
		Driver:   env.DBDriver,
		DSN:      env.DBDsn,
		Name:     env.DBName,
		Host:     env.DBHost,
		User:     env.DBUser,
		Password: env.DBPassword,
		Port:     env.DBPort,
		config:   config,
	}
}

// Connect to connect Database
func (db *Database) Connect() (*gorm.DB, error) {
	logLevel := logger.Silent
	if NewEnv().Config().LogLevel == logrus.DebugLevel {
		logLevel = logger.Info
	}

	if NewEnv().Config().LogLevel == logrus.ErrorLevel {
		logLevel = logger.Error
	}

	dsn := db.DSN
	var newDB *gorm.DB
	var err error

	db.config.Logger = logger.Default.LogMode(logLevel)

	switch db.Driver {
	case DatabaseDriverMSSQL:
		if len(dsn) == 0 {
			dsn = fmt.Sprintf("sqlserver://%v:%v@%v:%v?database=%v",
				db.User, db.Password, db.Host, db.Port, db.Name,
			)
		}
		newDB, err = gorm.Open(sqlserver.Open(dsn), db.config)
	case DatabaseDriverPOSTGRES:
		if len(dsn) == 0 {
			dsn = fmt.Sprintf("host=%s user=%s password=%s dbname=%s port=%s sslmode=disable TimeZone=utc",
				db.Host, db.User, db.Password, db.Name, db.Port)
		}
		newDB, err = gorm.Open(postgres.Open(dsn), db.config)
	default:
		if len(dsn) == 0 {
			dsn = fmt.Sprintf("%v:%v@tcp(%v:%v)/%v?charset=utf8&parseTime=True&loc=Local&multiStatements=True&loc=UTC",
				db.User, db.Password, db.Host, db.Port, db.Name,
			)
		}
		newDB, err = gorm.Open(mysql.Open(dsn), db.config)
	}

	if err != nil {
		return nil, err
	}

	return newDB, nil
}

func Paginate(db *gorm.DB, model interface{}, options *PageOptions) (*PageResponse, error) {
	if options.Page < 1 {
		options.Page = 1
	}

	offset := (options.Page - 1) * options.Limit

	if len(options.OrderBy) > 0 {
		for _, o := range options.OrderBy {
			db = db.Order(o)
		}
	}

	var totalCount int64
	if err := db.Model(model).Count(&totalCount).Error; err != nil {
		return nil, err
	}

	if err := db.Limit(int(options.Limit)).Offset(int(offset)).Find(model).Error; err != nil {
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

func NewKeywordAndCondition(keywordOptions []KeywordOptions) *KeywordConditionWrapper {
	return &KeywordConditionWrapper{
		Condition:      And,
		KeywordOptions: keywordOptions,
	}
}

func NewKeywordOrCondition(keywordOptions []KeywordOptions) *KeywordConditionWrapper {
	return &KeywordConditionWrapper{
		Condition:      Or,
		KeywordOptions: keywordOptions,
	}
}

func NewKeywordMustMatchOptions(keys []string, value string) []KeywordOptions {
	var kwOptions []KeywordOptions
	if len(keys) > 0 {
		kwOptions = make([]KeywordOptions, len(keys))
		for i, k := range keys {
			kwOptions[i] = KeywordOptions{
				Type:  MustMatch,
				Key:   k,
				Value: value,
			}
		}
	}

	return kwOptions
}

func NewKeywordMustMatchOption(key string, value string) *KeywordOptions {
	return &KeywordOptions{
		Type:  MustMatch,
		Key:   key,
		Value: value,
	}
}

func NewKeywordWildCardOptions(keys []string, value string) []KeywordOptions {
	var kwOptions []KeywordOptions
	if len(keys) > 0 {
		kwOptions = make([]KeywordOptions, len(keys))
		for i, k := range keys {
			kwOptions[i] = KeywordOptions{
				Type:  Wildcard,
				Key:   k,
				Value: value,
			}
		}
	}

	return kwOptions
}

func NewKeywordWildCardOption(key string, value string) *KeywordOptions {
	return &KeywordOptions{
		Type:  Wildcard,
		Key:   key,
		Value: value,
	}
}

func SetSearch(db *gorm.DB, keywordCondition *KeywordConditionWrapper) *gorm.DB {
	return setSearch(db, keywordCondition)
}

func SetSearchSimple(db *gorm.DB, q string, columns []string) *gorm.DB {
	return setSearch(db, NewKeywordOrCondition(NewKeywordWildCardOptions(columns, q)))
}

func DBErrorToIError(err error) IError {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return Error{
			Status:  http.StatusNotFound,
			Code:    "NOT_FOUND",
			Message: err.Error(),
		}
	}
	if err != nil {
		return Error{
			Status:  http.StatusInternalServerError,
			Code:    "DATABASE_ERROR",
			Message: err.Error(),
		}
	}

	return nil
}

func setSearch(db *gorm.DB, keywordCondition *KeywordConditionWrapper) *gorm.DB {
	innerDb := db.Session(&gorm.Session{NewDB: true})

	// When length of element in where is or condition e.g. (where(or)) it will be (or),
	// so we force to where when the length is one
	if len(keywordCondition.KeywordOptions) == 1 {
		if keywordCondition.KeywordOptions[0].Type == Wildcard {
			return db.Where(innerDb.Where(fmt.Sprintf(`%s LIKE ?`, keywordCondition.KeywordOptions[0].Key), fmt.Sprintf(`%%%%%s%%%%`, keywordCondition.KeywordOptions[0].Value)))
		} else if keywordCondition.KeywordOptions[0].Type == MustMatch {
			return db.Where(innerDb.Where(fmt.Sprintf(`%s = ?`, keywordCondition.KeywordOptions[0].Key), keywordCondition.KeywordOptions[0].Value))
		}
	}
	for _, kw := range keywordCondition.KeywordOptions {
		if kw.Key != "" && kw.Value != "" {
			switch kw.Type {
			case MustMatch:
				if keywordCondition.Condition == And {
					innerDb = innerDb.Where(fmt.Sprintf(`%s = ?`, kw.Key), kw.Value)
				} else if keywordCondition.Condition == Or {
					innerDb = innerDb.Or(fmt.Sprintf(`%s = ?`, kw.Key), kw.Value)
				}
			case Wildcard:
				if keywordCondition.Condition == And {
					innerDb = innerDb.Where(fmt.Sprintf(`%s LIKE ?`, kw.Key), fmt.Sprintf(`%%%%%s%%%%`, kw.Value))
				} else if keywordCondition.Condition == Or {
					innerDb = innerDb.Or(fmt.Sprintf(`%s LIKE ?`, kw.Key), fmt.Sprintf(`%%%%%s%%%%`, kw.Value))
				}
			default:
			}
		}
	}

	return db.Where(innerDb)
}
