package operation_setting

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"

	"github.com/QuantumNous/new-api/setting/config"
)

// PartnerSetting carries the reseller/partner (合作方) configuration: which
// inviter accounts belong to a partner, and the white-label display copy
// served to that partner's invitees. It is the server-side counterpart of
// dto.WhiteLabelConfig and is edited by operators, never by end users.
type PartnerSetting struct {
	Partners []PartnerEntry `json:"partners"`
}

// PartnerEntry binds inviter user IDs to one partner and its display copy.
type PartnerEntry struct {
	ID             string              `json:"id"`               // 合作方标识，如 tommy
	InviterUserIDs []int               `json:"inviter_user_ids"` // 归属该合作方的邀请人账号
	InviteCodes    []PartnerInviteCode `json:"invite_codes,omitempty"`
	Contact        string              `json:"contact"` // 替换全局 topup_contact 的展示文案（Markdown/HTML）
	Notice         string              `json:"notice"`  // 合作方公告文本区（Markdown/HTML，支持 tnt 分块）
	Version        int                 `json:"version"` // 文案版本号，每次写入递增
}

// PartnerInviteCode is one partner-console issued invite code. Registration
// resolves the code to InviterID, which is a synthetic id: a console-created
// bucket carries no new-api account, so the code is the whole identity behind
// the attribution. The console owns this list (it is rewritten on every push),
// because a bucket is created and named in the console, not on the site.
type PartnerInviteCode struct {
	Code      string `json:"code"`
	InviterID int    `json:"inviter_id"`
}

const (
	// PartnerInviteCodeMinLength keeps console codes out of the site's own
	// aff-code namespace: real accounts get exactly 4 alphanumeric characters
	// (model.User.Insert calls common.GetRandomString(4)), so a 5+ character
	// code can never shadow a real account's invite link.
	PartnerInviteCodeMinLength = 5
	PartnerInviteCodeMaxLength = 32

	// PartnerSyntheticInviterIDFloor opens the id range reserved for
	// console-issued codes. Real accounts auto-increment from 1, so the gap
	// leaves room for a billion accounts before the ranges could ever meet.
	PartnerSyntheticInviterIDFloor = 1_000_000_000
	// PartnerSyntheticInviterIDCeiling keeps synthetic ids inside int32, which
	// is what users.inviter_id is.
	PartnerSyntheticInviterIDCeiling = 2_000_000_000

	// PartnerMaxInviteCodes bounds one partner's code list, matching the
	// inviter-id cap the push endpoint already enforces.
	PartnerMaxInviteCodes = 200
)

var partnerInviteCodeRE = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// ValidatePartnerInviteCode reports whether a console-issued code is usable as
// an invite link parameter.
func ValidatePartnerInviteCode(code string) error {
	if len(code) < PartnerInviteCodeMinLength || len(code) > PartnerInviteCodeMaxLength {
		return fmt.Errorf("invite code must hold %d-%d characters", PartnerInviteCodeMinLength, PartnerInviteCodeMaxLength)
	}
	if !partnerInviteCodeRE.MatchString(code) {
		return errors.New("invite code must hold letters, digits, underscore or hyphen")
	}
	return nil
}

// ValidatePartnerInviteCodeInviterID rejects an inviter id outside the reserved
// range: a code whose id could be a real account's would attribute invitees to
// that account.
func ValidatePartnerInviteCodeInviterID(inviterID int) error {
	if inviterID < PartnerSyntheticInviterIDFloor || inviterID > PartnerSyntheticInviterIDCeiling {
		return fmt.Errorf("inviter id must be within [%d, %d]", PartnerSyntheticInviterIDFloor, PartnerSyntheticInviterIDCeiling)
	}
	return nil
}

// sanitizePartnerInviteCodes drops entries a hand-edited option row could carry
// that would break attribution, and keeps the first of each duplicated code.
func sanitizePartnerInviteCodes(codes []PartnerInviteCode) []PartnerInviteCode {
	cleaned := make([]PartnerInviteCode, 0, len(codes))
	seen := make(map[string]bool, len(codes))
	for _, entry := range codes {
		if ValidatePartnerInviteCode(entry.Code) != nil || ValidatePartnerInviteCodeInviterID(entry.InviterID) != nil {
			continue
		}
		if seen[entry.Code] {
			continue
		}
		seen[entry.Code] = true
		cleaned = append(cleaned, entry)
	}
	return cleaned
}

