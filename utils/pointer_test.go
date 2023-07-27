package utils

import (
	"github.com/stretchr/testify/assert"
	"testing"
)

func TestGetPointer(t *testing.T) {
	// Test case 1: Testing with an integer
	intValue := 42
	expectedIntPointer := &intValue
	actualIntPointer := ToPointer(intValue)
	assert.Equal(t, expectedIntPointer, actualIntPointer, "Integer pointer should match")

	// Test case 2: Testing with a string
	strValue := "test"
	expectedStrPointer := &strValue
	actualStrPointer := ToPointer(strValue)
	assert.Equal(t, expectedStrPointer, actualStrPointer, "String pointer should match")

	// Test case 3: Testing with a struct
	type Person struct {
		Name string
		Age  int
	}
	personValue := Person{Name: "John Doe", Age: 30}
	expectedPersonPointer := &personValue
	actualPersonPointer := ToPointer(personValue)
	assert.Equal(t, expectedPersonPointer, actualPersonPointer, "Struct pointer should match")
}

func TestToNonPointer(t *testing.T) {
	// Test case 1: Testing with an integer pointer
	intValue := 42
	intPointer := &intValue
	expectedIntValue := 42
	actualIntValue := ToNonPointer(intPointer)
	assert.Equal(t, expectedIntValue, actualIntValue, "Integer value should match")

	// Test case 2: Testing with a string pointer
	strValue := "test"
	strPointer := &strValue
	expectedStrValue := "test"
	actualStrValue := ToNonPointer(strPointer)
	assert.Equal(t, expectedStrValue, actualStrValue, "String value should match")

	// Test case 3: Testing with a struct pointer
	type Person struct {
		Name string
		Age  int
	}
	personValue := Person{Name: "John Doe", Age: 30}
	personPointer := &personValue
	expectedPersonValue := personValue
	actualPersonValue := ToNonPointer(personPointer)
	assert.Equal(t, expectedPersonValue, actualPersonValue, "Struct value should match")
}
