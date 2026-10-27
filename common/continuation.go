package common

import (
	"strconv"
	"strings"
)

// ChatContinuationMaxSteps is the number of extra upstream rounds the relay may
// stitch into one client stream when a provider output cap ends the first round
// (finish_reason=length) while the client's budget still has room. 0 disables
// the feature; the relay additionally requires the channel to be whitelisted.
var ChatContinuationMaxSteps int

// ChatContinuationChannelIDs is the channel whitelist for transparent chat
// continuation (RELAY_CHAT_CONTINUATION_CHANNELS, comma separated ids).
var ChatContinuationChannelIDs map[int]struct{}

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
