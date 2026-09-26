package router

import (
	"net/http"

	"github.com/QuantumNous/new-api/controller"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/service/authz"
	"github.com/gin-gonic/gin"
)

type permissionRoute struct {
	method     string
	path       string
	permission authz.Permission
	handler    gin.HandlerFunc
}

func registerChannelRoutes(apiRouter *gin.RouterGroup) {
	apiRouter.GET("/observability/channel-model",
		middleware.AdminAuth(),
		middleware.RequirePermission(authz.ChannelRead),
		controller.GetChannelModelObservability,
	)
	apiRouter.GET("/observability/channel-availability",
		middleware.AdminAuth(),
		middleware.RequirePermission(authz.ChannelRead),
		controller.GetChannelAvailability,
	)
	channelRoute := apiRouter.Group("/channel")
	channelRoute.Use(middleware.AdminAuth())

	channelRoute.POST("/:id/key",
		middleware.RootAuth(),
		middleware.CriticalRateLimit(),
		middleware.DisableCache(),
		middleware.SecureVerificationRequired(),
		controller.GetChannelKey,
	)

	for _, route := range channelPermissionRoutes {
		channelRoute.Handle(route.method, route.path,
			middleware.RequirePermission(route.permission),
			route.handler,
		)
	}

	// tokeness-fitpolicy:begin （上游 merge 后请保留；见 docs/fitpolicy-tech-spec.md）
	// The capability surface deliberately does NOT live under /api/channel: that
	// group applies AdminAuth to every route in it, and a controlled suite
	// applier must be able to hold capability.write without general
	// administrator rights. It carries UserAuth plus its own permission only, so
	// the allowed caller is defined by the granted action rather than by a role.
	// The path is static rather than /channel/fit-capability to avoid any
	// wildcard-vs-static conflict with the group's /:id routes.
	fitCapabilityRoute := apiRouter.Group("/fit-capability")
	fitCapabilityRoute.Use(middleware.UserAuth())
	// Reading marks stays an administrative view, exactly like every other
	// channel read. Only the write path is meant to be reachable by a
	// non-administrator principal; the applier learns the current revision from
	// the 409 body of its own write rather than by reading.
	fitCapabilityRoute.GET("",
		middleware.AdminAuth(),
		middleware.RequirePermission(authz.ChannelRead),
		controller.GetChannelFitCapabilities,
	)
	fitCapabilityRoute.PUT("",
		middleware.RequirePermission(authz.ChannelCapabilityWrite),
		controller.PutChannelFitCapability,
	)
	// The controlled suite applier posts a whole run here. It holds
	// capability.write and nothing else, so it can record measurements but
	// cannot force its way past a live operator mark.
	fitCapabilityRoute.POST("/report",
		middleware.RequirePermission(authz.ChannelCapabilityWrite),
		controller.PostFitCapabilityReport,
	)
	// tokeness-fitpolicy:end
}

