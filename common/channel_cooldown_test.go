package common

import (
	"testing"
	"time"
)

func TestChannelCooldownPromotesAfterRepeatedFailures(t *testing.T) {
	const channelID = 987654
	t.Cleanup(func() { ClearChannelCooldown(channelID) })

	if ChannelInCooldown(channelID) {
		t.Fatal("a fresh channel must not be cooling")
	}
	RecordChannelFailure(channelID)
	if ChannelInCooldown(channelID) {
		t.Fatal("one failure must not cool the channel down")
	}
	RecordChannelFailure(channelID)
	if !ChannelInCooldown(channelID) {
		t.Fatal("two failures inside the window must cool the channel down")
	}
	ClearChannelCooldown(channelID)
	if ChannelInCooldown(channelID) {
		t.Fatal("clearing the cooldown must put the channel back immediately")
	}
}

func TestChannelCooldownExpires(t *testing.T) {
	const channelID = 987655
	t.Cleanup(func() { ClearChannelCooldown(channelID) })

	channelCooldown.Lock()
	channelCooldown.states[channelID] = &channelCooldownState{until: time.Now().Add(-time.Second)}
	channelCooldown.Unlock()

	if ChannelInCooldown(channelID) {
		t.Fatal("an expired cooldown must not keep the channel out")
	}
}
