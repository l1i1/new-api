package common

import (
	"fmt"
	"strconv"
	"strings"
)

// Option keys for the DB-backed continuation settings. The env vars with the
// same names remain only as first-boot defaults; after that the OptionMap row
// (editable in the admin console) is authoritative.
const (
	ChatContinuationOptionPrefix                    = "relay_chat_continuation_"
	ChatContinuationOptionOnAbnormal                = ChatContinuationOptionPrefix + "on_abnormal"
	ChatContinuationOptionOnMissing                 = ChatContinuationOptionPrefix + "on_missing"
	ChatContinuationOptionChannels                  = ChatContinuationOptionPrefix + "channels"
	ChatContinuationOptionMaxSteps                  = ChatContinuationOptionPrefix + "max_steps"
	ChatContinuationOptionAbnormalMaxSteps          = ChatContinuationOptionPrefix + "abnormal_max_steps"
	ChatContinuationOptionAbnormalMaxReasoningBytes = ChatContinuationOptionPrefix + "abnormal_max_reasoning_bytes"
	ChatContinuationOptionAbnormalMinReasoningChars = ChatContinuationOptionPrefix + "abnormal_min_reasoning_chars"
	ChatContinuationOptionMissingMaxSteps           = ChatContinuationOptionPrefix + "missing_max_steps"
	ChatContinuationOptionMissingMaxReasoningBytes  = ChatContinuationOptionPrefix + "missing_max_reasoning_bytes"
	ChatContinuationOptionMissingMinReasoningChars  = ChatContinuationOptionPrefix + "missing_min_reasoning_chars"
	ChatContinuationOptionMaxRounds                 = ChatContinuationOptionPrefix + "max_rounds"
	ChatContinuationOptionRoundMaxElapsedSeconds    = ChatContinuationOptionPrefix + "round_max_elapsed_seconds"
	ChatContinuationOptionRotateChannels            = ChatContinuationOptionPrefix + "rotate_channels"
	ChatContinuationOptionOutputCaps                = ChatContinuationOptionPrefix + "output_caps"
	ChatContinuationOptionToolBufferChannels        = ChatContinuationOptionPrefix + "tool_buffer_channels"
	ChatContinuationOptionToolRepairChannels        = ChatContinuationOptionPrefix + "tool_repair_channels"
	ChatContinuationOptionCacheEstimateModels       = ChatContinuationOptionPrefix + "cache_estimate_models"
)

// ChatContinuationMaxSteps is the number of extra upstream rounds the relay may
// stitch into one client stream when a provider output cap ends the first round
// (finish_reason=length) while the client's budget still has room. 0 disables
// the feature; the relay additionally requires the channel to be whitelisted.
var ChatContinuationMaxSteps int

// ChatContinuationMaxRounds is the total number of continuation rounds a request
// may run: round 1 on the original channel, every later round on a channel the
// request has not tried yet. Zero keeps the legacy MaxSteps round count.
var ChatContinuationMaxRounds int

// ChatContinuationMaxRoundsSet records whether the rotation-era round budget was
// configured explicitly (env or admin option). When it was not, the legacy
// per-mode MaxSteps values keep controlling the round count; when it was set to
// 0, transparent continuation is switched off entirely.
var ChatContinuationMaxRoundsSet bool

// ChatContinuationRoundMaxElapsedSeconds bounds the wall clock a repair may spend,
// counted from the request start (default 1500s).
var ChatContinuationRoundMaxElapsedSeconds int

// ChatContinuationOutputCaps lists the provider output-token caps whose streams
// are known to end without a terminal finish_reason. A stream that stops exactly
// at one of these caps after only reasoning is a by-design truncation, not a
// channel failure (2026-10-30: ch102 ended at 32000 with [DONE] and no
// finish_reason; the official endpoint answers that shape with length).
var ChatContinuationOutputCaps = []int{32000, 32768}

// ChatContinuationOutputCapsRaw keeps the comma separated form for the options table.
var ChatContinuationOutputCapsRaw = "32000,32768"

// ChatContinuationRotateChannels makes rounds 2..N rotate to channels that have
// not been tried. Disabling it restores same-channel rounds. The zero value of a
// bool is "same channel", so the initializer makes rotation the default for any
// code path that runs before the option/env defaults are applied.
var ChatContinuationRotateChannels = true

