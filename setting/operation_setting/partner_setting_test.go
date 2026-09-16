package operation_setting_test

import (
	"encoding/json"
	"testing"

	"github.com/QuantumNous/new-api/setting/operation_setting"
)

func TestPartnerSettingFindAndUpdate(t *testing.T) {
	setting := operation_setting.GetPartnerSetting()
	_ = setting
	entry, found := operation_setting.FindPartnerByInviter(0)
	if found {
		t.Fatalf("zero inviter matched %+v", entry)
	}
	if _, found := operation_setting.FindPartner(""); found {
		t.Fatal("empty partner id matched")
	}
	if _, err := operation_setting.UpdatePartnerContent("no-such-partner", "c", "n"); err == nil {
		t.Fatal("content update for unknown partner accepted")
	}
}

func TestPartnerSettingJSONShape(t *testing.T) {
	raw := `{"partners":[{"id":"tommy","inviter_user_ids":[7],"contact":"c","notice":"n","version":1}]}`
	var setting struct {
		Partners []operation_setting.PartnerEntry `json:"partners"`
	}
	if err := json.Unmarshal([]byte(raw), &setting); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(setting.Partners) != 1 || setting.Partners[0].ID != "tommy" || setting.Partners[0].InviterUserIDs[0] != 7 {
		t.Fatalf("unexpected shape: %+v", setting)
	}
}

func TestUpsertPartnerMembersMergesDeduped(t *testing.T) {
	entry, err := operation_setting.UpsertPartnerMembers("test-merge", []int{7, 7, 0, -1, 8})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if len(entry.InviterUserIDs) != 2 || entry.InviterUserIDs[0] != 7 || entry.InviterUserIDs[1] != 8 {
		t.Fatalf("unexpected members: %+v", entry)
	}
	entry, err = operation_setting.UpsertPartnerMembers("test-merge", []int{8, 9})
	if err != nil {
		t.Fatalf("re-upsert: %v", err)
	}
	if len(entry.InviterUserIDs) != 3 || entry.InviterUserIDs[2] != 9 {
		t.Fatalf("merge lost members: %+v", entry)
	}
	if _, found := operation_setting.FindPartnerByInviter(9); !found {
		t.Fatal("merged inviter not found")
	}
	if _, err := operation_setting.UpsertPartnerMembers("", []int{1}); err == nil {
		t.Fatal("empty partner id accepted")
	}
}
