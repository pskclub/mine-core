package coretest_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/pskclub/mine-core/v2/coretest"
)

// The fixture used to configure itself with t.Setenv, which Go forbids after
// t.Parallel(). A service suite built on coretest has to be allowed to run in
// parallel, so the configuration has to arrive some other way.
func TestNewContext_worksInAParallelTest(t *testing.T) {
	t.Parallel()
	ctx := coretest.NewContext(t, coretest.WithEnv(map[string]string{"jwt_secret": "parallel-ok"}))
	assert.Equal(t, "parallel-ok", ctx.ENV().Config().JWTSecret)
	assert.True(t, ctx.ENV().IsTest())
}
