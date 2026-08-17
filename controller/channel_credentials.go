package controller

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	channelobservability "github.com/QuantumNous/new-api/pkg/channel_observability"
	"github.com/QuantumNous/new-api/service"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type multiKeyStatusRequest struct {
	CredentialIDs []int  `json:"credential_ids"`
	Positions     []int  `json:"key_indices"`
	All           bool   `json:"all"`
	Status        string `json:"status"`
	Reason        string `json:"reason"`
	KeysRevision  int64  `json:"keys_revision"`
}

type multiKeyProxyRequest struct {
	CredentialID  int    `json:"credential_id"`
	CredentialIDs []int  `json:"credential_ids"`
	All           bool   `json:"all"`
	ProxyMode     string `json:"proxy_mode"`
	ProxyURL      string `json:"proxy_url"`
	KeysRevision  int64  `json:"keys_revision"`
}

type multiKeyTestRequest struct {
	CredentialIDs   []int  `json:"credential_ids"`
	KeyIndices      []int  `json:"key_indices"`
	All             bool   `json:"all"`
	IncludeDisabled bool   `json:"include_disabled"`
	Model           string `json:"model"`
	EndpointType    string `json:"endpoint_type"`
	Stream          bool   `json:"stream"`
	KeysRevision    int64  `json:"keys_revision"`
	Concurrency     int    `json:"concurrency"`
	TimeoutSeconds  int    `json:"timeout"`
}

type multiKeyTestResult struct {
	CredentialID int    `json:"credential_id"`
	Index        int    `json:"index"`
	Fingerprint  string `json:"fingerprint"`
	Status       string `json:"status"`
	HTTPStatus   int    `json:"http_status,omitempty"`
	LatencyMs    int64  `json:"latency_ms"`
	ErrorCode    string `json:"error_code,omitempty"`
	ErrorClass   string `json:"error_class,omitempty"`
	ErrorMessage string `json:"error_message,omitempty"`
	TestedAt     int64  `json:"tested_at"`
}

type channelCredentialTestTaskPayload struct {
	TaskID          string `json:"task_id,omitempty"`
	ChannelID       int    `json:"channel_id"`
	CredentialIDs   []int  `json:"credential_ids"`
	KeysRevision    int64  `json:"keys_revision"`
	Model           string `json:"model,omitempty"`
	EndpointType    string `json:"endpoint_type,omitempty"`
	Stream          bool   `json:"stream,omitempty"`
	IncludeDisabled bool   `json:"include_disabled,omitempty"`
	OperatorID      int    `json:"operator_id"`
	Concurrency     int    `json:"concurrency,omitempty"`
	TimeoutSeconds  int    `json:"timeout,omitempty"`
}

type channelCredentialTestTaskResult struct {
	ChannelID    int                  `json:"channel_id"`
	KeysRevision int64                `json:"keys_revision"`
	Results      []multiKeyTestResult `json:"results"`
}

// UpdateMultiKeyStatus applies one transactional state change to selected
// credentials. It is separate from the legacy action endpoint so clients can
// use stable credential IDs and an explicit all flag.
func UpdateMultiKeyStatus(c *gin.Context) {
	channelID, err := strconv.Atoi(c.Param("id"))
	if err != nil || channelID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "invalid channel id"})
		return
	}
	var request multiKeyStatusRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": err.Error()})
		return
	}
	status := common.ChannelStatusManuallyDisabled
	if strings.EqualFold(strings.TrimSpace(request.Status), "enabled") || strings.EqualFold(strings.TrimSpace(request.Status), "enable") {
		status = common.ChannelStatusEnabled
	} else if request.Status != "" && !strings.EqualFold(strings.TrimSpace(request.Status), "manual_disabled") && !strings.EqualFold(strings.TrimSpace(request.Status), "disabled") {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "status must be enabled or manual_disabled"})
		return
	}
	revision, err := model.UpdateChannelCredentialStatuses(model.DB, model.ChannelCredentialStatusUpdate{
		ChannelID: channelID, CredentialIDs: request.CredentialIDs, Positions: request.Positions,
		All: request.All, Status: status, Reason: request.Reason, ExpectedRev: request.KeysRevision,
	})
	if err != nil {
		writeChannelCredentialError(c, err)
		return
	}
	model.InitChannelCache()
	c.JSON(http.StatusOK, gin.H{"success": true, "keys_revision": revision})
}