// ChatToolBufferChannelIDs lists the channels whose tool-call deltas are held
// until the call is complete, so a cut can never deliver half an arguments
// document (RELAY_CHAT_CONTINUATION_TOOL_BUFFER_CHANNELS, comma separated ids;
// empty disables the gate everywhere).
var ChatToolBufferChannelIDs map[int]struct{}

// ChatToolBufferChannelsRaw keeps the comma separated form for the options table.
var ChatToolBufferChannelsRaw string

// ChatToolRepairChannelIDs lists the channels allowed to regenerate a cut tool
// call on another channel (RELAY_CHAT_CONTINUATION_TOOL_REPAIR_CHANNELS; empty
// disables regeneration everywhere). Scoped per channel because a regeneration
// spends a second upstream generation and must be piloted.
var ChatToolRepairChannelIDs map[int]struct{}

// ChatToolRepairChannelsRaw keeps the comma separated form for the options table.
var ChatToolRepairChannelsRaw string

// ChatCacheEstimateModels lists the model-name prefixes whose Claude-protocol
// responses carry an estimated cache_creation_input_tokens when the upstream only
// reports cache hits (implicit prefix cache, e.g. Kimi prompt caching). The estimate
// is reporting-side only: billing usage keeps its counts, so the invoice total does
// not change. Empty disables the estimate everywhere.
var ChatCacheEstimateModels = []string{"kimi-k3"}

// ChatCacheEstimateModelsRaw keeps the comma separated form for the options table.
var ChatCacheEstimateModelsRaw = "kimi-k3"

// ChatCacheEstimateModelAllowed reports whether the cache-write estimate applies to
// one model name (prefix match, case-insensitive).
func ChatCacheEstimateModelAllowed(model string) bool {
	model = strings.ToLower(strings.TrimSpace(model))
	if model == "" {
		return false
	}
	for _, prefix := range ChatCacheEstimateModels {
		prefix = strings.ToLower(strings.TrimSpace(prefix))
		if prefix != "" && strings.HasPrefix(model, prefix) {
			return true
		}
	}
	return false
}

// parseChatCacheEstimateModels splits the option value into model prefixes.
func parseChatCacheEstimateModels(value string) []string {
	models := make([]string, 0, 4)
	for _, part := range strings.Split(value, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			models = append(models, part)
		}
	}
	return models
}

// ChatToolBufferChannelAllowed reports whether tool-call deltas are buffered for
// one channel.
func ChatToolBufferChannelAllowed(channelID int) bool {
	if channelID <= 0 {
		return false
	}
	_, ok := ChatToolBufferChannelIDs[channelID]
	return ok
}

// ChatToolRepairChannelAllowed reports whether a cut tool call may be regenerated
// on another channel for this channel.
func ChatToolRepairChannelAllowed(channelID int) bool {
	if channelID <= 0 {
		return false
	}
	_, ok := ChatToolRepairChannelIDs[channelID]
	return ok
}

// ChatContinuationChannelIDs is the channel whitelist for transparent chat
// continuation (RELAY_CHAT_CONTINUATION_CHANNELS, comma separated ids).
var ChatContinuationChannelIDs map[int]struct{}

// ChatContinuationChannelsRaw keeps the comma separated env/default form so a
// fresh database is seeded with the operator's configured channel list.
var ChatContinuationChannelsRaw string

// ChatContinuationOnAbnormal enables the second continuation mode: when an
// upstream stream dies with an abnormal finish_reason before any visible
// content was produced but after reasoning was emitted (the 900s cut-off
// shape), the relay replays the captured reasoning as the assistant message
// and asks the model to continue thinking. Disabled by default.
var ChatContinuationOnAbnormal bool

// ChatContinuationAbnormalMaxSteps bounds the extra rounds for the abnormal
// mode. Kept separate from the output-cap mode because each round may cost a
// full upstream timeout window.
var ChatContinuationAbnormalMaxSteps int

// ChatContinuationAbnormalMaxReasoningBytes caps the reasoning text captured
// from the first round. A 900s thinking round can emit >100k characters; the
// cap keeps per-request memory bounded and skips continuation rather than
// replaying a partial prefix.
var ChatContinuationAbnormalMaxReasoningBytes int

// ChatContinuationOnMissing enables the third mode: an upstream stream that
// reaches EOF/[DONE] without ever sending a terminal finish_reason, has no
// visible content, but already emitted reasoning (the 60s silent cut shape).
// Disabled by default.
var ChatContinuationOnMissing bool

