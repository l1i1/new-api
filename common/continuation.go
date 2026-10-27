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
