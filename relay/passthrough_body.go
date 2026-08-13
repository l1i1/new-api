package relay

import (
	"crypto/sha256"
	"fmt"
	"io"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/logger"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/setting/model_setting"
	"github.com/gin-gonic/gin"
)

// shouldPassThroughOpenAIRequestBody permits raw bodies only when the upstream
// accepts the OpenAI wire format. Channel settings express an opt-in, not a
// protocol conversion bypass: Gemini, Vertex, and configurable adapters must
// still receive their converted request bodies.
func shouldPassThroughOpenAIRequestBody(info *relaycommon.RelayInfo) bool {
	if info == nil {
		return false
	}
	passThroughEnabled := model_setting.GetGlobalSettings().PassThroughRequestEnabled ||
		info.ChannelSetting.PassThroughBodyEnabled
	return passThroughEnabled && info.ApiType == constant.APITypeOpenAI
}

func getPassThroughRequestBody(c *gin.Context, info *relaycommon.RelayInfo) (io.Reader, error) {
	storage, err := common.GetBodyStorage(c)
	if err != nil {
		return nil, err
	}
	if _, err = storage.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}

	if common.DebugEnabled {
		hash := sha256.New()
		if _, err = io.Copy(hash, storage); err != nil {
			return nil, err
		}
		logger.LogDebug(
			c,
			"pass-through request body: sha256=%s length=%d model=%s node=%s",
			fmt.Sprintf("%x", hash.Sum(nil)),
			storage.Size(),
			info.OriginModelName,
			common.NodeName,
		)
		if _, err = storage.Seek(0, io.SeekStart); err != nil {
			return nil, err
		}
	}

	return common.NewReplayableBodyReader(storage), nil
}
