package operation_setting

import (
	"encoding/json"
	"errors"
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
	ID             string `json:"id"`               // 合作方标识，如 tommy
	InviterUserIDs []int  `json:"inviter_user_ids"` // 归属该合作方的邀请人账号
	Contact        string `json:"contact"`          // 替换全局 topup_contact 的展示文案（Markdown/HTML）
	Notice         string `json:"notice"`           // 合作方公告文本区（Markdown/HTML，支持 tnt 分块）
	Version        int    `json:"version"`          // 文案版本号，每次写入递增
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
	}
	return PartnerEntry{}, false
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