// PartnerSettingOptionKey is the options-table row holding the serialized
// partner configuration. It follows the ToolPriceOptionKey pattern:
// a dotted key persisted via model.UpdateOption and rehydrated at startup.
const PartnerSettingOptionKey = "partner_setting.partners"

var partnerSetting = PartnerSetting{}

var partnerSettingMu sync.RWMutex

// persistPartnerSetting writes a marshaled snapshot through the callback
// wired by model (options-table write). It runs without holding
// partnerSettingMu: model.UpdateOption dispatches back into
// LoadPartnerSettingFromJSONString on the same goroutine, and taking the
// mutex around persist would self-deadlock.
func persistPartnerSetting(raw []byte, persist func(key, value string) error) error {
	if persist == nil {
		return nil
	}
	return persist(PartnerSettingOptionKey, string(raw))
}

// loadPartnerSettingFromJSONString replaces the in-memory configuration from
// a persisted payload. Invalid entries fail closed on an empty configuration
// so a corrupt row can never misattribute invitees.
// LoadPartnerSettingFromJSONString replaces the in-memory configuration from
// a persisted payload. Invalid entries fail closed on an empty configuration
// so a corrupt row can never misattribute invitees.
func LoadPartnerSettingFromJSONString(value string) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return
	}
	var decoded PartnerSetting
	if err := json.Unmarshal([]byte(trimmed), &decoded); err != nil {
		return
	}
	partnerSettingMu.Lock()
	defer partnerSettingMu.Unlock()
	partnerSetting = decoded
	if partnerSetting.Partners == nil {
		partnerSetting.Partners = []PartnerEntry{}
	}
	for i := range partnerSetting.Partners {
		partnerSetting.Partners[i].InviteCodes = sanitizePartnerInviteCodes(partnerSetting.Partners[i].InviteCodes)
	}
}

func init() {
	config.GlobalConfig.Register("partner_setting", &partnerSetting)
}

// GetPartnerSetting returns a copy of the partner configuration.
func GetPartnerSetting() PartnerSetting {
	partnerSettingMu.RLock()
	defer partnerSettingMu.RUnlock()
	raw, err := json.Marshal(partnerSetting)
	if err != nil {
		return PartnerSetting{}
	}
	var out PartnerSetting
	if err := json.Unmarshal(raw, &out); err != nil {
		return PartnerSetting{}
	}
	return out
}

// FindPartnerByInviter returns the partner owning an inviter account, if any.
// Console-issued invite codes count: their synthetic ids are inviter ids too,
// which is what keeps white-label stamping and the invite-reward exclusion
// working for buckets that have no account behind them.
func FindPartnerByInviter(inviterID int) (PartnerEntry, bool) {
	if inviterID <= 0 {
		return PartnerEntry{}, false
	}
	for _, entry := range GetPartnerSetting().Partners {
		for _, id := range entry.InviterUserIDs {
			if id == inviterID {
				return entry, true
			}
		}
		for _, code := range entry.InviteCodes {
			if code.InviterID == inviterID {
				return entry, true
			}
		}
	}
	return PartnerEntry{}, false
}

// FindInviterIDByPartnerCode resolves a console-issued invite code to its
// synthetic inviter id. Codes are unique across partners (UpsertPartnerInviteCodes
// refuses duplicates), so the first match is the only match.
func FindInviterIDByPartnerCode(code string) (int, bool) {
	code = strings.TrimSpace(code)
	if code == "" {
		return 0, false
	}
	for _, entry := range GetPartnerSetting().Partners {
		for _, candidate := range entry.InviteCodes {
			if candidate.Code == code {
				return candidate.InviterID, true
			}
		}
	}
	return 0, false
}

