package service

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

const opsCyberPolicyKey = "ops_cyber_policy"
const opsCyberPolicyForwardedKey = "ops_cyber_policy_forwarded"

// CyberPolicyMark carries the upstream evidence for one cyber policy block.
type CyberPolicyMark struct {
	Code           string
	Message        string
	Body           string
	UpstreamStatus int
	UpstreamInTok  int
	UpstreamOutTok int
}

// CyberPolicyEventInput contains only stable request metadata; upstream bodies
// and prompts are intentionally not persisted.
type CyberPolicyEventInput struct {
	UserID      int
	Group       string
	Model       string
	Protocol    string
	RequestPath string
	RequestID   string
}

// MarkOpsCyberPolicy stores the first cyber policy mark for the current request.
func MarkOpsCyberPolicy(c *gin.Context, mark CyberPolicyMark) {
	if c == nil || GetOpsCyberPolicy(c) != nil {
		return
	}
	mark.Code = "cyber_policy"
	mark.Message = strings.TrimSpace(mark.Message)
	mark.Body = boundedCyberPolicyBody([]byte(mark.Body))
	c.Set(opsCyberPolicyKey, &mark)
}

// GetOpsCyberPolicy returns the current request's cyber policy mark, if any.
func GetOpsCyberPolicy(c *gin.Context) *CyberPolicyMark {
	if c == nil {
		return nil
	}
	value, ok := c.Get(opsCyberPolicyKey)
	if !ok {
		return nil
	}
	mark, ok := value.(*CyberPolicyMark)
	if !ok {
		return nil
	}
	return mark
}

// ClearOpsCyberPolicy resets the per-turn mark used by long-lived WS requests.
func ClearOpsCyberPolicy(c *gin.Context) {
	if c != nil {
		c.Set(opsCyberPolicyKey, (*CyberPolicyMark)(nil))
		c.Set(opsCyberPolicyForwardedKey, false)
	}
}

// MarkOpsCyberPolicyForwarded records that a native stream/websocket response
// has already been sent and controller-level response writing must be skipped.
func MarkOpsCyberPolicyForwarded(c *gin.Context) {
	if c != nil {
		c.Set(opsCyberPolicyForwardedKey, true)
	}
}

func OpsCyberPolicyForwarded(c *gin.Context) bool {
	return c != nil && c.GetBool(opsCyberPolicyForwardedKey)
}

// NewOpenAICyberPolicyError detects and marks a cyber-policy payload, then
// returns the terminal error used by relay/controller code. A nil result means
// the payload is not a cyber-policy response.
func NewOpenAICyberPolicyError(c *gin.Context, payload []byte, status int, stream bool, usage *dto.Usage) *types.NewAPIError {
	hit, _, message := detectOpenAICyberPolicy(payload)
	if !hit {
		return nil
	}
	if status <= 0 {
		if stream {
			status = 200
		} else {
			status = 400
		}
	}
	if message == "" {
		message = "upstream cyber policy interception"
	}
	payloadUsage := extractCyberPolicyUsage(payload)
	if payloadUsage != nil {
		usage = dto.MergeUsage(payloadUsage, usage)
	}
	inTokens, outTokens := 0, 0
	if usage != nil {
		inTokens = normalizeCyberPolicyTokens(usage.InputTokens, usage.PromptTokens)
		outTokens = normalizeCyberPolicyTokens(usage.OutputTokens, usage.CompletionTokens)
	}
	MarkOpsCyberPolicy(c, CyberPolicyMark{
		Message:        message,
		Body:           boundedCyberPolicyBody(payload),
		UpstreamStatus: status,
		UpstreamInTok:  inTokens,
		UpstreamOutTok: outTokens,
	})
	mark := GetOpsCyberPolicy(c)
	if mark == nil {
		return types.NewOpenAIError(errors.New(message), types.ErrorCode("cyber_policy"), status,
			types.ErrOptionWithSkipRetry(), types.ErrOptionWithNoRecordErrorLog())
	}
	return types.NewOpenAIError(errors.New(mark.Message), types.ErrorCode("cyber_policy"), status,
		types.ErrOptionWithSkipRetry(), types.ErrOptionWithNoRecordErrorLog())
}