// UpdateMultiKeyProxy stores an inherit/direct/custom proxy override. The
// URL is write-only; the response contains only the canonical mode and a safe
// host summary from the refreshed credential view.
func UpdateMultiKeyProxy(c *gin.Context) {
	channelID, err := strconv.Atoi(c.Param("id"))
	if err != nil || channelID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "invalid channel id"})
		return
	}
	var request multiKeyProxyRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": err.Error()})
		return
	}
	if request.CredentialID > 0 && len(request.CredentialIDs) == 0 {
		request.CredentialIDs = []int{request.CredentialID}
	}
	oldProxies, newProxy, revision, err := model.UpdateChannelCredentialProxies(model.DB, model.ChannelCredentialProxyUpdate{ChannelID: channelID, CredentialIDs: request.CredentialIDs, All: request.All, Mode: request.ProxyMode, ProxyURL: request.ProxyURL, ExpectedRev: request.KeysRevision})
	if err != nil {
		writeChannelCredentialError(c, err)
		return
	}
	for _, oldProxy := range oldProxies {
		if oldProxy != "" {
			service.InvalidateProxyClient(oldProxy)
		}
	}
	if newProxy != "" {
		service.InvalidateProxyClient(newProxy)
	}
	model.InitChannelCache()
	response := gin.H{"success": true, "keys_revision": revision}
	response["proxy_mode"] = model.NormalizeCredentialProxyMode(request.ProxyMode)
	response["proxy_configured"] = newProxy != ""
	c.JSON(http.StatusOK, response)
}

// ListMultiKeyCredentials returns safe credential metadata. Secrets and full
// proxy URLs are never included in this response.
func ListMultiKeyCredentials(c *gin.Context) {
	channelID, err := strconv.Atoi(c.Param("id"))
	if err != nil || channelID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "invalid channel id"})
		return
	}
	channel, err := model.GetChannelById(channelID, true)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	if !channel.ChannelInfo.IsMultiKey {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "channel is not a multi-key channel"})
		return
	}
	credentials := channel.Credentials
	if len(credentials) == 0 {
		credentials, err = model.ListChannelCredentials(model.DB, channelID)
		if err != nil {
			common.ApiError(c, err)
			return
		}
	}
	public := make([]model.ChannelCredentialPublic, 0, len(credentials))
	page := parseBoundedInt(c.Query("page"), 1, 1, 100000)
	pageSize := parseBoundedInt(c.Query("page_size"), 50, 1, 100)
	statusFilter := parseBoundedInt(c.Query("status"), -1, -1, common.ChannelStatusAutoDisabled)
	for _, credential := range credentials {
		if credential.Position < 0 {
			continue
		}
		if statusFilter >= 0 && credential.Status != statusFilter {
			continue
		}
		public = append(public, credential.PublicView())
	}
	revision, err := model.GetChannelCredentialRevision(model.DB, channelID)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	total := len(public)
	totalPages := (total + pageSize - 1) / pageSize
	if totalPages == 0 {
		totalPages = 1
	}
	start := (page - 1) * pageSize
	if start > total {
		start = total
	}
	end := start + pageSize
	if end > total {
		end = total
	}
	pageKeys := public[start:end]
	enabledCount, manualDisabledCount, autoDisabledCount := 0, 0, 0
	for _, credential := range public {
		switch credential.Status {
		case common.ChannelStatusEnabled:
			enabledCount++
		case common.ChannelStatusManuallyDisabled:
			manualDisabledCount++
		case common.ChannelStatusAutoDisabled:
			autoDisabledCount++
		}
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": gin.H{
		"keys": pageKeys, "total": total, "page": page, "page_size": pageSize,
		"total_pages": totalPages, "enabled_count": enabledCount,
		"manual_disabled_count": manualDisabledCount, "auto_disabled_count": autoDisabledCount,
		"keys_revision": revision,
	}})
}

