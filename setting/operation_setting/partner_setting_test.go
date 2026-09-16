package operation_setting_test

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/QuantumNous/new-api/setting/operation_setting"
)

var errTestPersist = errors.New("persist failed")

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
	if _, err := operation_setting.UpdatePartnerContent("no-such-partner", "c", "n", nil); err == nil {
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
	entry, err := operation_setting.UpsertPartnerMembers("test-merge", []int{7, 7, 0, -1, 8}, nil)
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if len(entry.InviterUserIDs) != 2 || entry.InviterUserIDs[0] != 7 || entry.InviterUserIDs[1] != 8 {
		t.Fatalf("unexpected members: %+v", entry)
	}
	entry, err = operation_setting.UpsertPartnerMembers("test-merge", []int{8, 9}, nil)
	if err != nil {
		t.Fatalf("re-upsert: %v", err)
	}
	if len(entry.InviterUserIDs) != 3 || entry.InviterUserIDs[2] != 9 {
		t.Fatalf("merge lost members: %+v", entry)
	}
	if _, found := operation_setting.FindPartnerByInviter(9); !found {
		t.Fatal("merged inviter not found")
	}
	if _, err := operation_setting.UpsertPartnerMembers("", []int{1}, nil); err == nil {
		t.Fatal("empty partner id accepted")
	}
}

func TestPartnerSettingPersistsThroughCallback(t *testing.T) {
	writes := map[string]string{}
	persist := func(key, value string) error {
		writes[key] = value
		return nil
	}
	entry, err := operation_setting.UpsertPartnerMembers("test-persist", []int{42}, persist)
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if entry.ID != "test-persist" {
		t.Fatalf("unexpected entry: %+v", entry)
	}
	raw, ok := writes[operation_setting.PartnerSettingOptionKey]
	if !ok {
		t.Fatalf("no persist call, writes=%v", writes)
	}
	version, err := operation_setting.UpdatePartnerContent("test-persist", "c", "n", persist)
	if err != nil {
		t.Fatalf("content: %v", err)
	}
	if version != 1 {
		t.Fatalf("version = %d, want 1", version)
	}
	// A persistence failure rolls the in-memory change back.
	failing := func(key, value string) error { return errTestPersist }
	if _, err := operation_setting.UpdatePartnerContent("test-persist", "bad", "bad", failing); err == nil {
		t.Fatal("failing persist accepted")
	}
	found, ok := operation_setting.FindPartner("test-persist")
	if !ok || found.Contact != "c" || found.Version != 1 {
		t.Fatalf("rollback broken: %+v", found)
	}
	// Hydration restores the serialized snapshot.
	operation_setting.LoadPartnerSettingFromJSONString(raw)
	restored, ok := operation_setting.FindPartner("test-persist")
	if !ok || len(restored.InviterUserIDs) != 1 || restored.InviterUserIDs[0] != 42 {
		t.Fatalf("hydrate broken: %+v", restored)
	}
	// Corrupt payloads never clobber attribution.
	operation_setting.LoadPartnerSettingFromJSONString("{broken")
	if _, ok := operation_setting.FindPartner("test-persist"); !ok {
		t.Fatal("corrupt payload wiped config")
	}
	operation_setting.LoadPartnerSettingFromJSONString("")
	if _, ok := operation_setting.FindPartner("test-persist"); !ok {
		t.Fatal("empty payload wiped config")
	}
}
