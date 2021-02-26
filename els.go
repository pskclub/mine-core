package core

import "github.com/elastic/go-elasticsearch/v7"

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
	client.Search()
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