var channelPermissionRoutes = []permissionRoute{
	{method: http.MethodGet, path: "/", permission: authz.ChannelRead, handler: controller.GetAllChannels},
	{method: http.MethodGet, path: "/observability", permission: authz.ChannelRead, handler: controller.GetLegacyChannelModelObservability},
	{method: http.MethodGet, path: "/search", permission: authz.ChannelRead, handler: controller.SearchChannels},
	{method: http.MethodGet, path: "/models", permission: authz.ChannelRead, handler: controller.ChannelListModels},
	{method: http.MethodGet, path: "/default_base_urls", permission: authz.ChannelRead, handler: controller.GetChannelDefaultBaseURLs},
	{method: http.MethodGet, path: "/models_enabled", permission: authz.ChannelRead, handler: controller.EnabledListModels},
	{method: http.MethodGet, path: "/ops", permission: authz.ChannelRead, handler: controller.GetChannelOps},
	{method: http.MethodGet, path: "/:id", permission: authz.ChannelRead, handler: controller.GetChannel},
	{method: http.MethodGet, path: "/test", permission: authz.ChannelOperate, handler: controller.TestAllChannels},
	{method: http.MethodGet, path: "/test/:id", permission: authz.ChannelOperate, handler: controller.TestChannel},
	{method: http.MethodGet, path: "/update_balance", permission: authz.ChannelOperate, handler: controller.UpdateAllChannelsBalance},
	{method: http.MethodGet, path: "/update_balance/:id", permission: authz.ChannelOperate, handler: controller.UpdateChannelBalance},
	{method: http.MethodPost, path: "/", permission: authz.ChannelSensitiveWrite, handler: controller.AddChannel},
	{method: http.MethodPut, path: "/", permission: authz.ChannelWrite, handler: controller.UpdateChannel},
	{method: http.MethodPost, path: "/:id/used_quota/reset", permission: authz.ChannelOperate, handler: controller.ResetChannelUsedQuota},
	{method: http.MethodPost, path: "/used_quota/reset", permission: authz.ChannelOperate, handler: controller.BatchResetChannelUsedQuota},
	{method: http.MethodPost, path: "/status/batch", permission: authz.ChannelOperate, handler: controller.BatchUpdateChannelStatus},
	{method: http.MethodPost, path: "/:id/status", permission: authz.ChannelOperate, handler: controller.UpdateChannelStatus},
	{method: http.MethodDelete, path: "/disabled", permission: authz.ChannelSensitiveWrite, handler: controller.DeleteDisabledChannel},
	{method: http.MethodPost, path: "/tag/disabled", permission: authz.ChannelOperate, handler: controller.DisableTagChannels},
	{method: http.MethodPost, path: "/tag/enabled", permission: authz.ChannelOperate, handler: controller.EnableTagChannels},
	{method: http.MethodPut, path: "/tag", permission: authz.ChannelWrite, handler: controller.EditTagChannels},
	{method: http.MethodDelete, path: "/:id", permission: authz.ChannelSensitiveWrite, handler: controller.DeleteChannel},
	{method: http.MethodPost, path: "/batch", permission: authz.ChannelSensitiveWrite, handler: controller.DeleteChannelBatch},
	{method: http.MethodPost, path: "/fix", permission: authz.ChannelOperate, handler: controller.FixChannelsAbilities},
	{method: http.MethodGet, path: "/fetch_models/:id", permission: authz.ChannelOperate, handler: controller.FetchUpstreamModels},
	{method: http.MethodGet, path: "/:id/vllm/status", permission: authz.ChannelRead, handler: controller.GetVLLMChannelStatus},
	{method: http.MethodGet, path: "/:id/sglang/status", permission: authz.ChannelRead, handler: controller.GetSGLangChannelStatus},
	{method: http.MethodPost, path: "/fetch_models", permission: authz.ChannelSensitiveWrite, handler: controller.FetchModels},
	{method: http.MethodPost, path: "/:id/codex/refresh", permission: authz.ChannelSensitiveWrite, handler: controller.RefreshCodexChannelCredential},
	{method: http.MethodGet, path: "/:id/codex/usage", permission: authz.ChannelRead, handler: controller.GetCodexChannelUsage},
	{method: http.MethodGet, path: "/:id/codex/usage/reset-credits", permission: authz.ChannelRead, handler: controller.GetCodexChannelRateLimitResetCredits},
	{method: http.MethodPost, path: "/:id/codex/usage/reset", permission: authz.ChannelOperate, handler: controller.ResetCodexChannelUsage},
	{method: http.MethodPost, path: "/ollama/pull", permission: authz.ChannelSensitiveWrite, handler: controller.OllamaPullModel},
	{method: http.MethodPost, path: "/ollama/pull/stream", permission: authz.ChannelSensitiveWrite, handler: controller.OllamaPullModelStream},
	{method: http.MethodDelete, path: "/ollama/delete", permission: authz.ChannelSensitiveWrite, handler: controller.OllamaDeleteModel},
	{method: http.MethodGet, path: "/ollama/version/:id", permission: authz.ChannelSensitiveWrite, handler: controller.OllamaVersion},
	{method: http.MethodPost, path: "/batch/tag", permission: authz.ChannelWrite, handler: controller.BatchSetChannelTag},
	{method: http.MethodGet, path: "/tag/models", permission: authz.ChannelRead, handler: controller.GetTagModels},
	{method: http.MethodPost, path: "/copy/:id", permission: authz.ChannelSensitiveWrite, handler: controller.CopyChannel},
	{method: http.MethodPost, path: "/multi_key/manage", permission: authz.ChannelOperate, handler: controller.ManageMultiKeys},
	{method: http.MethodPost, path: "/multi_key/scheduled_test", permission: authz.ChannelOperate, handler: controller.ScheduleMultiKeyTest},
	{method: http.MethodGet, path: "/:id/multi-key", permission: authz.ChannelRead, handler: controller.ListMultiKeyCredentials},
	{method: http.MethodPost, path: "/:id/multi-key/test", permission: authz.ChannelOperate, handler: controller.TestMultiKeys},
	{method: http.MethodGet, path: "/:id/multi-key/test/:task_id", permission: authz.ChannelOperate, handler: controller.GetMultiKeyTestTask},
	{method: http.MethodPost, path: "/:id/multi-key/test/:task_id/cancel", permission: authz.ChannelOperate, handler: controller.CancelMultiKeyTestTask},
	{method: http.MethodPost, path: "/:id/multi-key/status", permission: authz.ChannelOperate, handler: controller.UpdateMultiKeyStatus},
	{method: http.MethodPost, path: "/:id/multi-key/credentials", permission: authz.ChannelSensitiveWrite, handler: controller.AppendMultiKeyCredentials},
	{method: http.MethodPatch, path: "/:id/multi-key/proxy", permission: authz.ChannelSensitiveWrite, handler: controller.UpdateMultiKeyProxy},
	{method: http.MethodPost, path: "/:id/multi_key/test", permission: authz.ChannelOperate, handler: controller.TestMultiKeys},
	{method: http.MethodPost, path: "/:id/multi_key/status", permission: authz.ChannelOperate, handler: controller.UpdateMultiKeyStatus},
	{method: http.MethodPost, path: "/:id/multi_key/credentials", permission: authz.ChannelSensitiveWrite, handler: controller.AppendMultiKeyCredentials},
	{method: http.MethodPatch, path: "/:id/multi_key/proxy", permission: authz.ChannelSensitiveWrite, handler: controller.UpdateMultiKeyProxy},
	{method: http.MethodPost, path: "/upstream_updates/apply", permission: authz.ChannelWrite, handler: controller.ApplyChannelUpstreamModelUpdates},
	{method: http.MethodPost, path: "/upstream_updates/apply_all", permission: authz.ChannelWrite, handler: controller.ApplyAllChannelUpstreamModelUpdates},
	{method: http.MethodPost, path: "/upstream_updates/detect", permission: authz.ChannelOperate, handler: controller.DetectChannelUpstreamModelUpdates},
	{method: http.MethodPost, path: "/upstream_updates/detect_all", permission: authz.ChannelOperate, handler: controller.DetectAllChannelUpstreamModelUpdates},
}
