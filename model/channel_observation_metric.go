package model

import (
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/QuantumNous/new-api/common"
)

// ObservationHistogram is a mergeable fixed-bucket latency histogram. Values
// above the last bucket are kept in Overflow so p95 remains well-defined.
type ObservationHistogram struct {
	Counts   []int64 `json:"counts"`
	Overflow int64   `json:"overflow"`
}

var ObservationHistogramBounds = []int64{10, 25, 50, 100, 250, 500, 1000, 2500, 5000, 10000, 30000, 60000}

func NewObservationHistogram() ObservationHistogram {
	return ObservationHistogram{Counts: make([]int64, len(ObservationHistogramBounds)+1)}
}

func (h *ObservationHistogram) Add(valueMs int64) {
	if h == nil || valueMs < 0 {
		return
	}
	if len(h.Counts) != len(ObservationHistogramBounds)+1 {
		h.Counts = make([]int64, len(ObservationHistogramBounds)+1)
	}
	for i, bound := range ObservationHistogramBounds {
		if valueMs <= bound {
			h.Counts[i]++
			return
		}
	}
	h.Counts[len(h.Counts)-1]++
	h.Overflow++
}

func (h *ObservationHistogram) Merge(other ObservationHistogram) {
	if h == nil {
		return
	}
	if len(h.Counts) != len(ObservationHistogramBounds)+1 {
		h.Counts = make([]int64, len(ObservationHistogramBounds)+1)
	}
	for i, count := range other.Counts {
		if i >= len(h.Counts) {
			break
		}
		h.Counts[i] += count
	}
	h.Overflow += other.Overflow
}

func (h ObservationHistogram) Count() int64 {
	var total int64
	for _, count := range h.Counts {
		total += count
	}
	return total
}

func (h ObservationHistogram) Average() int64 {
	total := h.Count()
	if total == 0 {
		return 0
	}
	var sum int64
	for i, count := range h.Counts {
		if i >= len(ObservationHistogramBounds) {
			break
		}
		sum += count * ObservationHistogramBounds[i]
	}
	if len(h.Counts) > len(ObservationHistogramBounds) {
		sum += h.Counts[len(ObservationHistogramBounds)] * ObservationHistogramBounds[len(ObservationHistogramBounds)-1]
	}
	return sum / total
}

func (h ObservationHistogram) P95() int64 {
	total := h.Count()
	if total == 0 {
		return 0
	}
	rank := (total*95 + 99) / 100
	var seen int64
	for i, count := range h.Counts {
		seen += count
		if seen >= rank {
			if i >= len(ObservationHistogramBounds) {
				return ObservationHistogramBounds[len(ObservationHistogramBounds)-1]
			}
			return ObservationHistogramBounds[i]
		}
	}
	return ObservationHistogramBounds[len(ObservationHistogramBounds)-1]
}

func marshalObservationHistogram(hist ObservationHistogram) string {
	data, err := common.Marshal(hist)
	if err != nil {
		return ""
	}
	return string(data)
}

func unmarshalObservationHistogram(raw string) ObservationHistogram {
	hist := NewObservationHistogram()
	if raw == "" {
		return hist
	}
	if err := common.Unmarshal([]byte(raw), &hist); err != nil {
		return NewObservationHistogram()
	}
	if len(hist.Counts) != len(ObservationHistogramBounds)+1 {
		return NewObservationHistogram()
	}
	return hist
}

