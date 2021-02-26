package core

import (
	"bytes"
	"encoding/json"
	"github.com/elastic/go-elasticsearch/v7"
)

type ELS struct {
	Address  string
	User     string
	Password string
}

type els struct {
	connection *elasticsearch.Client
}

type IELS interface {
	Client() *elasticsearch.Client
	CreateIndex(name string, body interface{}, options *ELSCreateIndexOptions) error
}

func (e ELS) Connect() (IELS, error) {
	client, err := elasticsearch.NewClient(elasticsearch.Config{
		Addresses: []string{e.Address},
		Username:  e.User,
		Password:  e.Password,
	})
	if err != nil {
		return nil, err
	}

	return &els{connection: client}, nil
}

func NewELS(env *ENVConfig) *ELS {
	return &ELS{
		Address:  env.ELSAddress,
		User:     env.ELSUser,
		Password: env.ELSPassword,
	}
}

func (e els) Client() *elasticsearch.Client {
	return e.connection
}

type ELSCreateIndexOptions struct {
}

func (e els) CreateIndex(name string, body interface{}, options *ELSCreateIndexOptions) error {
	_, err := e.Client().Indices.Create(name)
	if err != nil {
		return err
	}

	if body != nil {
		var buf bytes.Buffer
		_ = json.NewEncoder(&buf).Encode(body)
		_, err = e.Client().Indices.PutMapping(&buf, e.Client().Indices.PutMapping.WithIndex(name))
		if err != nil {
			return err
		}
	}

	return nil
}