// TestMultiKeys enqueues a bounded asynchronous probe. Empty selections are
// rejected; callers must opt into the whole pool with all=true.
func TestMultiKeys(c *gin.Context) {
	channelID, err := strconv.Atoi(c.Param("id"))
	if err != nil || channelID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "invalid channel id"})
		return
	}
	var request multiKeyTestRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": err.Error()})
		return
	}
	channel, err := model.GetChannelById(channelID, true)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	if !channel.ChannelInfo.IsMultiKey {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "channel is not a multi-key channel"})
		return
	}
	currentRevision, err := model.GetChannelCredentialRevision(model.DB, channelID)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	if request.KeysRevision > 0 && request.KeysRevision != currentRevision {
		writeChannelCredentialError(c, model.ErrChannelCredentialRevisionConflict)
		return
	}
	credentials := channel.Credentials
	if len(credentials) == 0 {
		credentials, err = model.ListChannelCredentials(model.DB, channelID)
		if err != nil {
			common.ApiError(c, err)
			return
		}
	}
	credentialIDs := selectMultiKeyCredentialIDs(credentials, request)
	if len(credentialIDs) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "no keys selected"})
		return
	}
	if len(credentialIDs) > 100 {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "at most 100 keys can be tested per request"})
		return
	}
	if request.Concurrency <= 0 {
		request.Concurrency = 4
	}
	if request.Concurrency > 16 {
		request.Concurrency = 16
	}
	if request.TimeoutSeconds <= 0 {
		request.TimeoutSeconds = 60
	}
	if request.TimeoutSeconds > 300 {
		request.TimeoutSeconds = 300
	}
	task, created, err := service.EnqueueSystemTaskWithKey(model.SystemTaskTypeChannelCredentialTest, fmt.Sprintf("channel:%d", channelID), channelCredentialTestTaskPayload{
		ChannelID: channelID, CredentialIDs: credentialIDs, KeysRevision: currentRevision,
		Model: request.Model, EndpointType: request.EndpointType, Stream: request.Stream,
		IncludeDisabled: request.IncludeDisabled, OperatorID: c.GetInt("id"), Concurrency: request.Concurrency, TimeoutSeconds: request.TimeoutSeconds,
	})
	if err != nil {
		common.ApiError(c, err)
		return
	}
	if !created {
		c.JSON(http.StatusConflict, gin.H{"success": false, "message": "a credential test task is already running", "data": task.ToResponse()})
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"success": true, "data": task.ToResponse()})
}

func selectMultiKeyCredentialIDs(credentials []model.ChannelCredential, request multiKeyTestRequest) []int {
	selected := make(map[int]bool)
	for _, id := range request.CredentialIDs {
		for _, credential := range credentials {
			if credential.Id == id && credential.Position >= 0 && (request.IncludeDisabled || credential.Status == common.ChannelStatusEnabled) {
				selected[id] = true
			}
		}
	}
	if request.All {
		for _, credential := range credentials {
			if credential.Position >= 0 && (request.IncludeDisabled || credential.Status == common.ChannelStatusEnabled) {
				selected[credential.Id] = true
			}
		}
	} else {
		for _, position := range request.KeyIndices {
			if position >= 0 {
				for _, credential := range credentials {
					if credential.Position == position && (request.IncludeDisabled || credential.Status == common.ChannelStatusEnabled) {
						selected[credential.Id] = true
					}
				}
			}
		}
	}
	ids := make([]int, 0, len(selected))
	for _, credential := range credentials {
		if credential.Position >= 0 && selected[credential.Id] {
			ids = append(ids, credential.Id)
		}
	}
	return ids
}

