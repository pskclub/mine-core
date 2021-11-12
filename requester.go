package core

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"errors"
	"github.com/gojektech/heimdall/v6/httpclient"
	"github.com/gojektech/heimdall/v6/plugins"
	"github.com/pskclub/mine-core/utils"
	"github.com/sirupsen/logrus"
	"io"
	"io/ioutil"
	"mime/multipart"
	"net/http"
	xurl "net/url"
	"os"
	"strings"
	"time"
)

type RequesterOptions struct {
	BaseURL         string
	Timeout         *time.Duration
	Headers         http.Header
	Params          xurl.Values
	RetryCount      int
	IsMultipartForm bool
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

type IFile interface {
	Name() string
	Value() []byte
}

type File struct {
	name  string
	value []byte
}

// Deprecated: RequestWrapper is deprecated, use RequestToStruct or RequestToStructPagination instead.
func RequestWrapper(dest interface{}, requester func() (*RequestResponse, error)) (*RequestResponse, error) {
	res, err := requester()

	if err != nil {
		return nil, err
	}
	err = utils.MapToStruct(res.Data, dest)
	if err != nil {
		return nil, err
	}
	return res, err
}

func NewFile(name string, value []byte) IFile {
	return &File{
		name:  name,
		value: value,
	}
}

func (f File) Name() string {
	return f.name
}

func (f File) Value() []byte {
	return f.value
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

	if !options.IsMultipartForm {
		res, err := r.client.Post(url, r.getJSONBody(body), headers)
		return r.transformResponse(res, err)

	} else {
		newBody, contentType, err := r.getMultipartBody(body)
		if err != nil {
			return nil, err
		}

		headers.Add("Content-Type", contentType)

		res, err := r.client.Post(url, newBody, headers)
		return r.transformResponse(res, err)
	}

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
		}, errors.New("Something went wrong ")
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

func (r Requester) getMultipartBody(body interface{}) (*bytes.Buffer, string, error) {
	if newBody, ok := body.(map[string]interface{}); ok {
		var b bytes.Buffer
		var err error

		w := multipart.NewWriter(&b)

		for key, value := range newBody {
			var fw io.Writer

			if x, ok := value.(io.Closer); ok {
				defer x.Close()
			}

			if f, ok := value.(IFile); ok {

				fw, err = w.CreateFormFile(key, f.Name())
				if err != nil {
					return nil, "", err
				}

				_, err = fw.Write(f.Value())
				if err != nil {
					return nil, "", err
				}

			} else if f, ok := value.(*os.File); ok {
				if fw, err = w.CreateFormFile(key, f.Name()); err != nil {
					return nil, "", err
				}

				if _, err = io.Copy(fw, f); err != nil {
					return nil, "", err
				}

			} else if s, ok := value.(string); ok {
				if fw, err = w.CreateFormField(key); err != nil {
					return nil, "", err
				}

				_, err = fw.Write(utils.StringToBytes(s))
				if err != nil {
					return nil, "", err
				}

			} else {
				return nil, "", errors.New("A multipart/form-data value can either be IFile, *os.File, string ")
			}
		}

		if err = w.Close(); err != nil {
			return nil, "", err
		}

		return &b, w.FormDataContentType(), nil

	} else {

		return nil, "", errors.New("Requested body cannot be transform to multipart/form-data ")

	}
}

func (r Requester) getOptions(_url string, opts *RequesterOptions) (url string, headers http.Header) {
	headers = make(http.Header)
	url = _url

	if opts != nil {
		r.client = newRequesterWithOptions(r.ctx, opts).client
		url = r.getURL(_url, opts)
		if opts.Headers != nil {
			headers = opts.Headers
		}
	}

	return url, headers
}

func RequesterToStruct(desc interface{}, requester func() (*RequestResponse, error)) IError {
	res, err := requester()
	if res == nil {
		return Error{
			Status:  http.StatusInternalServerError,
			Code:    "NETWORK_ERROR",
			Message: err.Error(),
		}
	}

	if res.ErrorCode != "" {
		ierr := Error{}
		_ = utils.MapToStruct(res.Data, &ierr)
		ierr.Status = res.StatusCode
		return ierr
	}

	if err != nil {
		return Error{
			Status:  http.StatusInternalServerError,
			Code:    "NETWORK_ERROR",
			Message: err.Error(),
		}
	}
	if err = json.Unmarshal(res.RawData, desc); err != nil {
		return Error{
			Status:  http.StatusInternalServerError,
			Code:    "INTERNAL_SERVER_ERROR",
			Message: err.Error(),
		}
	}

	return nil
}

func RequesterToStructPagination(items interface{}, options *PageOptions, requester func() (*RequestResponse, error)) (*PageResponse, IError) {
	res, err := requester()
	if res == nil {
		return nil, Error{
			Status:  http.StatusInternalServerError,
			Code:    "NETWORK_ERROR",
			Message: err.Error(),
		}
	}

	if res.ErrorCode != "" {
		ierr := Error{}
		_ = utils.MapToStruct(res.Data, &ierr)
		ierr.Status = res.StatusCode
		return nil, ierr
	}

	if err != nil {
		return nil, Error{
			Status:  http.StatusInternalServerError,
			Code:    "NETWORK_ERROR",
			Message: err.Error(),
		}
	}

	itemByte, err := json.Marshal(res.Data["items"])
	if err != nil {
		return nil, Error{
			Status:  http.StatusInternalServerError,
			Code:    "INTERNAL_SERVER_ERROR",
			Message: err.Error(),
		}
	}

	if err = json.Unmarshal(itemByte, &items); err != nil {
		return nil, Error{
			Status:  http.StatusInternalServerError,
			Code:    "INTERNAL_SERVER_ERROR",
			Message: err.Error(),
		}
	}

	pageResponse := &PageResponse{
		Q:       options.Q,
		OrderBy: options.OrderBy,
	}

	if length, ok := items.([]interface{}); ok {
		pageResponse.Count = int64(len(length))
	}

	if err = json.Unmarshal(res.RawData, pageResponse); err != nil {
		return nil, Error{
			Status:  http.StatusInternalServerError,
			Code:    "INTERNAL_SERVER_ERROR",
			Message: err.Error(),
		}
	}

	return pageResponse, nil
}
