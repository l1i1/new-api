package channelobservability

import (
	"context"
	"encoding/base64"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/go-redis/redis/v8"
)

const bucketSeconds int64 = 300

const (
	defaultQueryHours            = 24
	maxQueryHours                = 24 * 30
	defaultPageSize              = 50
	maxPageSize                  = 200
	MinimumReliableSamples int64 = 20
	errorClassRedisPrefix        = "error_class:"
)

type Usage struct {
	InputTokens      int64
	CacheReadTokens  int64
	CacheWriteTokens int64
	Observable       bool
}

type Observation struct {
	ChannelId      int
	CredentialId   int
	RequestedModel string
	UpstreamModel  string
	Group          string
	Protocol       string
	RetryIndex     int
	LatencyMs      int64
	TtftMs         int64
	FRTMs          int64
	Success        bool
	Usage          Usage
	ErrorClass     string
}

type bucketKey struct {
	bucketTs       int64
	channelId      int
	credentialId   int
	requestedModel string
	upstreamModel  string
	group          string
	protocol       string
}

type atomicBucket struct {
	mu    sync.Mutex
	value model.ChannelModelPerfAggregate
}

var hotBuckets sync.Map

func Init() {
	go flushLoop()
}

func RecordAttempt(info *relaycommon.RelayInfo, success bool, errorClass string) {
	if info == nil || info.IsChannelTest || info.OriginModelName == "" || info.ChannelMeta == nil {
		return
	}
	start := info.AttemptStartTime
	if start.IsZero() {
		start = info.StartTime
	}
	end := time.Now()
	sample := Observation{
		ChannelId: info.ChannelId, CredentialId: info.ChannelCredentialId,
		RequestedModel: info.OriginModelName, UpstreamModel: info.UpstreamModelName,
		Group: info.UsingGroup, Protocol: string(info.RelayFormat), RetryIndex: info.RetryIndex,
		LatencyMs: end.Sub(start).Milliseconds(), Success: success, ErrorClass: normalizeErrorClass(errorClass, success),
	}
	if !info.AttemptFirstDownstreamWriteTime.IsZero() {
		sample.TtftMs = info.AttemptFirstDownstreamWriteTime.Sub(start).Milliseconds()
	}
	if !info.AttemptFirstResponseTime.IsZero() {
		sample.FRTMs = info.AttemptFirstResponseTime.Sub(start).Milliseconds()
	}
	record(sample, false)
}

func normalizeErrorClass(raw string, success bool) string {
	if success {
		return ""
	}
	value := strings.ToLower(strings.TrimSpace(raw))
	if value == "" {
		return "unknown"
	}
	classification := func(class string) string {
		if strings.Contains(value, class) {
			return class
		}
		return ""
	}
	for _, class := range []string{"authentication", "unauthorized", "invalid_key", "rate_limited", "rate_limit", "too_many", "proxy_network", "proxy", "dial", "timeout", "deadline", "model_access", "model", "parse", "response_body", "upstream"} {
		if matched := classification(class); matched != "" {
			switch matched {
			case "unauthorized", "invalid_key":
				return "authentication"
			case "rate_limit", "too_many":
				return "rate_limited"
			case "proxy", "dial":
				return "proxy_network"
			case "deadline":
				return "timeout"
			case "model":
				return "model_access"
			case "parse", "response_body":
				return "parse"
			default:
				return matched
			}
		}
	}
	if len(value) > 64 {
		return value[:64]
	}
	return value
}

func RecordRequest(info *relaycommon.RelayInfo, success bool, usage Usage) {
	if info == nil || info.IsChannelTest || info.OriginModelName == "" {
		return
	}
	start := info.StartTime
	if start.IsZero() {
		start = time.Now()
	}
	sample := Observation{
		ChannelId: info.ChannelId, CredentialId: info.ChannelCredentialId,
		RequestedModel: info.OriginModelName, UpstreamModel: info.UpstreamModelName,
		Group: info.UsingGroup, Protocol: string(info.RelayFormat),
		LatencyMs: time.Since(start).Milliseconds(), Success: success, Usage: usage,
	}
	if !info.FirstDownstreamWriteTime.IsZero() {
		sample.TtftMs = info.FirstDownstreamWriteTime.Sub(start).Milliseconds()
	}
	record(sample, true)
}