// ChannelModelPerfMetric stores completed aggregate buckets. Rows are append
// only per process/node so all supported SQL dialects can merge histograms in
// Go without dialect-specific JSON arithmetic.
type ChannelModelPerfMetric struct {
	Id                      int    `json:"id" gorm:"primaryKey"`
	BucketTs                int64  `json:"bucket_ts" gorm:"bigint;index"`
	ChannelId               int    `json:"channel_id" gorm:"index"`
	CredentialId            int    `json:"credential_id" gorm:"index"`
	RequestedModel          string `json:"requested_model" gorm:"size:128;index"`
	UpstreamModel           string `json:"upstream_model" gorm:"size:128;index"`
	UseGroup                string `json:"group" gorm:"column:group;size:64;index"`
	Protocol                string `json:"protocol" gorm:"size:32;index"`
	NodeName                string `json:"node_name" gorm:"size:128;index"`
	RequestCount            int64  `json:"request_count"`
	RequestSuccessCount     int64  `json:"request_success_count"`
	AttemptCount            int64  `json:"attempt_count"`
	AttemptSuccessCount     int64  `json:"attempt_success_count"`
	CacheObservableCount    int64  `json:"cache_observable_count"`
	CacheHitCount           int64  `json:"cache_hit_count"`
	InputTokens             int64  `json:"input_tokens"`
	CacheReadTokens         int64  `json:"cache_read_tokens"`
	CacheWriteTokens        int64  `json:"cache_write_tokens"`
	LatencySumMs            int64  `json:"latency_sum_ms"`
	RequestLatencySumMs     int64  `json:"request_latency_sum_ms"`
	TtftSumMs               int64  `json:"ttft_sum_ms"`
	FRTSumMs                int64  `json:"frt_sum_ms"`
	LatencyCount            int64  `json:"latency_count"`
	RequestLatencyCount     int64  `json:"request_latency_count"`
	TtftCount               int64  `json:"ttft_count"`
	FRTCount                int64  `json:"frt_count"`
	LatencyHistogram        string `json:"-" gorm:"type:text"`
	RequestLatencyHistogram string `json:"-" gorm:"type:text"`
	TtftHistogram           string `json:"-" gorm:"type:text"`
	FRTHistogram            string `json:"-" gorm:"type:text"`
	SampleCount             int64  `json:"sample_count"`
	UsageCount              int64  `json:"usage_count"`
	ErrorCounts             string `json:"-" gorm:"type:text"`
}

func (ChannelModelPerfMetric) TableName() string { return "channel_model_perf_metrics" }

type ChannelModelPerfQuery struct {
	StartTs        int64
	EndTs          int64
	ChannelId      int
	CredentialId   int
	RequestedModel string
	Group          string
	Protocol       string
	Hours          int
}

type ChannelModelPerfAggregate struct {
	ChannelId      int
	CredentialId   int
	RequestedModel string
	UpstreamModel  string
	// UpstreamModels is retained for response compatibility with early local
	// builds. New aggregation keys always use UpstreamModel as a dimension.
	UpstreamModels          []string
	Group                   string
	Protocol                string
	RequestCount            int64
	RequestSuccessCount     int64
	AttemptCount            int64
	AttemptSuccessCount     int64
	CacheObservableCount    int64
	CacheHitCount           int64
	InputTokens             int64
	CacheReadTokens         int64
	CacheWriteTokens        int64
	LatencySumMs            int64
	RequestLatencySumMs     int64
	TtftSumMs               int64
	FRTSumMs                int64
	LatencyCount            int64
	RequestLatencyCount     int64
	TtftCount               int64
	FRTCount                int64
	LatencyHistogram        ObservationHistogram
	RequestLatencyHistogram ObservationHistogram
	TtftHistogram           ObservationHistogram
	FRTHistogram            ObservationHistogram
	SampleCount             int64
	UsageCount              int64
	ErrorCounts             map[string]int64
}

func normalizeErrorCounts(value map[string]int64) map[string]int64 {
	if value == nil {
		return map[string]int64{}
	}
	return value
}

func mergeErrorCounts(target map[string]int64, source map[string]int64) map[string]int64 {
	target = normalizeErrorCounts(target)
	for class, count := range source {
		if class == "" || count <= 0 {
			continue
		}
		target[class] += count
	}
	return target
}

