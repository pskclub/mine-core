package core

import "net/http"

type RequesterMethodType string

const (
	RequesterMethodTypeGET     RequesterMethodType = http.MethodGet
	RequesterMethodTypeHEAD    RequesterMethodType = http.MethodHead
	RequesterMethodTypePOST    RequesterMethodType = http.MethodPost
	RequesterMethodTypePUT     RequesterMethodType = http.MethodPut
	RequesterMethodTypePATCH   RequesterMethodType = http.MethodPatch
	RequesterMethodTypeDELETE  RequesterMethodType = http.MethodDelete
	RequesterMethodTypeOPTIONS RequesterMethodType = http.MethodOptions
	RequesterMethodTypeTRACE   RequesterMethodType = http.MethodTrace
)