func record(sample Observation, request bool) {
	if sample.ChannelId <= 0 || sample.RequestedModel == "" {
		return
	}
	if sample.LatencyMs < 0 {
		sample.LatencyMs = 0
	}
	key := bucketKey{bucketTs: bucketStart(time.Now().Unix()), channelId: sample.ChannelId, credentialId: sample.CredentialId,
		requestedModel: sample.RequestedModel, upstreamModel: sample.UpstreamModel, group: sample.Group, protocol: sample.Protocol}
	actual, _ := hotBuckets.LoadOrStore(key, &atomicBucket{value: model.ChannelModelPerfAggregate{
		ChannelId: key.channelId, CredentialId: key.credentialId, RequestedModel: key.requestedModel, UpstreamModel: key.upstreamModel,
		Group: key.group, Protocol: key.protocol, LatencyHistogram: model.NewObservationHistogram(),
		RequestLatencyHistogram: model.NewObservationHistogram(), TtftHistogram: model.NewObservationHistogram(), FRTHistogram: model.NewObservationHistogram(), ErrorCounts: map[string]int64{},
	}})
	bucket := actual.(*atomicBucket)
	bucket.mu.Lock()
	value := &bucket.value
	if sample.UpstreamModel != "" && value.UpstreamModel == "" {
		value.UpstreamModel = sample.UpstreamModel
		value.UpstreamModels = []string{sample.UpstreamModel}
	}
	if !request && !sample.Success && sample.ErrorClass != "" {
		if value.ErrorCounts == nil {
			value.ErrorCounts = map[string]int64{}
		}
		value.ErrorCounts[sample.ErrorClass]++
	}
	if request {
		value.RequestCount++
		if sample.Success {
			value.RequestSuccessCount++
		}
		value.RequestLatencySumMs += sample.LatencyMs
		value.RequestLatencyCount++
		value.RequestLatencyHistogram.Add(sample.LatencyMs)
		value.SampleCount++
		if sample.TtftMs > 0 {
			value.TtftSumMs += sample.TtftMs
			value.TtftCount++
			value.TtftHistogram.Add(sample.TtftMs)
		}
	} else {
		value.AttemptCount++
		if sample.Success {
			value.AttemptSuccessCount++
		}
		value.LatencySumMs += sample.LatencyMs
		value.LatencyCount++
		value.LatencyHistogram.Add(sample.LatencyMs)
		if sample.TtftMs > 0 {
			value.TtftSumMs += sample.TtftMs
			value.TtftCount++
			value.TtftHistogram.Add(sample.TtftMs)
		}
		if sample.FRTMs > 0 {
			value.FRTSumMs += sample.FRTMs
			value.FRTCount++
			value.FRTHistogram.Add(sample.FRTMs)
		}
	}
	if sample.Usage.Observable {
		value.UsageCount++
		value.CacheObservableCount++
		value.InputTokens += sample.Usage.InputTokens
		value.CacheReadTokens += sample.Usage.CacheReadTokens
		value.CacheWriteTokens += sample.Usage.CacheWriteTokens
		if sample.Usage.CacheReadTokens > 0 {
			value.CacheHitCount++
		}
	}
	bucket.mu.Unlock()
	recordRedis(key, sample, request)
}

func bucketStart(ts int64) int64 { return ts - ts%bucketSeconds }

func flushLoop() {
	for {
		time.Sleep(time.Minute)
		flushCompletedBuckets()
	}
}

func flushCompletedBuckets() {
	current := bucketStart(time.Now().Unix())
	hotBuckets.Range(func(rawKey, rawValue any) bool {
		key := rawKey.(bucketKey)
		if key.bucketTs >= current {
			return true
		}
		bucket := rawValue.(*atomicBucket)
		bucket.mu.Lock()
		value := bucket.value
		bucket.value = model.ChannelModelPerfAggregate{ChannelId: key.channelId, CredentialId: key.credentialId, RequestedModel: key.requestedModel, UpstreamModel: key.upstreamModel, UpstreamModels: []string{key.upstreamModel}, Group: key.group, Protocol: key.protocol,
			LatencyHistogram: model.NewObservationHistogram(), RequestLatencyHistogram: model.NewObservationHistogram(), TtftHistogram: model.NewObservationHistogram(), FRTHistogram: model.NewObservationHistogram(), ErrorCounts: map[string]int64{}}
		bucket.mu.Unlock()
		metric, err := model.BuildChannelModelPerfMetric(value, common.NodeName, key.bucketTs)
		if err == nil && metric.AttemptCount+metric.RequestCount > 0 {
			if err = model.DB.Create(&metric).Error; err != nil {
				bucket.mu.Lock()
				// Samples may have arrived while the database write was in
				// progress. Merge them back instead of overwriting them.
				currentValue := bucket.value
				merged := mergeAggregate([]model.ChannelModelPerfAggregate{value}, currentValue)
				if len(merged) == 1 {
					bucket.value = merged[0]
				} else {
					bucket.value = currentValue
				}
				bucket.mu.Unlock()
				common.SysError(fmt.Sprintf("failed to flush channel observation bucket: %v", err))
				return true
			}
		}
		if key.bucketTs < bucketStart(time.Now().Add(-24*time.Hour).Unix()) {
			hotBuckets.Delete(rawKey)
		}
		return true
	})
}