// runMultiKeyCredentialTests performs the bounded work for the system-task
// handler. Results are keyed by durable credential ID and never contain raw
// upstream or proxy errors.
func runMultiKeyCredentialTests(ctx context.Context, payload channelCredentialTestTaskPayload, report func(processed, total int)) (channelCredentialTestTaskResult, error) {
	if payload.ChannelID <= 0 || len(payload.CredentialIDs) == 0 {
		return channelCredentialTestTaskResult{}, errors.New("invalid credential test payload")
	}
	channel, err := model.GetChannelById(payload.ChannelID, true)
	if err != nil {
		return channelCredentialTestTaskResult{}, err
	}
	if !channel.ChannelInfo.IsMultiKey {
		return channelCredentialTestTaskResult{}, errors.New("channel is not a multi-key channel")
	}
	currentRevision, err := model.GetChannelCredentialRevision(model.DB, payload.ChannelID)
	if err != nil {
		return channelCredentialTestTaskResult{}, err
	}
	if payload.KeysRevision > 0 && payload.KeysRevision != currentRevision {
		return channelCredentialTestTaskResult{}, model.ErrChannelCredentialRevisionConflict
	}
	credentials, err := model.ListChannelCredentials(model.DB, payload.ChannelID)
	if err != nil {
		return channelCredentialTestTaskResult{}, err
	}
	byID := make(map[int]model.ChannelCredential, len(credentials))
	for _, credential := range credentials {
		byID[credential.Id] = credential
	}
	selected := make([]model.ChannelCredential, 0, len(payload.CredentialIDs))
	seen := make(map[int]bool, len(payload.CredentialIDs))
	for _, id := range payload.CredentialIDs {
		if seen[id] {
			continue
		}
		credential, ok := byID[id]
		if !ok || credential.Position < 0 {
			continue
		}
		if !payload.IncludeDisabled && credential.Status != common.ChannelStatusEnabled {
			continue
		}
		seen[id] = true
		selected = append(selected, credential)
	}
	if len(selected) == 0 {
		return channelCredentialTestTaskResult{}, model.ErrChannelCredentialSelectionEmpty
	}
	testUserID, err := resolveChannelTestUserID(nil)
	if err != nil {
		return channelCredentialTestTaskResult{}, err
	}

	type completedResult struct {
		order  int
		result multiKeyTestResult
	}
	jobs := make(chan struct {
		order      int
		credential model.ChannelCredential
	})
	results := make(chan completedResult, len(selected))
	workerCount := len(selected)
	if payload.Concurrency <= 0 {
		payload.Concurrency = 4
	}
	if payload.Concurrency > 16 {
		payload.Concurrency = 16
	}
	if workerCount > payload.Concurrency {
		workerCount = payload.Concurrency
	}
	timeout := time.Duration(payload.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	var workers sync.WaitGroup
	for worker := 0; worker < workerCount; worker++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for job := range jobs {
				if ctx.Err() != nil {
					return
				}
				cancelRequested, cancelErr := model.IsSystemTaskCancelRequested(payload.TaskID)
				if cancelErr == nil && cancelRequested {
					return
				}
				started := time.Now()
				probeCtx, cancel := context.WithTimeout(ctx, timeout)
				credentialID := job.credential.Id
				probe := testChannelWithOptions(probeCtx, channel, testUserID, payload.Model, payload.EndpointType, payload.Stream, &credentialID, payload.IncludeDisabled, false)
				cancel()
				result := buildMultiKeyTestResult(job.credential, probe, time.Since(started), ctx)
				_ = model.RecordChannelCredentialTest(model.DB, payload.ChannelID, job.credential.Id, result.Status, result.LatencyMs, result.HTTPStatus, result.ErrorCode, result.ErrorClass, result.ErrorMessage)
				results <- completedResult{order: job.order, result: result}
			}
		}()
	}