func marshalErrorCounts(value map[string]int64) string {
	value = normalizeErrorCounts(value)
	if len(value) == 0 {
		return ""
	}
	data, err := common.Marshal(value)
	if err != nil {
		return ""
	}
	return string(data)
}

func unmarshalErrorCounts(raw string) map[string]int64 {
	result := map[string]int64{}
	if raw == "" {
		return result
	}
	if err := common.Unmarshal([]byte(raw), &result); err != nil {
		return map[string]int64{}
	}
	return result
}

func (a *ChannelModelPerfAggregate) merge(metric ChannelModelPerfMetric) {
	if a.LatencyHistogram.Counts == nil {
		a.LatencyHistogram = NewObservationHistogram()
		a.RequestLatencyHistogram = NewObservationHistogram()
		a.TtftHistogram = NewObservationHistogram()
		a.FRTHistogram = NewObservationHistogram()
	}
	a.RequestCount += metric.RequestCount
	a.RequestSuccessCount += metric.RequestSuccessCount
	a.AttemptCount += metric.AttemptCount
	a.AttemptSuccessCount += metric.AttemptSuccessCount
	a.CacheObservableCount += metric.CacheObservableCount
	a.CacheHitCount += metric.CacheHitCount
	a.InputTokens += metric.InputTokens
	a.CacheReadTokens += metric.CacheReadTokens
	a.CacheWriteTokens += metric.CacheWriteTokens
	a.LatencySumMs += metric.LatencySumMs
	a.RequestLatencySumMs += metric.RequestLatencySumMs
	a.TtftSumMs += metric.TtftSumMs
	a.FRTSumMs += metric.FRTSumMs
	a.LatencyCount += metric.LatencyCount
	a.RequestLatencyCount += metric.RequestLatencyCount
	a.TtftCount += metric.TtftCount
	a.FRTCount += metric.FRTCount
	a.SampleCount += metric.SampleCount
	a.UsageCount += metric.UsageCount
	a.ErrorCounts = mergeErrorCounts(a.ErrorCounts, unmarshalErrorCounts(metric.ErrorCounts))
	a.LatencyHistogram.Merge(unmarshalObservationHistogram(metric.LatencyHistogram))
	a.RequestLatencyHistogram.Merge(unmarshalObservationHistogram(metric.RequestLatencyHistogram))
	a.TtftHistogram.Merge(unmarshalObservationHistogram(metric.TtftHistogram))
	a.FRTHistogram.Merge(unmarshalObservationHistogram(metric.FRTHistogram))
	if metric.UpstreamModel != "" {
		a.UpstreamModel = metric.UpstreamModel
		a.UpstreamModels = []string{metric.UpstreamModel}
	}
}

func GetChannelModelPerfMetrics(query ChannelModelPerfQuery) ([]ChannelModelPerfAggregate, error) {
	if query.EndTs <= 0 {
		query.EndTs = time.Now().Unix()
	}
	if query.StartTs <= 0 {
		hours := query.Hours
		if hours <= 0 {
			hours = 24
		}
		if hours > 24*30 {
			hours = 24 * 30
		}
		query.StartTs = query.EndTs - int64(hours)*3600
	}
	dbQuery := DB.Where("bucket_ts >= ? AND bucket_ts <= ?", query.StartTs, query.EndTs)
	if query.ChannelId > 0 {
		dbQuery = dbQuery.Where("channel_id = ?", query.ChannelId)
	}
	if query.CredentialId > 0 {
		dbQuery = dbQuery.Where("credential_id = ?", query.CredentialId)
	}
	if query.RequestedModel != "" {
		dbQuery = dbQuery.Where("requested_model = ?", query.RequestedModel)
	}
	if query.Group != "" {
		dbQuery = dbQuery.Where(commonGroupCol+" = ?", query.Group)
	}
	if query.Protocol != "" {
		dbQuery = dbQuery.Where("protocol = ?", query.Protocol)
	}
	var rows []ChannelModelPerfMetric
	if err := dbQuery.Find(&rows).Error; err != nil {
		return nil, err
	}
	merged := make(map[string]*ChannelModelPerfAggregate)
	for _, row := range rows {
		key := fmtObservationAggregateKey(row.ChannelId, row.CredentialId, row.RequestedModel, row.UpstreamModel, row.UseGroup, row.Protocol)
		aggregate := merged[key]
		if aggregate == nil {
			aggregate = &ChannelModelPerfAggregate{ChannelId: row.ChannelId, CredentialId: row.CredentialId, RequestedModel: row.RequestedModel, UpstreamModel: row.UpstreamModel, Group: row.UseGroup, Protocol: row.Protocol, ErrorCounts: map[string]int64{}}
			merged[key] = aggregate
		}
		aggregate.merge(row)
	}
	result := make([]ChannelModelPerfAggregate, 0, len(merged))
	for _, value := range merged {
		result = append(result, *value)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].AttemptCount == result[j].AttemptCount {
			return result[i].RequestedModel < result[j].RequestedModel
		}
		return result[i].AttemptCount > result[j].AttemptCount
	})
	return result, nil
}