type Query struct {
	StartTs        int64
	EndTs          int64
	Hours          int
	ChannelId      int
	ChannelIds     []int
	CredentialId   int
	RequestedModel string
	Group          string
	Protocol       string
	Page           int
	PageSize       int
	SortBy         string
	SortOrder      string
}

type Result struct {
	ChannelId           int              `json:"channel_id"`
	CredentialId        int              `json:"credential_id"`
	RequestedModel      string           `json:"requested_model"`
	UpstreamModel       string           `json:"upstream_model,omitempty"`
	UpstreamModels      []string         `json:"upstream_models,omitempty"`
	Group               string           `json:"group"`
	Protocol            string           `json:"protocol"`
	RequestCount        int64            `json:"request_count"`
	RequestSuccessCount int64            `json:"request_success_count"`
	RequestFailureCount int64            `json:"request_failure_count"`
	AttemptCount        int64            `json:"attempt_count"`
	RequestSuccessRate  float64          `json:"request_success_rate"`
	AttemptSuccessRate  float64          `json:"attempt_success_rate"`
	CacheHitRate        float64          `json:"cache_hit_rate"`
	CacheTokenRate      float64          `json:"cache_token_rate"`
	AvgLatencyMs        int64            `json:"avg_latency_ms"`
	P95LatencyMs        int64            `json:"p95_latency_ms"`
	AvgRequestLatencyMs int64            `json:"avg_request_latency_ms"`
	P95RequestLatencyMs int64            `json:"p95_request_latency_ms"`
	AvgTtftMs           int64            `json:"avg_ttft_ms"`
	P95TtftMs           int64            `json:"p95_ttft_ms"`
	AvgFRTMs            int64            `json:"avg_upstream_frt_ms"`
	P95FRTMs            int64            `json:"p95_upstream_frt_ms"`
	SampleCoverage      float64          `json:"sample_coverage"`
	UsageCoverage       float64          `json:"usage_coverage"`
	SampleCount         int64            `json:"sample_count"`
	SampleSufficient    bool             `json:"sample_sufficient"`
	SampleInsufficient  bool             `json:"sample_insufficient"`
	SampleStatus        string           `json:"sample_status"`
	UsageSufficient     bool             `json:"usage_sufficient"`
	ErrorTrends         map[string]int64 `json:"error_trends"`
}

type PageResult struct {
	Items      []Result `json:"items"`
	Total      int      `json:"total"`
	Page       int      `json:"page"`
	PageSize   int      `json:"page_size"`
	TotalPages int      `json:"total_pages"`
}

type AvailabilityQuery struct {
	StartTs     int64
	EndTs       int64
	Hours       int
	ChannelIds  []int
	BucketCount int
}

type AvailabilityPoint struct {
	BucketStart         int64   `json:"bucket_start"`
	BucketEnd           int64   `json:"bucket_end"`
	RequestCount        int64   `json:"request_count"`
	RequestSuccessCount int64   `json:"request_success_count"`
	RequestFailureCount int64   `json:"request_failure_count"`
	RequestSuccessRate  float64 `json:"request_success_rate"`
	AvgLatencyMs        int64   `json:"avg_latency_ms"`
	latencySumMs        int64
	latencyCount        int64
}

type AvailabilitySeries struct {
	ChannelId int                 `json:"channel_id"`
	Points    []AvailabilityPoint `json:"points"`
}

func QueryMetrics(query Query) ([]Result, error) {
	query.Page = 0
	query.PageSize = 0
	page, err := queryMetricsPage(query)
	if err != nil {
		return nil, err
	}
	return page.Items, nil
}

func QueryMetricsPage(query Query) (PageResult, error) {
	if query.Page <= 0 {
		query.Page = 1
	}
	if query.PageSize <= 0 {
		query.PageSize = defaultPageSize
	}
	if query.PageSize > maxPageSize {
		query.PageSize = maxPageSize
	}
	return queryMetricsPage(query)
}