// ChatContinuationMissingMaxSteps bounds the extra rounds for the missing-finish
// mode; one round avoids burning repeated 60s windows.
var ChatContinuationMissingMaxSteps int

// ChatContinuationMissingMaxReasoningBytes caps the captured reasoning for the
// missing-finish mode.
var ChatContinuationMissingMaxReasoningBytes int

// ChatContinuationMissingMinReasoningChars is the minimum captured reasoning
// length that makes a missing-finish continuation worthwhile.
var ChatContinuationMissingMinReasoningChars int

// ChatContinuationAbnormalMinReasoningChars is the minimum captured reasoning
// length that makes a continuation worthwhile (below it the model had barely
// started thinking).
var ChatContinuationAbnormalMinReasoningChars int

// ParseChatContinuationChannelIDs parses the comma separated channel-id list.
// Invalid entries are ignored; an empty value yields an empty set.
func ParseChatContinuationChannelIDs(raw string) map[int]struct{} {
	ids := make(map[int]struct{})
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		id, err := strconv.Atoi(part)
		if err != nil || id <= 0 {
			continue
		}
		ids[id] = struct{}{}
	}
	return ids
}

// ChatContinuationChannelAllowed reports whether continuation is enabled for
// one channel id.
func ChatContinuationChannelAllowed(channelID int) bool {
	if channelID <= 0 || len(ChatContinuationChannelIDs) == 0 {
		return false
	}
	_, ok := ChatContinuationChannelIDs[channelID]
	return ok
}

// SetChatContinuationOption validates and applies one DB-backed continuation
// setting. It returns an error for an unknown key or a malformed value so the
// admin API rejects the write before it can reach request goroutines.
func ValidateChatContinuationOption(key, value string) error {
	value = strings.TrimSpace(value)
	switch key {
	case ChatContinuationOptionRotateChannels:
		if _, err := strconv.ParseBool(value); err != nil {
			return fmt.Errorf("%s must be true or false", key)
		}
	case ChatContinuationOptionOutputCaps:
		if value == "" {
			return nil
		}
		for _, part := range strings.Split(value, ",") {
			part = strings.TrimSpace(part)
			if n, err := strconv.Atoi(part); err != nil || n <= 0 {
				return fmt.Errorf("%s must be comma separated positive token counts", key)
			}
		}
	case ChatContinuationOptionMaxRounds, ChatContinuationOptionRoundMaxElapsedSeconds:
		if n, err := strconv.Atoi(value); err != nil || n < 0 {
			return fmt.Errorf("%s must be an integer >= 0", key)
		}
	case ChatContinuationOptionOnAbnormal, ChatContinuationOptionOnMissing:
		if _, err := strconv.ParseBool(value); err != nil {
			return fmt.Errorf("%s must be true or false", key)
		}
	case ChatContinuationOptionCacheEstimateModels:
		// Comma separated model-name prefixes; an empty list disables the estimate.
		for _, part := range strings.Split(value, ",") {
			if strings.ContainsAny(strings.TrimSpace(part), " \t\n") {
				return fmt.Errorf("%s must be comma separated model names", key)
			}
		}
	case ChatContinuationOptionToolBufferChannels, ChatContinuationOptionToolRepairChannels:
		for _, part := range strings.Split(value, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			if id, err := strconv.Atoi(part); err != nil || id <= 0 {
				return fmt.Errorf("%s must be comma separated channel ids", key)
			}
		}
	case ChatContinuationOptionChannels:
		if value == "" {
			return nil
		}
		for _, part := range strings.Split(value, ",") {
			part = strings.TrimSpace(part)
			id, err := strconv.Atoi(part)
			if err != nil || id <= 0 {
				return fmt.Errorf("%s must be comma separated positive channel ids", key)
			}
		}
	case ChatContinuationOptionAbnormalMaxReasoningBytes, ChatContinuationOptionMissingMaxReasoningBytes:
		if n, err := strconv.Atoi(value); err != nil || n < 1 {
			return fmt.Errorf("%s must be an integer >= 1", key)
		}
	case ChatContinuationOptionMaxSteps, ChatContinuationOptionAbnormalMaxSteps,
		ChatContinuationOptionMissingMaxSteps, ChatContinuationOptionAbnormalMinReasoningChars,
		ChatContinuationOptionMissingMinReasoningChars:
		if n, err := strconv.Atoi(value); err != nil || n < 0 {
			return fmt.Errorf("%s must be an integer >= 0", key)
		}
	default:
		return fmt.Errorf("unknown chat continuation option: %s", key)
	}
	return nil
}