func extractCyberPolicyUsage(payload []byte) *dto.Usage {
	usage := &dto.Usage{}
	found := false
	for _, path := range []string{"usage", "response.usage"} {
		result := gjson.GetBytes(payload, path)
		if !result.Exists() || result.Raw == "" {
			continue
		}
		var candidate dto.Usage
		if err := common.Unmarshal([]byte(result.Raw), &candidate); err != nil {
			continue
		}
		usage = dto.MergeUsage(usage, &candidate)
		found = true
	}
	if !found {
		return nil
	}
	return usage
}

func normalizeCyberPolicyTokens(values ...int) int {
	const maxTokens = common.MaxQuota / 2
	for _, value := range values {
		if value > 0 {
			if value > maxTokens {
				return maxTokens
			}
			return value
		}
	}
	return 0
}

func boundedCyberPolicyBody(payload []byte) string {
	const maxBody = 4096
	if len(payload) > maxBody {
		payload = payload[:maxBody]
	}
	return strings.TrimSpace(string(payload))
}

// RecordCyberPolicyEvent persists a redacted, independently identifiable risk
// event. It deliberately bypasses content-moderation sampling/configuration.
func RecordCyberPolicyEvent(input CyberPolicyEventInput, mark *CyberPolicyMark) error {
	if mark == nil {
		return errors.New("cyber policy mark is nil")
	}
	message := CyberPolicyMessageForLog(mark.Message)
	digest := sha256.Sum256([]byte("cyber_policy\x00" + message))
	return model.CreateContentModerationLog(&model.ContentModerationLog{
		UserID:      input.UserID,
		GroupName:   strings.TrimSpace(input.Group),
		ModelName:   strings.TrimSpace(input.Model),
		Protocol:    strings.TrimSpace(input.Protocol),
		RequestPath: strings.TrimSpace(input.RequestPath),
		RequestID:   strings.TrimSpace(input.RequestID),
		Mode:        "post_upstream",
		Action:      model.ContentModerationActionCyberPolicy,
		Flagged:     true,
		Blocked:     true,
		Category:    model.ContentModerationActionCyberPolicy,
		Score:       1,
		Excerpt:     "[redacted]",
		ExcerptHash: hex.EncodeToString(digest[:]),
		Error:       message,
	})
}

// CyberPolicyMessageForLog masks and bounds an upstream message before it is
// written to gateway or operations logs. Client forwarding keeps the original
// upstream body separately in CyberPolicyMark.Body.
func CyberPolicyMessageForLog(message string) string {
	message = common.MaskSensitiveInfo(strings.TrimSpace(message))
	if message == "" {
		message = "upstream cyber policy interception"
	}
	return boundedCyberPolicyMessage(message)
}

func boundedCyberPolicyMessage(message string) string {
	const maxMessage = 1024
	if len(message) > maxMessage {
		return message[:maxMessage]
	}
	return message
}

// detectOpenAICyberPolicy recognizes OpenAI-compatible cyber_policy errors in
// either the top-level error object or a Responses event's nested response.
func detectOpenAICyberPolicy(payload []byte) (bool, string, string) {
	topCode := strings.TrimSpace(gjson.GetBytes(payload, "error.code").String())
	if strings.EqualFold(topCode, "cyber_policy") {
		return true, "cyber_policy", strings.TrimSpace(gjson.GetBytes(payload, "error.message").String())
	}
	nestedCode := strings.TrimSpace(gjson.GetBytes(payload, "response.error.code").String())
	if !strings.EqualFold(nestedCode, "cyber_policy") {
		return false, "", ""
	}
	return true, "cyber_policy", strings.TrimSpace(gjson.GetBytes(payload, "response.error.message").String())
}