func queryMetricsPage(query Query) (PageResult, error) {
	startTs, endTs, err := queryRange(query)
	if err != nil {
		return PageResult{}, err
	}
	aggregates, err := model.GetChannelModelPerfMetrics(model.ChannelModelPerfQuery{StartTs: startTs, EndTs: endTs, Hours: query.Hours, ChannelId: query.ChannelId, ChannelIds: query.ChannelIds, CredentialId: query.CredentialId, RequestedModel: query.RequestedModel, Group: query.Group, Protocol: query.Protocol})
	if err != nil {
		return PageResult{}, err
	}
	for _, value := range activeAggregates(query, startTs, endTs) {
		aggregates = mergeAggregate(aggregates, value)
	}
	results := make([]Result, 0, len(aggregates))
	for _, value := range aggregates {
		results = append(results, aggregateResult(value))
	}
	sortResults(results, query.SortBy, query.SortOrder)
	total := len(results)
	if query.Page <= 0 || query.PageSize <= 0 {
		return PageResult{Items: results, Total: total, Page: 1, PageSize: total, TotalPages: pageCount(total, total)}, nil
	}
	start := (query.Page - 1) * query.PageSize
	if start > total {
		start = total
	}
	end := start + query.PageSize
	if end > total {
		end = total
	}
	return PageResult{Items: results[start:end], Total: total, Page: query.Page, PageSize: query.PageSize, TotalPages: pageCount(total, query.PageSize)}, nil
}

func QueryAvailabilitySeries(query AvailabilityQuery) ([]AvailabilitySeries, error) {
	startTs, endTs, err := queryRange(Query{StartTs: query.StartTs, EndTs: query.EndTs, Hours: query.Hours})
	if err != nil {
		return nil, err
	}
	channelIds := uniquePositiveInts(query.ChannelIds)
	if len(channelIds) == 0 {
		return []AvailabilitySeries{}, nil
	}
	bucketCount := query.BucketCount
	if bucketCount <= 0 {
		bucketCount = 24
	}
	if bucketCount > 96 {
		bucketCount = 96
	}
	bucketSize := (endTs - startTs) / int64(bucketCount)
	if bucketSize <= 0 {
		bucketSize = 1
	}
	points := make(map[int][]AvailabilityPoint, len(channelIds))
	for _, channelId := range channelIds {
		points[channelId] = make([]AvailabilityPoint, bucketCount)
		for index := range points[channelId] {
			bucketStart := startTs + int64(index)*bucketSize
			bucketEnd := bucketStart + bucketSize
			if index == bucketCount-1 || bucketEnd > endTs {
				bucketEnd = endTs
			}
			points[channelId][index] = AvailabilityPoint{BucketStart: bucketStart, BucketEnd: bucketEnd}
		}
	}

	rows, err := model.GetChannelModelPerfMetricRows(model.ChannelModelPerfQuery{StartTs: startTs, EndTs: endTs, Hours: query.Hours, ChannelIds: channelIds})
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		addAvailabilityMetric(points, channelIds, row.BucketTs, row.ChannelId, row.RequestCount, row.RequestSuccessCount, row.RequestLatencySumMs, row.RequestLatencyCount, startTs, bucketSize, bucketCount)
	}
	activeQuery := Query{StartTs: startTs, EndTs: endTs, ChannelIds: channelIds}
	for _, aggregate := range activeAggregates(activeQuery, startTs, endTs) {
		addAvailabilityMetric(points, channelIds, bucketStart(time.Now().Unix()), aggregate.ChannelId, aggregate.RequestCount, aggregate.RequestSuccessCount, aggregate.RequestLatencySumMs, aggregate.RequestLatencyCount, startTs, bucketSize, bucketCount)
	}
	result := make([]AvailabilitySeries, 0, len(channelIds))
	for _, channelId := range channelIds {
		for index := range points[channelId] {
			point := &points[channelId][index]
			point.RequestFailureCount = nonNegative(point.RequestCount - point.RequestSuccessCount)
			point.RequestSuccessRate = percentage(point.RequestSuccessCount, point.RequestCount)
		}
		result = append(result, AvailabilitySeries{ChannelId: channelId, Points: points[channelId]})
	}
	return result, nil
}

func addAvailabilityMetric(points map[int][]AvailabilityPoint, channelIds []int, bucketTs int64, channelId int, requestCount, successCount, latencySum, latencyCount int64, startTs, bucketSize int64, bucketCount int) {
	if !containsInt(channelIds, channelId) || bucketTs < startTs {
		return
	}
	index := int((bucketTs - startTs) / bucketSize)
	if index < 0 {
		return
	}
	if index >= bucketCount {
		index = bucketCount - 1
	}
	point := &points[channelId][index]
	point.RequestCount += nonNegative(requestCount)
	point.RequestSuccessCount += nonNegative(successCount)
	if latencyCount > 0 {
		point.latencySumMs += latencySum
		point.latencyCount += latencyCount
		point.AvgLatencyMs = average(point.latencySumMs, point.latencyCount)
	}
}

