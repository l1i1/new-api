package helper

import (
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type deadlineRecorder struct {
	gin.ResponseWriter
	deadline time.Time
}

func (w *deadlineRecorder) SetWriteDeadline(t time.Time) error {
	w.deadline = t
	return nil
}

func TestExtendWriteDeadlineForSetsFutureDeadline(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	writer := &deadlineRecorder{}
	c.Writer = writer

	before := time.Now()
	ExtendWriteDeadlineFor(c, 2*time.Minute)
	require.False(t, writer.deadline.IsZero())
	require.True(t, writer.deadline.After(before.Add(time.Minute)))
}
