package models

type Note struct {
	BaseModel
	UserID string `json:"user_id"`
	Title  string `json:"title"`
	Body   string `json:"body"`
	Pinned bool   `json:"pinned"`
}
