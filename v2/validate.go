package core

// IValidate is implemented by request payloads that validate without a context.
type IValidate interface {
	Valid() IError
}

// IValidateContext is implemented by request payloads that validate with access
// to the context (e.g. for DB-backed rules). Same contract as v1.
type IValidateContext interface {
	Valid(ctx IContext) IError
}