func SetChatContinuationOption(key, value string) error {
	if err := ValidateChatContinuationOption(key, value); err != nil {
		return err
	}
	value = strings.TrimSpace(value)
	parseBool := func() (bool, error) { return strconv.ParseBool(value) }
	parseInt := func(min int) (int, error) {
		n, err := strconv.Atoi(value)
		if err != nil || n < min {
			return 0, fmt.Errorf("%s must be an integer >= %d", key, min)
		}
		return n, nil
	}
	switch key {
	case ChatContinuationOptionOnAbnormal:
		v, err := parseBool()
		if err != nil {
			return err
		}
		ChatContinuationOnAbnormal = v
	case ChatContinuationOptionOnMissing:
		v, err := parseBool()
		if err != nil {
			return err
		}
		ChatContinuationOnMissing = v
	case ChatContinuationOptionChannels:
		ChatContinuationChannelsRaw = value
		if value != "" {
			for _, part := range strings.Split(value, ",") {
				part = strings.TrimSpace(part)
				id, err := strconv.Atoi(part)
				if err != nil || id <= 0 {
					return fmt.Errorf("%s must be comma separated positive channel ids", key)
				}
			}
		}
		ChatContinuationChannelIDs = ParseChatContinuationChannelIDs(value)
	case ChatContinuationOptionToolBufferChannels:
		ChatToolBufferChannelsRaw = value
		ChatToolBufferChannelIDs = ParseChatContinuationChannelIDs(value)
	case ChatContinuationOptionToolRepairChannels:
		ChatToolRepairChannelsRaw = value
		ChatToolRepairChannelIDs = ParseChatContinuationChannelIDs(value)
	case ChatContinuationOptionCacheEstimateModels:
		ChatCacheEstimateModelsRaw = value
		ChatCacheEstimateModels = parseChatCacheEstimateModels(value)
	case ChatContinuationOptionOutputCaps:
		caps := []int{}
		if value != "" {
			for _, part := range strings.Split(value, ",") {
				part = strings.TrimSpace(part)
				n, err := strconv.Atoi(part)
				if err != nil || n <= 0 {
					return fmt.Errorf("%s must be comma separated positive token counts", key)
				}
				caps = append(caps, n)
			}
		}
		ChatContinuationOutputCapsRaw = value
		ChatContinuationOutputCaps = caps
	case ChatContinuationOptionRotateChannels:
		v, err := parseBool()
		if err != nil {
			return err
		}
		ChatContinuationRotateChannels = v
	case ChatContinuationOptionMaxRounds:
		v, err := parseInt(0)
		if err != nil {
			return err
		}
		ChatContinuationMaxRounds = v
		ChatContinuationMaxRoundsSet = true
	case ChatContinuationOptionRoundMaxElapsedSeconds:
		v, err := parseInt(0)
		if err != nil {
			return err
		}
		ChatContinuationRoundMaxElapsedSeconds = v
	case ChatContinuationOptionMaxSteps:
		v, err := parseInt(0)
		if err != nil {
			return err
		}
		ChatContinuationMaxSteps = v
	case ChatContinuationOptionAbnormalMaxSteps:
		v, err := parseInt(0)
		if err != nil {
			return err
		}
		ChatContinuationAbnormalMaxSteps = v
	case ChatContinuationOptionAbnormalMaxReasoningBytes:
		v, err := parseInt(1)
		if err != nil {
			return err
		}
		ChatContinuationAbnormalMaxReasoningBytes = v
	case ChatContinuationOptionAbnormalMinReasoningChars:
		v, err := parseInt(0)
		if err != nil {
			return err
		}
		ChatContinuationAbnormalMinReasoningChars = v
	case ChatContinuationOptionMissingMaxSteps:
		v, err := parseInt(0)
		if err != nil {
			return err
		}
		ChatContinuationMissingMaxSteps = v
	case ChatContinuationOptionMissingMaxReasoningBytes:
		v, err := parseInt(1)
		if err != nil {
			return err
		}
		ChatContinuationMissingMaxReasoningBytes = v
	case ChatContinuationOptionMissingMinReasoningChars:
		v, err := parseInt(0)
		if err != nil {
			return err
		}
		ChatContinuationMissingMinReasoningChars = v
	default:
		return fmt.Errorf("unknown chat continuation option: %s", key)
	}
	return nil
}
