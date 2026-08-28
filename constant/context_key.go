package constant

type ContextKey string

const (
	ContextKeyTokenCountMeta  ContextKey = "token_count_meta"
	ContextKeyPromptTokens    ContextKey = "prompt_tokens"
	ContextKeyEstimatedTokens ContextKey = "estimated_tokens"

	ContextKeyOriginalModel    ContextKey = "original_model"
	ContextKeyResolvedModel    ContextKey = "resolved_model"
	ContextKeyRequestStartTime ContextKey = "request_start_time"

	/* token related keys */
	ContextKeyTokenUnlimited         ContextKey = "token_unlimited_quota"
	ContextKeyTokenKey               ContextKey = "token_key"
	ContextKeyTokenId                ContextKey = "token_id"
	ContextKeyTokenGroup             ContextKey = "token_group"
	ContextKeyTokenSpecificChannelId ContextKey = "specific_channel_id"
	ContextKeyTokenModelLimitEnabled ContextKey = "token_model_limit_enabled"
	ContextKeyTokenModelLimit        ContextKey = "token_model_limit"
	ContextKeyTokenCrossGroupRetry   ContextKey = "token_cross_group_retry"
	ContextKeyTokenAutoGroups        ContextKey = "token_auto_groups"

	/* channel related keys */
	ContextKeyChannelId                ContextKey = "channel_id"
	ContextKeyChannelName              ContextKey = "channel_name"
	ContextKeyChannelCreateTime        ContextKey = "channel_create_time"
	ContextKeyChannelBaseUrl           ContextKey = "base_url"
	ContextKeyChannelType              ContextKey = "channel_type"
	ContextKeyChannelSetting           ContextKey = "channel_setting"
	ContextKeyChannelOtherSetting      ContextKey = "channel_other_setting"
	ContextKeyChannelParamOverride     ContextKey = "param_override"
	ContextKeyChannelHeaderOverride    ContextKey = "header_override"
	ContextKeyChannelOrganization      ContextKey = "channel_organization"
	ContextKeyChannelAutoBan           ContextKey = "auto_ban"
	ContextKeyChannelModelMapping      ContextKey = "model_mapping"
	ContextKeyChannelStatusCodeMapping ContextKey = "status_code_mapping"
	ContextKeyChannelIsMultiKey        ContextKey = "channel_is_multi_key"
	ContextKeyChannelMultiKeyIndex     ContextKey = "channel_multi_key_index"
	ContextKeyChannelCredentialId      ContextKey = "channel_credential_id"
	ContextKeyChannelProxyMode         ContextKey = "channel_proxy_mode"
	ContextKeyChannelEffectiveProxy    ContextKey = "channel_effective_proxy"
	ContextKeySelectedChannel          ContextKey = "selected_channel"
	ContextKeyChannelKey               ContextKey = "channel_key"
	ContextKeyForceMultiKeyIndex       ContextKey = "force_multi_key_index"
	// ContextKeyForceMultiKeyCredentialID pins a probe or other administrative
	// operation to the durable credential identity. The runtime resolves its
	// current legacy position immediately before selecting the key.
	ContextKeyForceMultiKeyCredentialID ContextKey = "force_multi_key_credential_id"
	ContextKeyIncludeDisabledKey        ContextKey = "include_disabled_key"

	ContextKeyChannelMultiKeyTried           ContextKey = "channel_multi_key_tried"
	ContextKeyChannelMultiKeySuccessRecorded ContextKey = "channel_multi_key_success_recorded"

	ContextKeyAutoGroup           ContextKey = "auto_group"
	ContextKeyAutoGroupIndex      ContextKey = "auto_group_index"
	ContextKeyAutoGroupRetryIndex ContextKey = "auto_group_retry_index"

	/* user related keys */
	ContextKeyUserId      ContextKey = "id"
	ContextKeyUserSetting ContextKey = "user_setting"
	ContextKeyUserQuota   ContextKey = "user_quota"
	ContextKeyUserStatus  ContextKey = "user_status"
	ContextKeyUserEmail   ContextKey = "user_email"
	ContextKeyUserGroup   ContextKey = "user_group"
	ContextKeyUsingGroup  ContextKey = "group"
	ContextKeyUserName    ContextKey = "username"
	// ContextKeyGroupAccessPolicy stores the immutable policy snapshot loaded
	// for the authenticated user's base group.
	ContextKeyGroupAccessPolicy ContextKey = "group_access_policy"

	ContextKeyLocalCountTokens ContextKey = "local_count_tokens"

	// ContextKeyV4OfficialPin marks a deepseek-v4 request whose sampling
	// parameters are known to diverge on aggregator upstreams, so channel
	// selection pins it to the official DeepSeek channel. Ordinary fit-able
	// requests keep normal aggregator routing.
	ContextKeyV4OfficialPin ContextKey = "v4_official_pin"

	// ContextKeyRelayInfoPtr stores the active *relaycommon.RelayInfo so
	// shared response writers (e.g. IOCopyBytesGracefully) can rewrite the
	// client-facing model name after per-format handlers resolved the
	// upstream (channel-mapped) model id.
	ContextKeyRelayInfoPtr ContextKey = "relay_info_ptr"

	ContextKeySystemPromptOverride ContextKey = "system_prompt_override"
	ContextKeyOllamaPromptCache    ContextKey = "ollama_prompt_cache"

	// ContextKeyFileSourcesToCleanup stores file sources that need cleanup when request ends
	ContextKeyFileSourcesToCleanup ContextKey = "file_sources_to_cleanup"

	// ContextKeyAdminRejectReason stores an admin-only reject/block reason extracted from upstream responses.
	// It is not returned to end users, but can be persisted into consume/error logs for debugging.
	ContextKeyAdminRejectReason ContextKey = "admin_reject_reason"

	// ContextKeyLanguage stores the user's language preference for i18n
	ContextKeyLanguage ContextKey = "language"
	ContextKeyIsStream ContextKey = "is_stream"

	// ContextKeyAuditLogged marks that the current request has already recorded
	// a manage/operation audit log inside the handler. When set, the admin-audit
	// fallback in authHelper (finishAdminAudit) skips its record to avoid
	// duplicate entries.
	ContextKeyAuditLogged ContextKey = "audit_logged"
)
