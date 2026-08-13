package main

import (
	"fmt"

	"github.com/stretchr/testify/assert"
	req "github.com/stretchr/testify/require"
)

type reporter struct{}

func (reporter) Errorf(format string, args ...interface{}) {}
func (reporter) FailNow()                                  {}

func main() {
	var r reporter
	assert.Equal(r, 1, 1)
	assert.Equal(r, "a", "a")
	req.NoError(r, nil)
	fmt.Println("ok")
}