func fmtObservationAggregateKey(channelID, credentialID int, modelName, upstreamModel, group, protocol string) string {
	return fmt.Sprintf("%d/%d/%s/%s/%s/%s", channelID, credentialID, modelName, upstreamModel, group, protocol)
}

func BuildChannelModelPerfMetric(aggregate ChannelModelPerfAggregate, nodeName string, bucketTs int64) (ChannelModelPerfMetric, error) {
	if aggregate.ChannelId <= 0 || aggregate.RequestedModel == "" || bucketTs <= 0 {
		return ChannelModelPerfMetric{}, errors.New("invalid observation metric")
	}
	upstream := aggregate.UpstreamModel
	if upstream == "" && len(aggregate.UpstreamModels) > 0 {
		upstream = aggregate.UpstreamModels[0]
	}
	return ChannelModelPerfMetric{
		BucketTs: bucketTs, ChannelId: aggregate.ChannelId, CredentialId: aggregate.CredentialId,
		RequestedModel: aggregate.RequestedModel, UpstreamModel: upstream, UseGroup: aggregate.Group,
		Protocol: aggregate.Protocol, NodeName: nodeName,
		RequestCount: aggregate.RequestCount, RequestSuccessCount: aggregate.RequestSuccessCount,
		AttemptCount: aggregate.AttemptCount, AttemptSuccessCount: aggregate.AttemptSuccessCount,
		CacheObservableCount: aggregate.CacheObservableCount, CacheHitCount: aggregate.CacheHitCount,
		InputTokens: aggregate.InputTokens, CacheReadTokens: aggregate.CacheReadTokens, CacheWriteTokens: aggregate.CacheWriteTokens,
		LatencySumMs: aggregate.LatencySumMs, TtftSumMs: aggregate.TtftSumMs, FRTSumMs: aggregate.FRTSumMs,
		RequestLatencySumMs: aggregate.RequestLatencySumMs,
		LatencyCount:        aggregate.LatencyCount, RequestLatencyCount: aggregate.RequestLatencyCount, TtftCount: aggregate.TtftCount, FRTCount: aggregate.FRTCount,
		LatencyHistogram:        marshalObservationHistogram(aggregate.LatencyHistogram),
		RequestLatencyHistogram: marshalObservationHistogram(aggregate.RequestLatencyHistogram),
		TtftHistogram:           marshalObservationHistogram(aggregate.TtftHistogram),
		FRTHistogram:            marshalObservationHistogram(aggregate.FRTHistogram),
		SampleCount:             aggregate.SampleCount, UsageCount: aggregate.UsageCount,
		ErrorCounts: marshalErrorCounts(aggregate.ErrorCounts),
	}, nil
}
