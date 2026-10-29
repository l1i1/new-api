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

// ChatContinuationRotateChannels makes rounds 2..N rotate to channels that have
// not been tried. Disabling it restores same-channel rounds. The zero value of a
// bool is "same channel", so the initializer makes rotation the default for any
// code path that runs before the option/env defaults are applied.
var ChatContinuationRotateChannels = true

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
	case ChatContinuationOptionMaxRounds, ChatContinuationOptionRoundMaxElapsedSeconds:
		if n, err := strconv.Atoi(value); err != nil || n < 0 {
			return fmt.Errorf("%s must be an integer >= 0", key)
		}
	case ChatContinuationOptionOnAbnormal, ChatContinuationOptionOnMissing:
		if _, err := strconv.ParseBool(value); err != nil {
			return fmt.Errorf("%s must be true or false", key)
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