func nonNegative(value int64) int64 {
	if value < 0 {
		return 0
	}
	return value
}

func uniquePositiveInts(values []int) []int {
	seen := make(map[int]struct{}, len(values))
	result := make([]int, 0, len(values))
	for _, value := range values {
		if value <= 0 {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func containsInt(values []int, target int) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func queryRange(query Query) (int64, int64, error) {
	endTs := query.EndTs
	if endTs <= 0 {
		endTs = time.Now().Unix()
	}
	startTs := query.StartTs
	if startTs <= 0 {
		hours := query.Hours
		if hours <= 0 {
			hours = defaultQueryHours
		}
		if hours > maxQueryHours {
			hours = maxQueryHours
		}
		startTs = endTs - int64(hours)*3600
	}
	if startTs > endTs {
		return 0, 0, fmt.Errorf("start must not be after end")
	}
	return startTs, endTs, nil
}

func pageCount(total, pageSize int) int {
	if total == 0 {
		return 1
	}
	if pageSize <= 0 {
		return 0
	}
	return (total + pageSize - 1) / pageSize
}

func sortResults(results []Result, sortBy, sortOrder string) {
	if sortBy == "" {
		sortBy = "attempt_count"
	}
	ascending := strings.EqualFold(sortOrder, "asc")
	less := func(left, right Result) bool {
		switch sortBy {
		case "request_count":
			return left.RequestCount < right.RequestCount
		case "request_success_rate":
			return left.RequestSuccessRate < right.RequestSuccessRate
		case "attempt_success_rate":
			return left.AttemptSuccessRate < right.AttemptSuccessRate
		case "cache_hit_rate":
			return left.CacheHitRate < right.CacheHitRate
		case "cache_token_rate":
			return left.CacheTokenRate < right.CacheTokenRate
		case "avg_latency_ms":
			return left.AvgLatencyMs < right.AvgLatencyMs
		case "p95_latency_ms":
			return left.P95LatencyMs < right.P95LatencyMs
		case "avg_ttft_ms":
			return left.AvgTtftMs < right.AvgTtftMs
		case "p95_ttft_ms":
			return left.P95TtftMs < right.P95TtftMs
		case "channel_id":
			return left.ChannelId < right.ChannelId
		case "model", "requested_model":
			return left.RequestedModel < right.RequestedModel
		default:
			return left.AttemptCount < right.AttemptCount
		}
	}
	sort.SliceStable(results, func(i, j int) bool {
		if less(results[i], results[j]) {
			return ascending
		}
		if less(results[j], results[i]) {
			return !ascending
		}
		if results[i].ChannelId != results[j].ChannelId {
			return results[i].ChannelId < results[j].ChannelId
		}
		return results[i].RequestedModel < results[j].RequestedModel
	})
}

func mergeAggregate(target []model.ChannelModelPerfAggregate, value model.ChannelModelPerfAggregate) []model.ChannelModelPerfAggregate {
	for i := range target {
		if target[i].ChannelId == value.ChannelId && target[i].CredentialId == value.CredentialId && target[i].RequestedModel == value.RequestedModel && target[i].UpstreamModel == value.UpstreamModel && target[i].Group == value.Group && target[i].Protocol == value.Protocol {
			target[i].RequestCount += value.RequestCount
			target[i].RequestSuccessCount += value.RequestSuccessCount
			target[i].AttemptCount += value.AttemptCount
			target[i].AttemptSuccessCount += value.AttemptSuccessCount
			target[i].CacheObservableCount += value.CacheObservableCount
			target[i].CacheHitCount += value.CacheHitCount
			target[i].InputTokens += value.InputTokens
			target[i].CacheReadTokens += value.CacheReadTokens
			target[i].CacheWriteTokens += value.CacheWriteTokens
			target[i].LatencySumMs += value.LatencySumMs
			target[i].RequestLatencySumMs += value.RequestLatencySumMs
			target[i].TtftSumMs += value.TtftSumMs
			target[i].FRTSumMs += value.FRTSumMs
			target[i].LatencyCount += value.LatencyCount
			target[i].RequestLatencyCount += value.RequestLatencyCount
			target[i].TtftCount += value.TtftCount
			target[i].FRTCount += value.FRTCount
			target[i].SampleCount += value.SampleCount
			target[i].UsageCount += value.UsageCount
			if target[i].ErrorCounts == nil {
				target[i].ErrorCounts = map[string]int64{}
			}
			for class, count := range value.ErrorCounts {
				target[i].ErrorCounts[class] += count
			}
			target[i].LatencyHistogram.Merge(value.LatencyHistogram)
			target[i].RequestLatencyHistogram.Merge(value.RequestLatencyHistogram)
			target[i].TtftHistogram.Merge(value.TtftHistogram)
			target[i].FRTHistogram.Merge(value.FRTHistogram)
			if target[i].UpstreamModel == "" {
				target[i].UpstreamModel = value.UpstreamModel
			}
			if target[i].UpstreamModel == "" {
				for _, upstream := range value.UpstreamModels {
					if upstream == "" {
						continue
					}
					found := false
					for _, existing := range target[i].UpstreamModels {
						if existing == upstream {
							found = true
							break
						}
					}
					if !found {
						target[i].UpstreamModels = append(target[i].UpstreamModels, upstream)
					}
				}
				sort.Strings(target[i].UpstreamModels)
			}
			return target
		}
	}
	target = append(target, value)
	return target
}

func aggregateResult(value model.ChannelModelPerfAggregate) Result {
	result := Result{ChannelId: value.ChannelId, CredentialId: value.CredentialId, RequestedModel: value.RequestedModel, UpstreamModel: value.UpstreamModel, UpstreamModels: value.UpstreamModels, Group: value.Group, Protocol: value.Protocol,
		RequestCount: value.RequestCount, RequestSuccessCount: value.RequestSuccessCount, RequestFailureCount: nonNegative(value.RequestCount - value.RequestSuccessCount), AttemptCount: value.AttemptCount,
		AvgLatencyMs: average(value.LatencySumMs, value.LatencyCount), P95LatencyMs: value.LatencyHistogram.P95(),
		AvgRequestLatencyMs: average(value.RequestLatencySumMs, value.RequestLatencyCount), P95RequestLatencyMs: value.RequestLatencyHistogram.P95(),
		AvgTtftMs: average(value.TtftSumMs, value.TtftCount), P95TtftMs: value.TtftHistogram.P95(), AvgFRTMs: average(value.FRTSumMs, value.FRTCount), P95FRTMs: value.FRTHistogram.P95(),
		UsageCoverage: percentage(value.UsageCount, value.RequestCount), SampleCoverage: percentage(value.SampleCount, value.RequestCount),
		SampleCount: value.SampleCount, SampleSufficient: value.SampleCount >= MinimumReliableSamples,
		SampleInsufficient: value.SampleCount < MinimumReliableSamples,
		UsageSufficient:    value.UsageCount >= MinimumReliableSamples, SampleStatus: sampleStatus(value.SampleCount),
		ErrorTrends: copyErrorCounts(value.ErrorCounts),
	}
	result.RequestSuccessRate = percentage(value.RequestSuccessCount, value.RequestCount)
	result.AttemptSuccessRate = percentage(value.AttemptSuccessCount, value.AttemptCount)
	result.CacheHitRate = percentage(value.CacheHitCount, value.CacheObservableCount)
	result.CacheTokenRate = percentage(value.CacheReadTokens, value.InputTokens)
	return result
}

func sampleStatus(sampleCount int64) string {
	if sampleCount < MinimumReliableSamples {
		return "insufficient"
	}
	return "sufficient"
}

func copyErrorCounts(source map[string]int64) map[string]int64 {
	result := make(map[string]int64, len(source))
	for class, count := range source {
		if class != "" && count > 0 {
			result[class] = count
		}
	}
	return result
}

func average(sum, count int64) int64 {
	if count == 0 {
		return 0
	}
	return sum / count
}
func percentage(numerator, denominator int64) float64 {
	if denominator == 0 {
		return 0
	}
	return float64(numerator) / float64(denominator) * 100
}

func activeAggregates(query Query, startTs, endTs int64) []model.ChannelModelPerfAggregate {
	currentBucket := bucketStart(time.Now().Unix())
	if !common.RedisEnabled || common.RDB == nil {
		return localActiveAggregates(query, startTs, endTs)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	keys, err := common.RDB.SMembers(ctx, redisIndexKey()).Result()
	if err != nil {
		return localActiveAggregates(query, startTs, endTs)
	}
	result := make([]model.ChannelModelPerfAggregate, 0)
	for _, redisKey := range keys {
		key, ok := decodeRedisKey(redisKey)
		if !ok || key.bucketTs != currentBucket || key.bucketTs < startTs || key.bucketTs > endTs || !matches(query, key) {
			continue
		}
		values, err := common.RDB.HGetAll(ctx, redisKey).Result()
		if err != nil {
			continue
		}
		result = append(result, redisValueToAggregate(key, values))
	}
	return result
}

func localActiveAggregates(query Query, startTs, endTs int64) []model.ChannelModelPerfAggregate {
	currentBucket := bucketStart(time.Now().Unix())
	result := make([]model.ChannelModelPerfAggregate, 0)
	hotBuckets.Range(func(rawKey, rawValue any) bool {
		key := rawKey.(bucketKey)
		if key.bucketTs != currentBucket || key.bucketTs < startTs || key.bucketTs > endTs || !matches(query, key) {
			return true
		}
		bucket := rawValue.(*atomicBucket)
		bucket.mu.Lock()
		value := bucket.value
		bucket.mu.Unlock()
		result = append(result, value)
		return true
	})
	return result
}

func matches(query Query, key bucketKey) bool {
	return (query.ChannelId <= 0 || query.ChannelId == key.channelId) && (len(query.ChannelIds) == 0 || containsInt(query.ChannelIds, key.channelId)) && (query.CredentialId <= 0 || query.CredentialId == key.credentialId) && (query.RequestedModel == "" || query.RequestedModel == key.requestedModel) && (query.Group == "" || query.Group == key.group) && (query.Protocol == "" || query.Protocol == key.protocol)
}

func redisIndexKey() string { return "channel-observation:v1:index" }
func encodeRedisKey(key bucketKey) string {
	fields := []string{strconv.FormatInt(key.bucketTs, 10), strconv.Itoa(key.channelId), strconv.Itoa(key.credentialId), key.requestedModel, key.upstreamModel, key.group, key.protocol}
	encoded := make([]string, len(fields))
	for i, field := range fields {
		encoded[i] = base64.RawURLEncoding.EncodeToString([]byte(field))
	}
	return "channel-observation:v1:" + strings.Join(encoded, ".")
}
func decodeRedisKey(redisKey string) (bucketKey, bool) {
	parts := strings.Split(redisKey, ":")
	if len(parts) != 3 {
		return bucketKey{}, false
	}
	encodedFields := strings.Split(parts[2], ".")
	if len(encodedFields) != 7 {
		return bucketKey{}, false
	}
	fields := make([]string, len(encodedFields))
	for i, encoded := range encodedFields {
		decoded, err := base64.RawURLEncoding.DecodeString(encoded)
		if err != nil {
			return bucketKey{}, false
		}
		fields[i] = string(decoded)
	}
	bucketTs, err := strconv.ParseInt(fields[0], 10, 64)
	if err != nil {
		return bucketKey{}, false
	}
	channelID, err := strconv.Atoi(fields[1])
	if err != nil {
		return bucketKey{}, false
	}
	credentialID, err := strconv.Atoi(fields[2])
	if err != nil {
		return bucketKey{}, false
	}
	return bucketKey{bucketTs: bucketTs, channelId: channelID, credentialId: credentialID, requestedModel: fields[3], upstreamModel: fields[4], group: fields[5], protocol: fields[6]}, true
}

func recordRedis(key bucketKey, sample Observation, request bool) {
	if !common.RedisEnabled || common.RDB == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	redisKey := encodeRedisKey(key)
	pipe := common.RDB.TxPipeline()
	if request {
		pipe.HIncrBy(ctx, redisKey, "request", 1)
		if sample.Success {
			pipe.HIncrBy(ctx, redisKey, "request_ok", 1)
		}
		pipe.HIncrBy(ctx, redisKey, "request_latency", sample.LatencyMs)
		pipe.HIncrBy(ctx, redisKey, "request_latency_count", 1)
		incrementRedisHistogram(pipe, ctx, redisKey, "request_hist", sample.LatencyMs)
		pipe.HIncrBy(ctx, redisKey, "sample", 1)
	} else {
		pipe.HIncrBy(ctx, redisKey, "attempt", 1)
		if sample.Success {
			pipe.HIncrBy(ctx, redisKey, "attempt_ok", 1)
		}
		pipe.HIncrBy(ctx, redisKey, "latency", sample.LatencyMs)
		pipe.HIncrBy(ctx, redisKey, "latency_count", 1)
		incrementRedisHistogram(pipe, ctx, redisKey, "lat_hist", sample.LatencyMs)
		if sample.TtftMs > 0 {
			pipe.HIncrBy(ctx, redisKey, "ttft", sample.TtftMs)
			pipe.HIncrBy(ctx, redisKey, "ttft_count", 1)
			incrementRedisHistogram(pipe, ctx, redisKey, "ttft_hist", sample.TtftMs)
		}
		if sample.FRTMs > 0 {
			pipe.HIncrBy(ctx, redisKey, "frt", sample.FRTMs)
			pipe.HIncrBy(ctx, redisKey, "frt_count", 1)
			incrementRedisHistogram(pipe, ctx, redisKey, "frt_hist", sample.FRTMs)
		}
		if !sample.Success && sample.ErrorClass != "" {
			pipe.HIncrBy(ctx, redisKey, encodeRedisErrorClass(sample.ErrorClass), 1)
		}
	}
	if sample.Usage.Observable {
		pipe.HIncrBy(ctx, redisKey, "usage", 1)
		pipe.HIncrBy(ctx, redisKey, "input", sample.Usage.InputTokens)
		pipe.HIncrBy(ctx, redisKey, "cache_read", sample.Usage.CacheReadTokens)
		pipe.HIncrBy(ctx, redisKey, "cache_write", sample.Usage.CacheWriteTokens)
		pipe.HIncrBy(ctx, redisKey, "cache_observable", 1)
		if sample.Usage.CacheReadTokens > 0 {
			pipe.HIncrBy(ctx, redisKey, "cache_hit", 1)
		}
	}
	pipe.Expire(ctx, redisKey, 2*time.Hour)
	pipe.SAdd(ctx, redisIndexKey(), redisKey)
	pipe.Expire(ctx, redisIndexKey(), 2*time.Hour)
	_, _ = pipe.Exec(ctx)
}

func incrementRedisHistogram(pipe redis.Pipeliner, ctx context.Context, key, prefix string, valueMs int64) {
	if valueMs < 0 {
		return
	}
	index := len(model.ObservationHistogramBounds)
	for i, bound := range model.ObservationHistogramBounds {
		if valueMs <= bound {
			index = i
			break
		}
	}
	pipe.HIncrBy(ctx, key, fmt.Sprintf("%s_%d", prefix, index), 1)
}

func redisValueToAggregate(key bucketKey, values map[string]string) model.ChannelModelPerfAggregate {
	parse := func(name string) int64 { value, _ := strconv.ParseInt(values[name], 10, 64); return value }
	hist := func(prefix string) model.ObservationHistogram {
		result := model.NewObservationHistogram()
		for i := range result.Counts {
			result.Counts[i] = parse(fmt.Sprintf("%s_%d", prefix, i))
		}
		return result
	}
	errors := map[string]int64{}
	for field, raw := range values {
		if !strings.HasPrefix(field, errorClassRedisPrefix) {
			continue
		}
		encodedClass := strings.TrimPrefix(field, errorClassRedisPrefix)
		classBytes, err := base64.RawURLEncoding.DecodeString(encodedClass)
		if err != nil {
			continue
		}
		count, err := strconv.ParseInt(raw, 10, 64)
		if err == nil && count > 0 {
			errors[string(classBytes)] += count
		}
	}
	// Read the original fixed fields as well so Redis buckets written by an
	// older process remain visible during a rolling upgrade.
	for _, class := range []string{"authentication", "rate_limited", "proxy_network", "model_access", "upstream", "parse", "timeout", "canceled", "unknown"} {
		if count := parse("error_" + class); count > 0 {
			errors[class] += count
		}
	}
	return model.ChannelModelPerfAggregate{
		ChannelId: key.channelId, CredentialId: key.credentialId, RequestedModel: key.requestedModel,
		UpstreamModel: key.upstreamModel, Group: key.group, Protocol: key.protocol, UpstreamModels: []string{key.upstreamModel},
		RequestCount: parse("request"), RequestSuccessCount: parse("request_ok"),
		AttemptCount: parse("attempt"), AttemptSuccessCount: parse("attempt_ok"),
		CacheObservableCount: parse("cache_observable"), CacheHitCount: parse("cache_hit"),
		InputTokens: parse("input"), CacheReadTokens: parse("cache_read"), CacheWriteTokens: parse("cache_write"),
		LatencySumMs: parse("latency"), RequestLatencySumMs: parse("request_latency"),
		TtftSumMs: parse("ttft"), FRTSumMs: parse("frt"),
		LatencyCount: parse("latency_count"), RequestLatencyCount: parse("request_latency_count"),
		TtftCount: parse("ttft_count"), FRTCount: parse("frt_count"),
		LatencyHistogram: hist("lat_hist"), RequestLatencyHistogram: hist("request_hist"),
		TtftHistogram: hist("ttft_hist"), FRTHistogram: hist("frt_hist"),
		SampleCount: parse("sample"), UsageCount: parse("usage"), ErrorCounts: errors,
	}
}

func encodeRedisErrorClass(class string) string {
	return errorClassRedisPrefix + base64.RawURLEncoding.EncodeToString([]byte(class))
}
