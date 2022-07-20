package core

import (
	"github.com/tidwall/gjson"
	"github.com/pskclub/mine-core/consts"
	"github.com/pskclub/mine-core/utils"
)

type IABCIContext interface {
	IContext
	GetOperation(tx []byte) string
	GetMessageJSON(tx []byte) string
}

type ABCIContext struct {
	IContext
}

type ABCIContextOptions struct {
	ContextOptions *ContextOptions
}

func NewABCIContext(options *ABCIContextOptions) IABCIContext {
	ctxOptions := options.ContextOptions
	ctxOptions.contextType = consts.ABCI
	return &ABCIContext{NewContext(ctxOptions)}
}

func (A ABCIContext) GetOperation(tx []byte) string {
	operation := gjson.Get(A.GetMessageJSON(tx), "operation").String()
	return operation
}

func (A ABCIContext) GetMessageJSON(tx []byte) string {
	value := gjson.Get(utils.BytesToString(tx), "message").String()
	msgJSON, err := utils.Base64Decode(value)
	if err != nil {
		return ""
	}

	return msgJSON
}