enqueue:
	for order, credential := range selected {
		select {
		case jobs <- struct {
			order      int
			credential model.ChannelCredential
		}{order: order, credential: credential}:
		case <-ctx.Done():
			break enqueue
		}
	}
	close(jobs)
	workers.Wait()
	close(results)
	ordered := make([]multiKeyTestResult, len(selected))
	completed := 0
	for item := range results {
		if item.order >= 0 && item.order < len(ordered) {
			ordered[item.order] = item.result
			completed++
			if report != nil {
				report(completed, len(selected))
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return channelCredentialTestTaskResult{}, err
	}
	if completed != len(selected) {
		return channelCredentialTestTaskResult{}, errors.New("credential test task did not complete all selected keys")
	}
	return channelCredentialTestTaskResult{ChannelID: payload.ChannelID, KeysRevision: currentRevision, Results: ordered}, nil
}

func buildMultiKeyTestResult(credential model.ChannelCredential, probe testResult, elapsed time.Duration, ctx context.Context) multiKeyTestResult {
	result := multiKeyTestResult{CredentialID: credential.Id, Index: credential.Position, Fingerprint: credential.Fingerprint, Status: "success", LatencyMs: elapsed.Milliseconds(), TestedAt: time.Now().Unix()}
	if probe.localErr == nil && probe.newAPIError == nil {
		return result
	}
	result.Status = "failed"
	if probe.newAPIError != nil {
		result.HTTPStatus = probe.newAPIError.StatusCode
		result.ErrorClass = classifyChannelCredentialTestError(probe.newAPIError.StatusCode, string(probe.newAPIError.GetErrorCode()), probe.newAPIError.Error(), ctx)
		result.ErrorCode = string(probe.newAPIError.GetErrorCode())
		if result.ErrorCode == "" {
			result.ErrorCode = result.ErrorClass
		}
		result.ErrorMessage = sanitizeMultiKeyTestError(probe.newAPIError.MaskSensitiveError())
	} else {
		result.ErrorClass = classifyChannelCredentialTestError(0, "", probe.localErr.Error(), ctx)
		result.ErrorCode = result.ErrorClass
		result.ErrorMessage = sanitizeMultiKeyTestError(common.MaskSensitiveInfo(probe.localErr.Error()))
	}
	return result
}

func sanitizeMultiKeyTestError(message string) string {
	const maxErrorRunes = 512
	message = strings.Join(strings.Fields(message), " ")
	if message == "" {
		return ""
	}
	runes := []rune(message)
	if len(runes) > maxErrorRunes {
		return string(runes[:maxErrorRunes]) + "..."
	}
	return message
}

func classifyChannelCredentialTestError(status int, errorCode, message string, ctx context.Context) string {
	if ctx != nil && errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return "timeout"
	}
	lower := strings.ToLower(errorCode + " " + message)
	if status == http.StatusUnauthorized || status == http.StatusForbidden || strings.Contains(lower, "unauthorized") || strings.Contains(lower, "invalid api key") {
		return "authentication"
	}
	if status == http.StatusTooManyRequests || strings.Contains(lower, "rate limit") || strings.Contains(lower, "too many") {
		return "rate_limited"
	}
	if strings.Contains(lower, "proxy") || strings.Contains(lower, "dial tcp") || strings.Contains(lower, "no such host") || strings.Contains(lower, "connection refused") {
		return "proxy_network"
	}
	if strings.Contains(lower, "model") && (status == http.StatusBadRequest || status == http.StatusNotFound) {
		return "model_access"
	}
	if status >= 500 || status == http.StatusRequestTimeout {
		return "upstream"
	}
	if strings.Contains(lower, "parse") || strings.Contains(lower, "response body") || strings.Contains(lower, "json") {
		return "parse"
	}
	return "upstream"
}

// GetMultiKeyTestTask exposes only the task state for the requested channel.
// The payload/result contain stable IDs and classified errors, never secrets.
func GetMultiKeyTestTask(c *gin.Context) {
	channelID, err := strconv.Atoi(c.Param("id"))
	if err != nil || channelID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "invalid channel id"})
		return
	}
	task, err := model.GetSystemTaskByTaskID(c.Param("task_id"))
	if err != nil {
		common.ApiError(c, err)
		return
	}
	if task == nil || task.Type != model.SystemTaskTypeChannelCredentialTest {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "message": "test task not found"})
		return
	}
	var payload channelCredentialTestTaskPayload
	if err := task.DecodePayload(&payload); err != nil || payload.ChannelID != channelID {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "message": "test task not found"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": task.ToResponse()})
}

func CancelMultiKeyTestTask(c *gin.Context) {
	channelID, err := strconv.Atoi(c.Param("id"))
	if err != nil || channelID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "invalid channel id"})
		return
	}
	task, err := model.GetSystemTaskByTaskID(c.Param("task_id"))
	if err != nil {
		common.ApiError(c, err)
		return
	}
	if task == nil || task.Type != model.SystemTaskTypeChannelCredentialTest {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "message": "test task not found"})
		return
	}
	var payload channelCredentialTestTaskPayload
	if err := task.DecodePayload(&payload); err != nil || payload.ChannelID != channelID {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "message": "test task not found"})
		return
	}
	if err := model.RequestSystemTaskCancel(task.TaskID); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusConflict, gin.H{"success": false, "message": "test task is no longer active"})
			return
		}
		common.ApiError(c, err)
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"success": true, "message": "test task cancellation requested"})
}

