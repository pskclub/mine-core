package utils

import (
	"github.com/stretchr/testify/assert"
	"testing"
)

func TestIsEmpty(t *testing.T) {
	v := IsEmpty("")
	if v != true {
		t.Errorf("Expect true")
	}

	v2 := IsEmpty(nil)
	if v2 != true {
		t.Errorf("Expect true")
	}

	v3 := IsEmpty(0)
	if v3 != true {
		t.Errorf("Expect true")
	}

	v4 := IsEmpty(" ")
	if v4 != false {
		t.Errorf("Expect false")
	}

	v5 := IsEmpty("eiei")
	if v5 != false {
		t.Errorf("Expect false")
	}
}

func TestMapToStruct(t *testing.T) {
	m := map[string]interface{}{
		"num": 55,
		"str": "5555",
		"boo": true,
	}

	type Str struct {
		Num int64
		Str string
		Boo bool
	}

	s := &Str{}

	assert.NoError(t, MapToStruct(m, s))
	assert.Equal(t, s.Num, int64(55))
	assert.Equal(t, s.Str, m["str"])
	assert.Equal(t, s.Boo, m["boo"])
}

func TestJSONParse(t *testing.T) {
	m := []byte(`
	{
		"num": 55,
		"str": "5555",
		"boo": true
	}
`)

	type Str struct {
		Num int64
		Str string
		Boo bool
	}

	s := &Str{}

	assert.NoError(t, JSONParse(m, s))
	assert.Equal(t, s.Num, int64(55))
	assert.Equal(t, s.Str, "5555")
	assert.Equal(t, s.Boo, true)
}
