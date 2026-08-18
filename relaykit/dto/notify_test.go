package dto

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNotifyJSONPreservesLegacyValuesField(t *testing.T) {
	payload, err := json.Marshal(NewNotify(NotifyTypeChannelUpdate, "title", "content", nil))
	require.NoError(t, err)
	require.JSONEq(t, `{"type":"channel_update","title":"title","content":"content","values":null}`, string(payload))
}