// IsPartnerCodeInviter reports whether an inviter id belongs to a
// console-issued invite code rather than a real account. Registration skips the
// site's own invite bookkeeping for these ids: there is no users row to carry
// aff_count or aff_quota, and a partner's money is settled in the console.
func IsPartnerCodeInviter(inviterID int) bool {
	if inviterID < PartnerSyntheticInviterIDFloor {
		return false
	}
	for _, entry := range GetPartnerSetting().Partners {
		for _, code := range entry.InviteCodes {
			if code.InviterID == inviterID {
				return true
			}
		}
	}
	return false
}

// FindPartner returns the partner entry by ID.
func FindPartner(partnerID string) (PartnerEntry, bool) {
	partnerID = strings.TrimSpace(partnerID)
	if partnerID == "" {
		return PartnerEntry{}, false
	}
	for _, entry := range GetPartnerSetting().Partners {
		if entry.ID == partnerID {
			return entry, true
		}
	}
	return PartnerEntry{}, false
}

// UpdatePartnerContent writes contact/notice copy for a partner and bumps the
// version. The partner entry must already exist. The mutation commits under
// the mutex, then the new state persists through persist (model options-table
// write) without holding the mutex — model dispatches back into this package
// on the same goroutine, so persisting under lock would self-deadlock. A
// persistence failure rolls the in-memory change back.
func UpdatePartnerContent(partnerID, contact, notice string, persist func(key, value string) error) (int, error) {
	partnerSettingMu.Lock()
	var previous PartnerEntry
	version := 0
	found := false
	for i, entry := range partnerSetting.Partners {
		if entry.ID == partnerID {
			previous = partnerSetting.Partners[i]
			partnerSetting.Partners[i].Contact = contact
			partnerSetting.Partners[i].Notice = notice
			partnerSetting.Partners[i].Version++
			version = partnerSetting.Partners[i].Version
			found = true
			break
		}
	}
	snapshot, marshalErr := json.Marshal(partnerSetting)
	partnerSettingMu.Unlock()
	if !found {
		return 0, ErrPartnerNotFound
	}
	if marshalErr != nil {
		return 0, marshalErr
	}
	if err := persistPartnerSetting(snapshot, persist); err != nil {
		partnerSettingMu.Lock()
		for i, entry := range partnerSetting.Partners {
			if entry.ID == partnerID {
				partnerSetting.Partners[i] = previous
				break
			}
		}
		partnerSettingMu.Unlock()
		return 0, err
	}
	return version, nil
}

// UpsertPartnerMembers creates the partner entry when absent and merges
// inviter user IDs into it, deduplicated and order-preserving. Removal is
// deliberately unsupported: unattributing users must stay an explicit,
// audited operator action, not a sync side effect. The mutation commits under
// the mutex, then persists without holding it (see UpdatePartnerContent); a
// persistence failure rolls the in-memory change back.
func UpsertPartnerMembers(partnerID string, inviterUserIDs []int, persist func(key, value string) error) (PartnerEntry, error) {
	partnerID = strings.TrimSpace(partnerID)
	if partnerID == "" {
		return PartnerEntry{}, errors.New("partner id is required")
	}
	partnerSettingMu.Lock()
	snapshot, snapshotErr := json.Marshal(partnerSetting)
	if snapshotErr != nil {
		partnerSettingMu.Unlock()
		return PartnerEntry{}, snapshotErr
	}
	var result PartnerEntry
	for i, entry := range partnerSetting.Partners {
		if entry.ID != partnerID {
			continue
		}
		seen := make(map[int]bool, len(entry.InviterUserIDs))
		for _, id := range entry.InviterUserIDs {
			seen[id] = true
		}
		for _, id := range inviterUserIDs {
			if id <= 0 || seen[id] {
				continue
			}
			seen[id] = true
			partnerSetting.Partners[i].InviterUserIDs = append(partnerSetting.Partners[i].InviterUserIDs, id)
		}
		result = partnerSetting.Partners[i]
		break
	}
	if result.ID == "" {
		members := make([]int, 0, len(inviterUserIDs))
		seen := make(map[int]bool, len(inviterUserIDs))
		for _, id := range inviterUserIDs {
			if id <= 0 || seen[id] {
				continue
			}
			seen[id] = true
			members = append(members, id)
		}
		partnerSetting.Partners = append(partnerSetting.Partners, PartnerEntry{ID: partnerID, InviterUserIDs: members})
		result = partnerSetting.Partners[len(partnerSetting.Partners)-1]
	}
	after, marshalErr := json.Marshal(partnerSetting)
	partnerSettingMu.Unlock()
	if marshalErr != nil {
		partnerSettingMu.Lock()
		_ = json.Unmarshal(snapshot, &partnerSetting)
		partnerSettingMu.Unlock()
		return PartnerEntry{}, marshalErr
	}
	if err := persistPartnerSetting(after, persist); err != nil {
		partnerSettingMu.Lock()
		_ = json.Unmarshal(snapshot, &partnerSetting)
		partnerSettingMu.Unlock()
		return PartnerEntry{}, err
	}
	return result, nil
}

