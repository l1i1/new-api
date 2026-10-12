package controller

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// TestRetryRoutingFailureReadsTheSelectionKind pins the discrimination the retry
// error-fidelity guard depends on.
//
// Every routing outcome of getChannel is reported with ErrorCodeGetChannelFailed,
// so the code alone cannot say "the retry found no candidate". The kind has to
// survive getChannel's wrap (it wraps with %w for this reason) and only
// temporarily_unavailable — plus the bare "no such channel" outcome, which
// carries no kind — may be treated as a routing failure. Getting this wrong makes
// the guard answer with an older upstream error when the platform actually failed
// to answer the routing question, or when the model was never configured.
func TestRetryRoutingFailureReadsTheSelectionKind(t *testing.T) {
	param := func() *service.RetryParam {
		return &service.RetryParam{
			TokenGroup: "default", ModelName: "glm-5.3-flash",
			RequestPath: "/v1/chat/completions",
		}
	}

	// wrapped builds the error exactly as getChannel does on the
	// "可用渠道失败（retry）" return, which is the only path whose kind the guard
	// can read.
	wrapped := func(kind service.ChannelSelectionFailureKind) *types.NewAPIError {
		selectionErr := &service.ChannelSelectionError{
			Kind: kind, Group: "default", Model: "glm-5.3-flash", Err: errors.New("selection detail"),
		}
		return types.NewError(
			fmt.Errorf("获取分组 %s 下模型 %s 的可用渠道失败（retry）: %w", "default", "glm-5.3-flash", selectionErr),
			types.ErrorCodeGetChannelFailed, types.ErrOptionWithSkipRetry())
	}

	for _, test := range []struct {
		kind service.ChannelSelectionFailureKind
		want bool
	}{
		{service.ChannelSelectionTemporarilyUnavailable, true},
		{service.ChannelSelectionInternalError, false},
		{service.ChannelSelectionModelNotConfigured, false},
		{service.ChannelSelectionAccessDenied, false},
	} {
		t.Run(string(test.kind), func(t *testing.T) {
			require.Equal(t, test.want, isRetryRoutingFailure(wrapped(test.kind), param()),
				"kind %q must not be mistaken for a missing candidate", test.kind)
		})
	}

	t.Run("the bare no-such-channel outcome counts", func(t *testing.T) {
		// getChannel's channel==nil return carries no structured kind; it is the
		// other genuine "the retry found nothing" outcome.
		bare := types.NewError(
			fmt.Errorf("分组 %s 下模型 %s 的可用渠道不存在（retry）", "default", "glm-5.3-flash"),
			types.ErrorCodeGetChannelFailed, types.ErrOptionWithSkipRetry())
		require.True(t, isRetryRoutingFailure(bare, param()))
	})

	t.Run("a saturating walk keeps its own answer", func(t *testing.T) {
		selectionErr := wrapped(service.ChannelSelectionTemporarilyUnavailable)
		saturated := param()
		saturated.ExcludeSaturatedChannel(7)
		require.True(t, saturated.HasSaturatedChannel())
		require.False(t, isRetryRoutingFailure(selectionErr, saturated),
			"nothing was tried against a busy channel, so there is no upstream error to answer with")
	})

	t.Run("another error code never qualifies", func(t *testing.T) {
		other := types.NewError(errors.New("upstream said no"), types.ErrorCodeBadResponseStatusCode, types.ErrOptionWithSkipRetry())
		require.False(t, isRetryRoutingFailure(other, param()))
	})

	t.Run("a nil selection failure or param is handled", func(t *testing.T) {
		require.False(t, isRetryRoutingFailure(nil, param()))
		require.True(t, isRetryRoutingFailure(wrapped(service.ChannelSelectionTemporarilyUnavailable), nil))
	})
}

// TestNoCandidateRetryLogRowKeepsTheUpstreamTextOutOfUserScope pins the scope of
// every field the routing-fidelity row carries.
//
// The upstream error text is the one field here that comes from a third party, so
// it must not reach the log owner: the user-visible projection of Other is not
// passed through the operator's message filter, while the error content is. It
// therefore goes to admin_info and through the same masking the content uses.
func TestNoCandidateRetryLogRowKeepsTheUpstreamTextOutOfUserScope(t *testing.T) {
	c, selectionErr, info := noCandidateTestContext(t)

	// A wording shaped like the ones an upstream really returns, carrying a URL
	// with a credential in its query for the masker to rewrite.
	upstream := types.NewOpenAIError(
		errors.New("upstream failed at https://relay.example.com/v1/keys?api_key=sk-live-secret: 角色信息不正确"),
		types.ErrorCodeBadResponseStatusCode, http.StatusBadRequest)
	info.LastError = upstream

	snap := noCandidateLogOther(c, info, selectionErr).Snapshot()

	// The operator half: present, masked, scoped.
	adminInfo, ok := snap["admin_info"].(map[string]any)
	require.True(t, ok, "the upstream text must live in admin_info, got %v", snap)
	stored, ok := adminInfo["last_upstream_error"].(string)
	require.True(t, ok, "admin_info must carry the upstream text")
	require.Equal(t, upstream.MaskSensitiveErrorWithStatusCode(), stored,
		"the stored text must go through the same masking the error content uses")
	require.NotContains(t, stored, "sk-live-secret", "the credential must not survive masking")
	require.NotContains(t, stored, "keys?api_key", "the masked URL must not keep its query")

	// The user half: the routing facts only.
	require.Equal(t, "routing_error", snap["error_type"])
	require.Equal(t, "get_channel_failed", snap["error_code"])
	require.Equal(t, true, snap["retry_no_candidate"])
	require.Equal(t, "local", snap["error_source"])
	require.Equal(t, 400, snap["last_upstream_status_code"])
	require.NotContains(t, snap, "last_upstream_error",
		"the upstream text must not sit in the user-visible top level")
	require.NotContains(t, snap, "channel_id")
	require.NotContains(t, snap, "channel_name")
}

