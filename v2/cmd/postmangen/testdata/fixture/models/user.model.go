package models

// BaseModel is embedded, so its fields are promoted into every response that
// returns a model.
type BaseModel struct {
	ID string `json:"id"`
}

type User struct {
	BaseModel
	Email    string `json:"email"`
	FullName string `json:"full_name"`
	Password string `json:"-"`
	// Address reaches the location graph, which is where a rendered reply stops
	// being bounded by the model it started from.
	Address *Address `json:"address"`
}
