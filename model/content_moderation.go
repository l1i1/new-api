package model

import (
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	ContentModerationOptionKey         = "content_moderation.config"
	ContentModerationActionCyberPolicy = "cyber_policy"
)

// ContentModerationLog stores an audit decision without retaining the request
// body. Excerpt is intentionally redacted by the service; ExcerptHash is the
// stable operator-side correlation value for the submitted multimodal input.
type ContentModerationLog struct {
	ID                int     `json:"id" gorm:"primaryKey"`
	UserID            int     `json:"user_id" gorm:"index"`
	GroupName         string  `json:"group" gorm:"column:group_name;index"`
	ModelName         string  `json:"model" gorm:"column:model_name;index"`
	Protocol          string  `json:"protocol" gorm:"index"`
	RequestPath       string  `json:"request_path,omitempty"`
	RequestID         string  `json:"request_id,omitempty" gorm:"index"`
	Mode              string  `json:"mode"`
	Action            string  `json:"action"`
	CapacityReason    string  `json:"capacity_reason,omitempty" gorm:"size:32"`
	Flagged           bool    `json:"flagged" gorm:"index"`
	Blocked           bool    `json:"blocked"`
	Category          string  `json:"category,omitempty"`
	Score             float64 `json:"score,omitempty"`
	CategoryScores    string  `json:"category_scores,omitempty" gorm:"type:text"`
	Excerpt           string  `json:"excerpt,omitempty" gorm:"type:text"`
	ExcerptHash       string  `json:"excerpt_hash,omitempty" gorm:"size:64;index"`
	LatencyMS         int64   `json:"latency_ms,omitempty"`
	Error             string  `json:"error,omitempty" gorm:"type:text"`
	EmailSent         bool    `json:"email_sent,omitempty"`
	EmailSending      bool    `json:"-"`
	EmailSendingAt    int64   `json:"-"`
	EmailSendingToken string  `json:"-" gorm:"size:64;index"`
	CreatedAt         int64   `json:"created_at" gorm:"autoCreateTime;index"`
}

func (ContentModerationLog) TableName() string {
	return "content_moderation_logs"
}

type ContentModerationLogFilter struct {
	UserID    *int
	GroupName string
	ModelName string
	Protocol  string
	RequestID string
	Flagged   *bool
	StartAt   int64
	EndAt     int64
	Offset    int
	Limit     int
}

type ContentModerationUserState struct {
	UserID                int   `json:"user_id" gorm:"primaryKey"`
	ViolationResetAfterID int64 `json:"violation_reset_after_id" gorm:"index"`
}

func (ContentModerationUserState) TableName() string {
	return "content_moderation_user_states"
}

func CreateContentModerationLog(entry *ContentModerationLog) error {
	if entry == nil {
		return errors.New("content moderation log is nil")
	}
	if DB == nil {
		return errors.New("database is not initialized")
	}
	return DB.Create(entry).Error
}

func GetContentModerationLog(id int) (*ContentModerationLog, error) {
	if DB == nil {
		return nil, errors.New("database is not initialized")
	}
	var entry ContentModerationLog
	if err := DB.First(&entry, id).Error; err != nil {
		return nil, err
	}
	return &entry, nil
}

func CountFlaggedContentModerationByUserSince(userID int, since time.Time) (int64, error) {
	if DB == nil {
		return 0, errors.New("database is not initialized")
	}
	resetAfterID, err := getContentModerationViolationResetAfterID(userID)
	if err != nil {
		return 0, err
	}
	var count int64
	err = DB.Model(&ContentModerationLog{}).
		Where("user_id = ? AND flagged = ? AND id > ? AND created_at >= ?", userID, true, resetAfterID, since.Unix()).
		Where("(action <> ? OR action IS NULL)", ContentModerationActionCyberPolicy).
		Count(&count).Error
	return count, err
}

func getContentModerationViolationResetAfterID(userID int) (int64, error) {
	if DB == nil {
		return 0, errors.New("database is not initialized")
	}
	var state ContentModerationUserState
	err := DB.Where("user_id = ?", userID).First(&state).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, nil
	}
	return state.ViolationResetAfterID, err
}