// TestNoCandidateRetryLogRowSurvivesAMissingUpstreamError covers the shape a
// selection failure takes when nothing was tried yet: there is no upstream error
// to attach, and the row must still be built rather than skipped.
func TestNoCandidateRetryLogRowSurvivesAMissingUpstreamError(t *testing.T) {
	c, selectionErr, info := noCandidateTestContext(t)

	snap := noCandidateLogOther(c, info, selectionErr).Snapshot()
	require.Equal(t, true, snap["retry_no_candidate"])
	require.NotContains(t, snap, "last_upstream_status_code")
	if admin, ok := snap["admin_info"].(map[string]any); ok {
		require.NotContains(t, admin, "last_upstream_error")
	}
	require.NotPanics(t, func() { noCandidateLogOther(c, nil, selectionErr) })
	require.NotPanics(t, func() { noCandidateLogOther(nil, nil, selectionErr) })
}

// TestNoCandidateRetryErrorRowPersistsOnEveryDialect writes the row through the
// real writer and reads it back, on every database the deployment rule cares
// about. The new call site is the only thing this change adds to the database
// path, so the insert, its JSON payload and the round-trip are what need proving;
// mysql and postgres run when their DSN is configured, exactly as the other
// database-matrix tests in this package do.
func TestNoCandidateRetryErrorRowPersistsOnEveryDialect(t *testing.T) {
	for _, dialect := range []struct {
		kind   string
		env    string
		dbType common.DatabaseType
	}{
		{"sqlite", "", common.DatabaseTypeSQLite},
		{"mysql", "TEST_MYSQL_DSN", common.DatabaseTypeMySQL},
		{"postgres", "TEST_POSTGRES_DSN", common.DatabaseTypePostgreSQL},
	} {
		t.Run(dialect.kind, func(t *testing.T) {
			dsn := ""
			if dialect.env != "" {
				dsn = os.Getenv(dialect.env)
				if dsn == "" {
					t.Skipf("%s not configured", dialect.env)
				}
			}
			db, _ := newAuditTestDatabase(t, dialect.kind, dsn)

			previousDB, previousLogDB := model.DB, model.LOG_DB
			previousMain, previousLog := common.MainDatabaseType(), common.LogDatabaseType()
			previousErrorLogEnabled := constant.ErrorLogEnabled
			model.DB, model.LOG_DB = db, db
			common.SetDatabaseTypes(dialect.dbType, dialect.dbType)
			constant.ErrorLogEnabled = true
			t.Cleanup(func() {
				model.DB, model.LOG_DB = previousDB, previousLogDB
				common.SetDatabaseTypes(previousMain, previousLog)
				constant.ErrorLogEnabled = previousErrorLogEnabled
			})

			require.NoError(t, db.AutoMigrate(&model.User{}, &model.Log{}))
			require.NoError(t, db.Create(&model.User{Id: 7, Username: "log-owner", Group: "default"}).Error)

			c, selectionErr, info := noCandidateTestContext(t)
			info.LastError = types.NewOpenAIError(errors.New("upstream said 角色信息不正确"),
				types.ErrorCodeBadResponseStatusCode, http.StatusBadRequest)

			recordNoCandidateRetryError(c, info, selectionErr)

			var row model.Log
			require.NoError(t, db.Where("type = ?", model.LogTypeError).Order("id desc").First(&row).Error,
				"the routing-fidelity row must be stored on %s", dialect.kind)
			// The content carries the masked routing failure; the code lives in the
			// metadata, which is asserted below.
			require.Contains(t, row.Content, "可用渠道失败")
			require.Contains(t, row.Content, "temporarily_unavailable")

			var other map[string]any
			require.NoError(t, common.Unmarshal([]byte(row.Other), &other))
			require.Equal(t, true, other["retry_no_candidate"])
			require.Equal(t, "routing_error", other["error_type"])
			admin, ok := other["admin_info"].(map[string]any)
			require.True(t, ok, "admin_info must round-trip on %s", dialect.kind)
			adminText, _ := admin["last_upstream_error"].(string)
			require.Contains(t, adminText, "status_code=400")
			require.Contains(t, adminText, "角色信息不正确")
			require.NotContains(t, other, "last_upstream_error")
		})
	}
}

// noCandidateTestContext builds the request context and the two errors the log
// row needs: the selection failure that ended the retry, and a relay info whose
// LastError stands for the upstream verdict it displaced.
func noCandidateTestContext(t *testing.T) (*gin.Context, *types.NewAPIError, *relaycommon.RelayInfo) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	c.Set("id", 7)
	c.Set("username", "log-owner")
	c.Set("token_name", "test-token")
	c.Set("token_id", 11)
	c.Set("original_model", "glm-5.3-flash")
	c.Set("group", "default")
	selectionErr := types.NewError(
		fmt.Errorf("获取分组 %s 下模型 %s 的可用渠道失败（retry）: temporarily_unavailable", "default", "glm-5.3-flash"),
		types.ErrorCodeGetChannelFailed, types.ErrOptionWithSkipRetry())
	return c, selectionErr, &relaycommon.RelayInfo{OriginModelName: "glm-5.3-flash", TokenGroup: "default"}
}
