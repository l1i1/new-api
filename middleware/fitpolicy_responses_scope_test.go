package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestFitPolicyScopeIncludesExactResponsesPath(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cases := []struct {
		path string
		want bool
	}{
		{"/v1/chat/completions", true},
		{"/v1/responses", true},
		{"/v1/responses/compact", false},
		{"/pg/chat/completions", false},
		{"/v1/messages", false},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, tc.path, nil)
			if got := fitPolicyInScope(c); got != tc.want {
				t.Fatalf("fitPolicyInScope(%q) = %v, want %v", tc.path, got, tc.want)
			}
		})
	}
}
