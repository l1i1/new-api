/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
package controller

import (
	"testing"

	"github.com/QuantumNous/new-api/model"
	"github.com/stretchr/testify/require"
)

func TestResolveRemovedMultiKeyPositions(t *testing.T) {
	channel := &model.Channel{
		ChannelInfo: model.ChannelInfo{
			IsMultiKey:   true,
			MultiKeySize: 5,
		},
		Credentials: []model.ChannelCredential{
			{Id: 10, Position: 0},
			{Id: 11, Position: 1},
			{Id: 12, Position: 2},
			{Id: 13, Position: 3},
			{Id: 14, Position: 4},
			// Historical rows must never select an active pool position.
			{Id: 25, Position: -25},
		},
	}

	t.Run("resolves durable ids to active positions only", func(t *testing.T) {
		removed := resolveRemovedMultiKeyPositions(channel, []int{11, 13, 25, 999})
		require.Equal(t, map[int]bool{1: true, 3: true}, removed)
	})

	t.Run("empty selection resolves to nothing", func(t *testing.T) {
		removed := resolveRemovedMultiKeyPositions(channel, nil)
		require.Empty(t, removed)
	})
}

func TestRebuildMultiKeyChannelState(t *testing.T) {
	channel := &model.Channel{
		Key: "k0\nk1\nk2\nk3",
		ChannelInfo: model.ChannelInfo{
			IsMultiKey:             true,
			MultiKeySize:           4,
			MultiKeyStatusList:     map[int]int{1: 2, 3: 3},
			MultiKeyDisabledTime:   map[int]int64{1: 100, 3: 300},
			MultiKeyDisabledReason: map[int]string{1: "manual", 3: "auto"},
		},
	}

	t.Run("removes positions and re-indexes disabled state", func(t *testing.T) {
		remaining, statuses, times, reasons := rebuildMultiKeyChannelState(
			channel,
			map[int]bool{1: true},
		)
		require.Equal(t, []string{"k0", "k2", "k3"}, remaining)
		require.Equal(t, map[int]int{2: 3}, statuses)
		require.Equal(t, map[int]int64{2: 300}, times)
		require.Equal(t, map[int]string{2: "auto"}, reasons)
	})

	t.Run("removing every key empties the pool", func(t *testing.T) {
		remaining, _, _, _ := rebuildMultiKeyChannelState(
			channel,
			map[int]bool{0: true, 1: true, 2: true, 3: true},
		)
		require.Empty(t, remaining)
	})

	t.Run("no removal keeps the key list and maps unchanged", func(t *testing.T) {
		remaining, statuses, times, reasons := rebuildMultiKeyChannelState(channel, map[int]bool{4: true})
		require.Equal(t, []string{"k0", "k1", "k2", "k3"}, remaining)
		require.Equal(t, map[int]int{1: 2, 3: 3}, statuses)
		require.Equal(t, map[int]int64{1: 100, 3: 300}, times)
		require.Equal(t, map[int]string{1: "manual", 3: "auto"}, reasons)
	})
}
