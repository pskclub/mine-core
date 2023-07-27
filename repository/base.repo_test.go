//go:build integration
// +build integration

package repository

import "testing"

type IUser struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

func (IUser) TableName() string {
	return "users"
}
func TestName(t *testing.T) {
	items, ierr := New[IUser](nil).Where("id = ?", 1).FindAll()
	t.Log(ierr)
	t.Log(items)
}
