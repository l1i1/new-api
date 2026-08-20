package dto

import (
	"testing"

	kitutil "github.com/QuantumNous/new-api/relaykit/relayconvert/kitutil"
	"github.com/stretchr/testify/require"
)

func TestGeminiPartUnmarshalSupportsSnakeCaseFileData(t *testing.T) {
	var part GeminiPart
	require.NoError(t, kitutil.Unmarshal([]byte(`{"file_data":{"mime_type":"image/png","file_uri":"https://img.test/example.png"}}`), &part))
	require.NotNil(t, part.FileData)
	require.Equal(t, "image/png", part.FileData.MimeType)
	require.Equal(t, "https://img.test/example.png", part.FileData.FileUri)
}
