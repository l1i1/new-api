package controller

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// TestRetryRoutingFailureReadsTheSelectionKind pins the discrimination the retry
// TestRetryRoutingFailureReadsTheSelectionKind pins what the retry
// error-fidelity guard is allowed to conclude from a failed re-selection.
//
// Every routing outcome of getChannel is reported with ErrorCodeGetChannelFailed,
// the selection kind has to survive getChannel's wrap (it wraps with %w for this
// reason), and even the no-candidate kind is only proof when the official-fit pin
// made the selector narrow before tiering. The cases below cover each way the
// conclusion could be drawn wrongly: a wrong kind, no kind at all, a saturating
// walk, and the unpinned selector defect that returns the no-candidate kind while
// a usable lower-priority channel still exists.
func TestRetryRoutingFailureReadsTheSelectionKind(t *testing.T) {
	gin.SetMode(gin.TestMode)
	pinned := func() *gin.Context {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		common.SetContextKey(c, constant.ContextKeyV4OfficialPin, true)
		return c
	}
	unpinned := func() *gin.Context {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		return c
	}
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
			require.Equal(t, test.want, retryFoundNoPinnedCandidate(pinned(), wrapped(test.kind), param()),
				"kind %q must not be mistaken for a missing candidate", test.kind)
		})
	}

	t.Run("without the pin the no-candidate kind proves nothing", func(t *testing.T) {
		// The selector defect: on the default path the request-path filter runs
		// after the tier has been chosen, so a top-tier channel that does not serve
		// this path shadows a usable lower-priority one and the selector returns
		// nil with a candidate still available. The same kind comes out, so the
		// conclusion needs the pin — the path that narrows before tiering.
		require.False(t, retryFoundNoPinnedCandidate(unpinned(),
			wrapped(service.ChannelSelectionTemporarilyUnavailable), param()),
			"an unpinned empty result may be a selector defect, not an exhausted candidate set")
	})

	t.Run("an untyped routing error is never taken for a missing candidate", func(t *testing.T) {
		// getChannel's defensive channel==nil return carries no structured kind,
		// and neither does the code a polling multi-key channel raises when it
		// cannot read its own credentials (model/channel.go) — that internal fault
		// reaches this branch through SetupContextForSelectedChannel. "No kind"
		// must therefore read as "not a no-candidate outcome".
		bare := types.NewError(
			fmt.Errorf("分组 %s 下模型 %s 的可用渠道不存在（retry）", "default", "glm-5.3-flash"),
			types.ErrorCodeGetChannelFailed, types.ErrOptionWithSkipRetry())
		require.False(t, retryFoundNoPinnedCandidate(pinned(), bare, param()))

		pollingFault := types.NewError(
			errors.New("failed to get available key: sql: database is closed"),
			types.ErrorCodeGetChannelFailed, types.ErrOptionWithSkipRetry())
		require.False(t, retryFoundNoPinnedCandidate(pinned(), pollingFault, param()),
			"a credential/cache fault is not an empty candidate set")
	})

	t.Run("a saturating walk keeps its own answer", func(t *testing.T) {
		selectionErr := wrapped(service.ChannelSelectionTemporarilyUnavailable)
		saturated := param()
		saturated.ExcludeSaturatedChannel(7)
		require.True(t, saturated.HasSaturatedChannel())
		require.False(t, retryFoundNoPinnedCandidate(pinned(), selectionErr, saturated),
			"nothing was tried against a busy channel, so there is no upstream error to answer with")
	})

	t.Run("another error code never qualifies", func(t *testing.T) {
		other := types.NewError(errors.New("upstream said no"), types.ErrorCodeBadResponseStatusCode, types.ErrOptionWithSkipRetry())
		require.False(t, retryFoundNoPinnedCandidate(pinned(), other, param()))
	})

	t.Run("a nil selection failure or param is handled", func(t *testing.T) {
		require.False(t, retryFoundNoPinnedCandidate(pinned(), nil, param()))
		require.False(t, retryFoundNoPinnedCandidate(nil, wrapped(service.ChannelSelectionTemporarilyUnavailable), param()),
			"a nil context cannot carry the pin, so nothing is proven")
		require.True(t, retryFoundNoPinnedCandidate(pinned(),
			wrapped(service.ChannelSelectionTemporarilyUnavailable), nil))
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

// TestRecordNoCandidateRetryErrorNeedsARequest pins the writer's own guard.
//
// RecordErrorLog reads the client address, so a context without a request must
// return before it rather than panic inside the logger. ErrorLogEnabled and the
// log handle are forced on for the duration, because with logging off the guard
// would never be reached and this test would pass for the wrong reason.
func TestRecordNoCandidateRetryErrorNeedsARequest(t *testing.T) {
	gin.SetMode(gin.TestMode)
	_, selectionErr, info := noCandidateTestContext(t)

	previousEnabled := constant.ErrorLogEnabled
	previousLogDB := model.LOG_DB
	constant.ErrorLogEnabled = true
	model.LOG_DB = nil
	t.Cleanup(func() {
		constant.ErrorLogEnabled = previousEnabled
		model.LOG_DB = previousLogDB
	})

	require.NotPanics(t, func() { recordNoCandidateRetryError(nil, info, selectionErr) })
	requestLess, _ := gin.CreateTestContext(httptest.NewRecorder())
	require.Nil(t, requestLess.Request)
	require.NotPanics(t, func() { recordNoCandidateRetryError(requestLess, info, selectionErr) },
		"a request-less context must return before the logger touches ClientIP")
	require.NotPanics(t, func() { recordNoCandidateRetryError(requestLess, info, nil) })
}

// TestNewAuditTestDatabaseLeavesTheCallersConnectionsAlone is the regression
// cover for a real pollution bug in this package's database fixture: its cleanup
// used to close whatever model.DB and model.LOG_DB pointed at, and because
// cleanups run last-in-first-out a caller that restored those globals in its own
// cleanup had them restored first and closed second — so the fixture took an
// outer connection down with it.
func TestNewAuditTestDatabaseLeavesTheCallersConnectionsAlone(t *testing.T) {
	outer, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:outer_%d?mode=memory&cache=shared", time.Now().UnixNano())), &gorm.Config{})
	require.NoError(t, err)
	previousDB, previousLogDB := model.DB, model.LOG_DB
	model.DB, model.LOG_DB = outer, outer
	t.Cleanup(func() {
		model.DB, model.LOG_DB = previousDB, previousLogDB
		if connection, err := outer.DB(); err == nil {
			_ = connection.Close()
		}
	})

	t.Run("fixture", func(t *testing.T) {
		fixture, _ := newAuditTestDatabase(t, "sqlite", "")
		previous := model.DB
		model.DB, model.LOG_DB = fixture, fixture
		// Registered after the fixture's own cleanup, so LIFO runs this restore
		// FIRST and the fixture's cleanup second — with the globals already back
		// on the outer connection. That is the real usage pattern, and it is what
		// used to hand the outer connection to the fixture's Close.
		t.Cleanup(func() { model.DB, model.LOG_DB = previous, previous })
	})

	require.NoError(t, outer.Exec("select 1").Error,
		"the fixture's cleanup must not close a connection it does not own")
}

// TestRetrySelectionFailureAnswersWithTheLastUpstreamError pins the client-visible
// rule added 2026-10-19: a retry that runs out of candidates answers with what the
// last real attempt said — upstream status code and body — never with the
// platform's routing error. The failover list contains 400, so the usual case is a
// client parameter error that the first channel reported honestly; masking it made
// the client believe the platform had broken.
func TestRetrySelectionFailureAnswersWithTheLastUpstreamError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	newContext := func() *gin.Context {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
		return c
	}
	param := func() *service.RetryParam {
		return &service.RetryParam{TokenGroup: "default", ModelName: "glm-5.3-flash", RequestPath: "/v1/chat/completions"}
	}
	routing := func(kind service.ChannelSelectionFailureKind) *types.NewAPIError {
		return types.NewError(
			fmt.Errorf("获取分组 %s 下模型 %s 的可用渠道失败（retry）: %w", "default", "glm-5.3-flash",
				&service.ChannelSelectionError{Kind: kind, Group: "default", Model: "glm-5.3-flash", Err: errors.New("selection detail")}),
			types.ErrorCodeGetChannelFailed, types.ErrOptionWithSkipRetry())
	}
	upstream400 := func() *types.NewAPIError {
		return types.WithOpenAIError(types.OpenAIError{
			Message: "角色信息不正确",
			Type:    "invalid_request_error",
			Code:    "1214",
		}, http.StatusBadRequest)
	}

	t.Run("an unpinned retry that ran out of candidates answers the upstream 400", func(t *testing.T) {
		upstream := upstream400()
		answer := answerRetrySelectionFailure(newContext(), &relaycommon.RelayInfo{LastError: upstream}, param(),
			routing(service.ChannelSelectionTemporarilyUnavailable))
		require.Same(t, upstream, answer)
		require.Equal(t, http.StatusBadRequest, answer.StatusCode)
		oaiErr := answer.ToOpenAIError()
		require.Equal(t, "角色信息不正确", oaiErr.Message)
		require.Equal(t, "invalid_request_error", oaiErr.Type)
		require.Equal(t, "1214", oaiErr.Code)
	})

	t.Run("a pinned provable exhaustion answers the verdict as well", func(t *testing.T) {
		c := newContext()
		common.SetContextKey(c, constant.ContextKeyV4OfficialPin, true)
		previous := constant.ErrorLogEnabled
		constant.ErrorLogEnabled = false
		t.Cleanup(func() { constant.ErrorLogEnabled = previous })
		upstream := upstream400()
		answer := answerRetrySelectionFailure(c, &relaycommon.RelayInfo{LastError: upstream}, param(),
			routing(service.ChannelSelectionTemporarilyUnavailable))
		require.Same(t, upstream, answer)
	})

	t.Run("an upstream 5xx is passed through when it is the last real answer", func(t *testing.T) {
		upstream := types.NewErrorWithStatusCode(errors.New("upstream unavailable"),
			types.ErrorCodeBadResponseStatusCode, http.StatusServiceUnavailable)
		answer := answerRetrySelectionFailure(newContext(), &relaycommon.RelayInfo{LastError: upstream}, param(),
			routing(service.ChannelSelectionInternalError))
		require.Same(t, upstream, answer)
		require.Equal(t, http.StatusServiceUnavailable, answer.StatusCode)
	})

	t.Run("even a routing fault answers the upstream error when one exists", func(t *testing.T) {
		upstream := upstream400()
		answer := answerRetrySelectionFailure(newContext(), &relaycommon.RelayInfo{LastError: upstream}, param(),
			routing(service.ChannelSelectionModelNotConfigured))
		require.Same(t, upstream, answer)
		require.NotContains(t, answer.Error(), "可用渠道失败")
	})

	t.Run("without any upstream answer the routing error is the honest one", func(t *testing.T) {
		routingErr := routing(service.ChannelSelectionTemporarilyUnavailable)
		answer := answerRetrySelectionFailure(newContext(), &relaycommon.RelayInfo{}, param(), routingErr)
		require.Same(t, routingErr, answer)
		require.Equal(t, types.ErrorCodeGetChannelFailed, answer.GetErrorCode())
	})
}
