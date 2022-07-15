package core

import (
	"github.com/stretchr/testify/suite"
	"net/http"
	"testing"
)

type RequesterTestSuite struct {
	suite.Suite
}

func TestRequesterTestSuite(t *testing.T) {
	suite.Run(t, new(RequesterTestSuite))
}

func (r *RequesterTestSuite) TestRequest_RequesterToStructPagination_ExpectNetworkError() {
	mReq := NewMockRequester()
	mReq.On("Get", "/vc/schema", &RequesterOptions{
		BaseURL: "https://etda-ssi.finema.dev",
	}).Return(nil, Error{
		Status: http.StatusInternalServerError,
		Code:   "NETWORK_ERROR",
	})

	items := make([]interface{}, 0)
	pageResponse, ierr := RequesterToStructPagination(items, &PageOptions{}, func() (*RequestResponse, error) {
		return mReq.Get("/vc/schema", &RequesterOptions{
			BaseURL: "https://etda-ssi.finema.dev",
		})
	})

	r.Error(ierr)
	r.Equal("NETWORK_ERROR", ierr.GetCode())
	r.Nil(pageResponse)
}

func (r *RequesterTestSuite) TestRequest_RequesterToStructPagination_ExpectInternalServerError() {
	mReq := NewMockRequester()
	mReq.On("Get", "/vc/schema", &RequesterOptions{
		BaseURL: "https://etda-ssi.finema.dev",
	}).Return(&RequestResponse{
		Data: nil,
	}, nil)

	items := make([]interface{}, 0)
	pageResponse, ierr := RequesterToStructPagination(items, &PageOptions{}, func() (*RequestResponse, error) {
		return mReq.Get("/vc/schema", &RequesterOptions{
			BaseURL: "https://etda-ssi.finema.dev",
		})
	})

	r.Error(ierr)
	r.Equal("INTERNAL_SERVER_ERROR", ierr.GetCode())
	r.Nil(pageResponse)
}

func (r *RequesterTestSuite) TestRequest_RequesterToStructPagination_ExpectInvalidParamError() {
	mReq := NewMockRequester()
	mReq.On("Post", "/vc/schema", nil, &RequesterOptions{
		BaseURL: "https://etda-ssi.finema.dev",
	}).Return(&RequestResponse{
		Data: map[string]interface{}{
			"code":    "INVALID_PARAMS",
			"message": "Invalid parameters",
			"fields": map[string]interface{}{
				"singh_field": map[string]string{
					"code":    "REQUIRED",
					"message": "The singh_field field is required",
				},
			},
		},
		ErrorCode: "INVALID_PARAMS",
	}, nil)

	items := make([]interface{}, 0)
	pageResponse, ierr := RequesterToStructPagination(items, &PageOptions{}, func() (*RequestResponse, error) {
		return mReq.Post("/vc/schema", nil, &RequesterOptions{
			BaseURL: "https://etda-ssi.finema.dev",
		})
	})

	r.Error(ierr)
	r.Equal("INVALID_PARAMS", ierr.GetCode())
	r.Nil(pageResponse)
}