// ErrPartnerNotFound is returned when a partner entry does not exist.
var ErrPartnerNotFound = errors.New("partner not found")

// UpsertPartnerInviteCodes replaces the console-issued invite codes of one
// partner, creating the entry when absent. The console owns this list — it is
// rebuilt from the partner's buckets on every push — so unlike the add-only
// member merge this is a replace. Replacing is safe for attribution: a dropped
// code stops working for new registrations, while invitees already attributed
// keep the inviter id stored on their own row.
func UpsertPartnerInviteCodes(partnerID string, codes []PartnerInviteCode, persist func(key, value string) error) (PartnerEntry, error) {
	partnerID = strings.TrimSpace(partnerID)
	if partnerID == "" {
		return PartnerEntry{}, errors.New("partner id is required")
	}
	if len(codes) > PartnerMaxInviteCodes {
		return PartnerEntry{}, fmt.Errorf("at most %d invite codes per partner", PartnerMaxInviteCodes)
	}
	cleaned := make([]PartnerInviteCode, 0, len(codes))
	seen := make(map[string]bool, len(codes))
	for _, entry := range codes {
		if err := ValidatePartnerInviteCode(entry.Code); err != nil {
			return PartnerEntry{}, fmt.Errorf("invite code %q: %w", entry.Code, err)
		}
		if err := ValidatePartnerInviteCodeInviterID(entry.InviterID); err != nil {
			return PartnerEntry{}, fmt.Errorf("invite code %q: %w", entry.Code, err)
		}
		if seen[entry.Code] {
			return PartnerEntry{}, fmt.Errorf("duplicate invite code %q", entry.Code)
		}
		seen[entry.Code] = true
		cleaned = append(cleaned, entry)
	}

	partnerSettingMu.Lock()
	snapshot, snapshotErr := json.Marshal(partnerSetting)
	if snapshotErr != nil {
		partnerSettingMu.Unlock()
		return PartnerEntry{}, snapshotErr
	}
	// Codes resolve globally at registration, so the same code under two
	// partners would attribute to whichever entry is scanned first.
	for _, entry := range partnerSetting.Partners {
		if entry.ID == partnerID {
			continue
		}
		for _, candidate := range entry.InviteCodes {
			if seen[candidate.Code] {
				partnerSettingMu.Unlock()
				return PartnerEntry{}, fmt.Errorf("invite code %q already belongs to partner %s", candidate.Code, entry.ID)
			}
		}
	}
	var result PartnerEntry
	found := false
	for i, entry := range partnerSetting.Partners {
		if entry.ID != partnerID {
			continue
		}
		partnerSetting.Partners[i].InviteCodes = cleaned
		result = partnerSetting.Partners[i]
		found = true
		break
	}
	if !found {
		partnerSetting.Partners = append(partnerSetting.Partners, PartnerEntry{ID: partnerID, InviteCodes: cleaned})
		result = partnerSetting.Partners[len(partnerSetting.Partners)-1]
	}
	after, marshalErr := json.Marshal(partnerSetting)
	partnerSettingMu.Unlock()
	if marshalErr != nil {
		partnerSettingMu.Lock()
		_ = json.Unmarshal(snapshot, &partnerSetting)
		partnerSettingMu.Unlock()
		return PartnerEntry{}, marshalErr
	}
	if err := persistPartnerSetting(after, persist); err != nil {
		partnerSettingMu.Lock()
		_ = json.Unmarshal(snapshot, &partnerSetting)
		partnerSettingMu.Unlock()
		return PartnerEntry{}, err
	}
	return result, nil
}