func writeChannelCredentialError(c *gin.Context, err error) {
	status := http.StatusBadRequest
	if errors.Is(err, model.ErrChannelCredentialRevisionConflict) {
		status = http.StatusConflict
	}
	if errors.Is(err, model.ErrChannelCredentialNotFound) {
		status = http.StatusNotFound
	}
	c.JSON(status, gin.H{"success": false, "message": err.Error()})
}

// GetChannelModelObservability returns the paginated channel/model contract.
// The legacy /api/channel/observability handler below intentionally keeps its
// original array-shaped response for existing admin clients.
func GetChannelModelObservability(c *gin.Context) {
	query, err := parseObservabilityQuery(c, true)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": err.Error()})
		return
	}
	page, err := channelobservability.QueryMetricsPage(query)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": gin.H{
		"items": page.Items, "total": page.Total, "page": page.Page, "page_size": page.PageSize,
		"total_pages": page.TotalPages, "start": query.StartTs, "end": query.EndTs,
	}})
}

func GetChannelAvailability(c *gin.Context) {
	channelIds, err := parseObservationChannelIds(c.Query("channel_ids"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": err.Error()})
		return
	}
	hours, err := parseObservationInt(c.Query("hours"), 24, 24*30, true)
	if err != nil || hours <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "invalid hours: must be between 1 and 720"})
		return
	}
	bucketCount, err := parseObservationInt(c.Query("bucket_count"), 24, 96, true)
	if err != nil || bucketCount <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "invalid bucket_count: must be between 1 and 96"})
		return
	}
	now := time.Now().Unix()
	startTs, err := parseObservationTime(c.Query("start"), now-int64(hours)*3600, true)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "invalid start: " + err.Error()})
		return
	}
	endTs, err := parseObservationTime(c.Query("end"), now, true)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "invalid end: " + err.Error()})
		return
	}
	if startTs > endTs || endTs-startTs > int64(30*24*3600) {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "invalid observation range"})
		return
	}
	series, err := channelobservability.QueryAvailabilitySeries(channelobservability.AvailabilityQuery{
		StartTs: startTs, EndTs: endTs, Hours: hours, ChannelIds: channelIds, BucketCount: bucketCount,
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": gin.H{
		"items": series, "start": startTs, "end": endTs, "bucket_count": bucketCount,
	}})
}

