package core

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"errors"
	"github.com/gojektech/heimdall/v6/httpclient"
	"github.com/gojektech/heimdall/v6/plugins"
	"github.com/sirupsen/logrus"
	"github.com/pskclub/mine-core/utils"
	"io"
	"io/ioutil"
	"net/http"
	xurl "net/url"
	"strings"
	"time"
)

type RequesterOptions struct {
	BaseURL    string
	Timeout    *time.Duration
	Headers    http.Header
	Params     xurl.Values
	RetryCount int
}

type RequestResponse struct {
	Data             map[string]interface{}
	RawData          []byte
	ErrorCode        string
	StatusCode       int
	Header           http.Header
	ContentLength    int64
	TransferEncoding []string
	Uncompressed     bool
	Trailer          http.Header
	Request          *http.Request
	TLS              *tls.ConnectionState
}

type IRequester interface {
	Get(url string, options *RequesterOptions) (*RequestResponse, error)
	Delete(url string, options *RequesterOptions) (*RequestResponse, error)
	Post(url string, body interface{}, options *RequesterOptions) (*RequestResponse, error)
	Put(url string, body interface{}, options *RequesterOptions) (*RequestResponse, error)
	Patch(url string, body interface{}, options *RequesterOptions) (*RequestResponse, error)
}

type Requester struct {
	client *httpclient.Client
	ctx    IContext
}

func NewRequester(ctx IContext) IRequester {
	client := httpclient.NewClient()
	requestLogger := plugins.NewRequestLogger(nil, nil)
	if ctx.ENV().Config().LogLevel == logrus.DebugLevel {
		client.AddPlugin(requestLogger)
	}

	return &Requester{
		client: client,
		ctx:    ctx,
	}
}

func newRequesterWithOptions(ctx IContext, options *RequesterOptions) *Requester {
	timeout := 30 * time.Second
	retryCount := 0

	if options != nil {
		if options.Timeout != nil {
			timeout = *options.Timeout
		}

		retryCount = options.RetryCount
	}
	client := httpclient.NewClient(
		httpclient.WithHTTPTimeout(timeout),
		httpclient.WithRetryCount(retryCount))
	requestLogger := plugins.NewRequestLogger(nil, nil)
	if ctx.ENV().Config().LogLevel == logrus.DebugLevel {
		client.AddPlugin(requestLogger)
	}

	return &Requester{
		client: client,
		ctx:    ctx,
	}
}

func (r Requester) Get(url string, options *RequesterOptions) (*RequestResponse, error) {
	url, headers := r.getOptions(url, options)
	res, err := r.client.Get(url, headers)
	return r.transformResponse(res, err)
}

func (r Requester) Delete(url string, options *RequesterOptions) (*RequestResponse, error) {
	url, headers := r.getOptions(url, options)
	res, err := r.client.Delete(url, headers)
	return r.transformResponse(res, err)
}

func (r Requester) Post(url string, body interface{}, options *RequesterOptions) (*RequestResponse, error) {
	url, headers := r.getOptions(url, options)
	res, err := r.client.Post(url, r.getJSONBody(body), headers)
	return r.transformResponse(res, err)
}

func (r Requester) Put(url string, body interface{}, options *RequesterOptions) (*RequestResponse, error) {
	url, headers := r.getOptions(url, options)
	res, err := r.client.Put(url, r.getJSONBody(body), headers)
	return r.transformResponse(res, err)
}

func (r Requester) Patch(url string, body interface{}, options *RequesterOptions) (*RequestResponse, error) {
	url, headers := r.getOptions(url, options)
	res, err := r.client.Patch(url, r.getJSONBody(body), headers)
	return r.transformResponse(res, err)
}

func (r Requester) transformResponse(res *http.Response, err error) (*RequestResponse, error) {
	var data map[string]interface{}

	if err != nil {
		return &RequestResponse{}, err
	}

	if res == nil {
		return &RequestResponse{
			Data: data,
		}, errors.New("Something went wrong")
	}

	result := &RequestResponse{
		Data:             data,
		StatusCode:       res.StatusCode,
		Header:           res.Header,
		ContentLength:    res.ContentLength,
		TransferEncoding: res.TransferEncoding,
		Uncompressed:     res.Uncompressed,
		Trailer:          res.Trailer,
		Request:          res.Request,
		TLS:              res.TLS,
	}

	if res.Body != nil {
		result.RawData, _ = ioutil.ReadAll(res.Body)
	}

	err = json.Unmarshal(result.RawData, &data)
	if err != nil {
		data = Map{
			"code":    "UNKNOWN_ERROR",
			"message": err.Error(),
		}
	}

	result.Data = data

	if res.StatusCode >= 200 && res.StatusCode < 300 {
		return result, nil
	}

	result.ErrorCode, _ = result.Data["code"].(string)

	return result, errors.New(utils.StructToString(result.Data))
}

func (r Requester) getURL(url string, opts *RequesterOptions) string {
	if opts != nil {
		newURL := opts.BaseURL + url
		if len(opts.Params) > 0 {
			params := opts.Params.Encode()
			if strings.IndexByte(newURL, '?') == -1 {
				newURL = newURL + "?" + params
			} else {
				newURL = newURL + "&" + params
			}
		}

		return newURL
	}

	return url
}

func (r Requester) getJSONBody(body interface{}) io.Reader {
	newBody, _ := json.Marshal(body)
	return bytes.NewReader(newBody)
}

func (r Requester) getOptions(_url string, opts *RequesterOptions) (url string, headers http.Header) {
	headers = make(http.Header)
	url = _url
	if opts != nil {
		r.client = newRequesterWithOptions(r.ctx, opts).client
		url = r.getURL(_url, opts)
		headers = opts.Headers
	}

	return url, headers
}