func ResetContentModerationUserViolations(userID int) error {
	if DB == nil {
		return errors.New("database is not initialized")
	}
	if userID <= 0 {
		return errors.New("invalid user id")
	}
	var resetAfterID int64
	if err := DB.Model(&ContentModerationLog{}).Where("user_id = ?", userID).
		Select("COALESCE(MAX(id), 0)").Scan(&resetAfterID).Error; err != nil {
		return err
	}
	state := ContentModerationUserState{
		UserID:                userID,
		ViolationResetAfterID: resetAfterID,
	}
	return DB.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "user_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"violation_reset_after_id"}),
	}).Create(&state).Error
}

// ClaimContentModerationEmail atomically reserves one notification attempt.
// The durable claim intentionally has no automatic takeover: after an
// ambiguous process failure, suppressing a duplicate email is safer than
// retrying an SMTP delivery that may already have succeeded.
func ClaimContentModerationEmail(id int, token string) (bool, error) {
	if DB == nil {
		return false, errors.New("database is not initialized")
	}
	if id <= 0 || strings.TrimSpace(token) == "" {
		return false, errors.New("invalid content moderation email claim")
	}
	result := DB.Model(&ContentModerationLog{}).
		Where("id = ? AND email_sent = ? AND email_sending = ?", id, false, false).
		Updates(map[string]interface{}{
			"email_sending":       true,
			"email_sending_at":    time.Now().Unix(),
			"email_sending_token": token,
		})
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected == 1, nil
}

func MarkContentModerationEmailSent(id int, token string) error {
	if DB == nil {
		return errors.New("database is not initialized")
	}
	result := DB.Model(&ContentModerationLog{}).
		Where("id = ? AND email_sent = ? AND email_sending = ? AND email_sending_token = ?", id, false, true, token).
		Updates(map[string]interface{}{
			"email_sent":          true,
			"email_sending":       false,
			"email_sending_at":    0,
			"email_sending_token": "",
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return errors.New("content moderation email claim was lost")
	}
	return nil
}

func ReleaseContentModerationEmailClaim(id int, token string) error {
	if DB == nil {
		return errors.New("database is not initialized")
	}
	return DB.Model(&ContentModerationLog{}).
		Where("id = ? AND email_sent = ? AND email_sending = ? AND email_sending_token = ?", id, false, true, token).
		Updates(map[string]interface{}{
			"email_sending":       false,
			"email_sending_at":    0,
			"email_sending_token": "",
		}).Error
}

func QueryContentModerationLogs(filter ContentModerationLogFilter) ([]ContentModerationLog, int64, error) {
	if DB == nil {
		return nil, 0, errors.New("database is not initialized")
	}

	query := DB.Model(&ContentModerationLog{})
	if filter.UserID != nil {
		query = query.Where("user_id = ?", *filter.UserID)
	}
	if group := strings.TrimSpace(filter.GroupName); group != "" {
		query = query.Where("group_name = ?", group)
	}
	if modelName := strings.TrimSpace(filter.ModelName); modelName != "" {
		query = query.Where("model_name = ?", modelName)
	}
	if protocol := strings.TrimSpace(filter.Protocol); protocol != "" {
		query = query.Where("protocol = ?", protocol)
	}
	if requestID := strings.TrimSpace(filter.RequestID); requestID != "" {
		query = query.Where("request_id = ?", requestID)
	}
	if filter.Flagged != nil {
		query = query.Where("flagged = ?", *filter.Flagged)
	}
	if filter.StartAt > 0 {
		query = query.Where("created_at >= ?", filter.StartAt)
	}
	if filter.EndAt > 0 {
		query = query.Where("created_at <= ?", filter.EndAt)
	}

	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	limit := filter.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 100 {
		limit = 100
	}
	offset := filter.Offset
	if offset < 0 {
		offset = 0
	}

	var entries []ContentModerationLog
	err := query.Order("created_at desc").Order("id desc").Offset(offset).Limit(limit).Find(&entries).Error
	return entries, total, err
}