func parseObservationChannelIds(raw string) ([]int, error) {
	const maxChannelIds = 200
	parts := strings.Split(raw, ",")
	result := make([]int, 0, len(parts))
	seen := make(map[int]struct{}, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		value, err := strconv.Atoi(part)
		if err != nil || value <= 0 || value > 1<<30 {
			return nil, fmt.Errorf("invalid channel_ids")
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
		if len(result) > maxChannelIds {
			return nil, fmt.Errorf("channel_ids cannot contain more than %d values", maxChannelIds)
		}
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("channel_ids is required")
	}
	return result, nil
}

// GetLegacyChannelModelObservability preserves the pre-pagination response
// consumed by the existing channel drawer.
func GetLegacyChannelModelObservability(c *gin.Context) {
	now := time.Now().Unix()
	query := channelobservability.Query{
		Hours:          parseBoundedInt(c.Query("hours"), 24, 1, 24*30),
		ChannelId:      parseBoundedInt(c.Query("channel_id"), 0, 0, 1<<30),
		CredentialId:   parseBoundedInt(c.Query("credential_id"), 0, 0, 1<<30),
		RequestedModel: strings.TrimSpace(c.Query("model")),
		Group:          strings.TrimSpace(c.Query("group")),
		Protocol:       strings.TrimSpace(c.Query("protocol")),
	}
	query.StartTs = parseLegacyUnixBounded(c.Query("start"), now-int64(query.Hours)*3600, now-30*24*3600, now)
	query.EndTs = parseLegacyUnixBounded(c.Query("end"), now, query.StartTs, now)
	results, err := channelobservability.QueryMetrics(query)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": results})
}

func parseObservabilityQuery(c *gin.Context, strict bool) (channelobservability.Query, error) {
	now := time.Now().Unix()
	query := channelobservability.Query{
		ChannelId:      0,
		CredentialId:   0,
		RequestedModel: strings.TrimSpace(c.Query("model")),
		Group:          strings.TrimSpace(c.Query("group")),
		Protocol:       strings.TrimSpace(c.Query("protocol")),
		Page:           1,
		PageSize:       50,
		SortBy:         strings.TrimSpace(c.Query("sort_by")),
		SortOrder:      strings.ToLower(strings.TrimSpace(c.Query("sort_order"))),
	}
	if raw := strings.TrimSpace(c.Query("aggregate_by_model")); raw != "" {
		value, parseErr := strconv.ParseBool(raw)
		if parseErr != nil {
			return query, fmt.Errorf("invalid aggregate_by_model: must be true or false")
		}
		query.AggregateByModel = value
	}
	if query.SortOrder == "" {
		query.SortOrder = "desc"
	}
	var err error
	if query.ChannelId, err = parseObservationInt(c.Query("channel_id"), 0, 1<<30, strict); err != nil {
		return query, fmt.Errorf("invalid channel_id: %w", err)
	}
	if query.CredentialId, err = parseObservationInt(c.Query("credential_id"), 0, 1<<30, strict); err != nil {
		return query, fmt.Errorf("invalid credential_id: %w", err)
	}
	if query.Page, err = parseObservationInt(c.Query("page"), 1, 100000, strict); err != nil {
		return query, fmt.Errorf("invalid page: %w", err)
	}
	if query.PageSize, err = parseObservationInt(c.Query("page_size"), 50, 200, strict); err != nil {
		return query, fmt.Errorf("invalid page_size: %w", err)
	}
	if query.SortOrder != "asc" && query.SortOrder != "desc" {
		return query, fmt.Errorf("sort_order must be asc or desc")
	}
	if !validObservabilitySort(query.SortBy) {
		return query, fmt.Errorf("unsupported sort_by")
	}
	hours, hoursErr := parseObservationInt(c.Query("hours"), 24, 24*30, strict)
	if hoursErr != nil {
		return query, fmt.Errorf("invalid hours: %w", hoursErr)
	}
	query.StartTs, err = parseObservationTime(c.Query("start"), now-int64(hours)*3600, strict)
	if err != nil {
		return query, fmt.Errorf("invalid start: %w", err)
	}
	query.EndTs, err = parseObservationTime(c.Query("end"), now, strict)
	if err != nil {
		return query, fmt.Errorf("invalid end: %w", err)
	}
	if query.StartTs > query.EndTs {
		return query, fmt.Errorf("start must not be after end")
	}
	if query.EndTs-query.StartTs > int64(30*24*3600) {
		return query, fmt.Errorf("observation range cannot exceed 30 days")
	}
	return query, nil
}

func validObservabilitySort(value string) bool {
	switch value {
	case "", "request_count", "attempt_count", "request_success_rate", "attempt_success_rate", "cache_hit_rate", "cache_token_rate", "avg_latency_ms", "p95_latency_ms", "avg_ttft_ms", "p95_ttft_ms", "channel_id", "model", "requested_model":
		return true
	default:
		return false
	}
}

func parseObservationInt(raw string, fallback, max int, strict bool) (int, error) {
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 0 || value > max {
		if strict {
			return fallback, fmt.Errorf("must be between 0 and %d", max)
		}
		return fallback, nil
	}
	return value, nil
}

func parseObservationTime(raw string, fallback int64, strict bool) (int64, error) {
	if raw == "" {
		return fallback, nil
	}
	if value, err := strconv.ParseInt(raw, 10, 64); err == nil && value >= 0 {
		return value, nil
	}
	if value, err := time.Parse(time.RFC3339, raw); err == nil {
		return value.Unix(), nil
	}
	if strict {
		return fallback, fmt.Errorf("must be unix seconds or RFC3339")
	}
	return fallback, nil
}

func parseLegacyUnixBounded(raw string, fallback, min, max int64) int64 {
	if raw == "" {
		return fallback
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value < min {
		return fallback
	}
	if value > max {
		return max
	}
	return value
}

func parseBoundedInt(raw string, fallback, min, max int) int {
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < min {
		return fallback
	}
	if value > max {
		return max
	}
	return value
}
