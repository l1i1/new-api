package channel

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type contextTestAdaptor struct {
	Adaptor
	baseURL string
}

func (a *contextTestAdaptor) GetRequestURL(*relaycommon.RelayInfo) (string, error) {
	return a.baseURL, nil
}

func (a *contextTestAdaptor) SetupRequestHeader(*gin.Context, *http.Header, *relaycommon.RelayInfo) error {
	return nil
}

type contextTestTaskAdaptor struct {
	TaskAdaptor
	baseURL string
}

func (a *contextTestTaskAdaptor) BuildRequestURL(*relaycommon.RelayInfo) (string, error) {
	return a.baseURL, nil
}

func (a *contextTestTaskAdaptor) BuildRequestHeader(*gin.Context, *http.Request, *relaycommon.RelayInfo) error {
	return nil
}

func TestUpstreamRequestsPropagateClientCancellation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service.InitHttpClient()

	tests := []struct {
		name string
		call func(*gin.Context, *relaycommon.RelayInfo, string) (*http.Response, error)
	}{
		{
			name: "api",
			call: func(c *gin.Context, info *relaycommon.RelayInfo, url string) (*http.Response, error) {
				return DoApiRequest(&contextTestAdaptor{baseURL: url}, c, info, nil)
			},
		},
		{
			name: "form",
			call: func(c *gin.Context, info *relaycommon.RelayInfo, url string) (*http.Response, error) {
				return DoFormRequest(&contextTestAdaptor{baseURL: url}, c, info, nil)
			},
		},
		{
			name: "task",
			call: func(c *gin.Context, info *relaycommon.RelayInfo, url string) (*http.Response, error) {
				return DoTaskApiRequest(&contextTestTaskAdaptor{baseURL: url}, c, info, nil)
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			requestStarted := make(chan struct{})
			requestCanceled := make(chan error, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				close(requestStarted)
				<-r.Context().Done()
				requestCanceled <- r.Context().Err()
			}))
			defer server.Close()

			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			request := httptest.NewRequest(http.MethodPost, "/relay", nil)
			requestContext, cancel := context.WithCancel(request.Context())
			c.Request = request.WithContext(requestContext)
			defer cancel()

			result := make(chan error, 1)
			go func() {
				resp, err := test.call(c, &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{}}, server.URL)
				if resp != nil {
					_ = resp.Body.Close()
				}
				result <- err
			}()

			select {
			case <-requestStarted:
			case <-time.After(5 * time.Second):
				t.Fatal("timed out waiting for upstream request")
			}
			cancel()

			select {
			case err := <-requestCanceled:
				require.True(t, errors.Is(err, context.Canceled), "upstream context should be canceled")
			case <-time.After(5 * time.Second):
				t.Fatal("timed out waiting for upstream cancellation")
			}

			select {
			case err := <-result:
				require.Error(t, err, "relay request should return after client cancellation")
			case <-time.After(5 * time.Second):
				t.Fatal("relay request did not terminate after client cancellation")
			}
		})
	}
}
