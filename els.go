package core

import (
	"bytes"
	"encoding/json"
	"github.com/elastic/go-elasticsearch/v7"
	"github.com/elastic/go-elasticsearch/v7/esapi"
	"github.com/go-errors/errors"
	"github.com/pskclub/mine-core/utils"
	"github.com/tidwall/gjson"
	"io"
	"io/ioutil"
	"reflect"
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
	CreateIndex(name string, body map[string]interface{}, options *ELSCreateIndexOptions) error
	Create(dest interface{}, index string, id string, body interface{}, options *ELSCreateIndexOptions) (*esapi.Response, error)
	CreateOrUpdate(dest interface{}, index string, id string, body interface{}, options *ELSUpdateOptions) (*esapi.Response, error)
	SearchPagination(dest interface{}, index string, body map[string]interface{}, pageOptions *PageOptions, opts *ELSCreateSearchOptions) (*PageResponse, error)
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

func (e els) CreateIndex(name string, body map[string]interface{}, options *ELSCreateIndexOptions) error {
	_, err := e.Client().Indices.Create(name)
	if err != nil {
		return err
	}

	if body != nil {
		res, err := e.Client().Indices.PutMapping(e.interfaceToReader(body), e.Client().Indices.PutMapping.WithIndex(name))
		if err != nil {
			return err
		}

		if res.IsError() {
			return errors.New(res.String())
		}
	}

	return nil
}

type ELSCreateCreateOptions struct {
}

func (e els) Create(dest interface{}, index string, id string, body interface{}, options *ELSCreateIndexOptions) (*esapi.Response, error) {
	res, err := e.Client().Create(index, id, e.interfaceToReader(body))
	if err != nil {
		return nil, err
	}

	if res.IsError() {
		return nil, errors.New(res.String())
	}
	_ = utils.JSONParse(utils.StringToBytes(res.String()), dest)

	return res, nil
}

type ELSCreateSearchOptions struct {
}

func (e els) getFrom(pageOptions *PageOptions) int64 {
	return pageOptions.Limit * (pageOptions.Page - 1)
}

func (e els) SearchPagination(dest interface{}, index string, body map[string]interface{}, pageOptions *PageOptions, opts *ELSCreateSearchOptions) (*PageResponse, error) {
	body["from"] = e.getFrom(pageOptions)
	body["size"] = pageOptions.Limit
	res, err := e.Client().Search(e.Client().Search.WithBody(bytes.NewBufferString(utils.JSONToString(body))), e.Client().Search.WithIndex(index))
	if err != nil {
		return nil, err
	}

	if res.IsError() {
		return nil, errors.New(res.String())
	}

	resByte, _ := ioutil.ReadAll(res.Body)
	result := gjson.GetManyBytes(resByte, "hits.hits.#._source", "hits.total.value")
	_ = json.Unmarshal([]byte(result[0].Raw), dest)

	return &PageResponse{
		Total:   result[1].Int(),
		Limit:   pageOptions.Limit,
		Count:   int64(reflect.ValueOf(dest).Elem().Len()),
		Page:    pageOptions.Page,
		Q:       pageOptions.Q,
		OrderBy: pageOptions.OrderBy,
	}, nil
}

type ELSUpdateOptions struct {
}

func (e els) CreateOrUpdate(dest interface{}, index string, id string, body interface{}, options *ELSUpdateOptions) (*esapi.Response, error) {
	newBody := Map{
		"doc":           body,
		"doc_as_upsert": true,
	}
	res, err := e.Client().Update(index, id, e.interfaceToReader(newBody))
	if err != nil {
		return nil, err
	}

	if res.IsError() {
		return nil, errors.New(res.String())
	}

	_ = utils.JSONParse(utils.StringToBytes(res.String()), dest)

	return res, nil
}

func (e els) interfaceToReader(body interface{}) io.Reader {
	var buf bytes.Buffer
	_ = json.NewEncoder(&buf).Encode(body)
	return &buf
}
