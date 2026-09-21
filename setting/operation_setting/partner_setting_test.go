package operation_setting_test

import (
	"encoding/json"
	"errors"
	"strings"
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

// TestPartnerInviteCodeValidation pins the rule that keeps console codes out of
// the site's aff-code namespace: real codes are exactly four characters, so a
// four-character partner code is refused.
func TestPartnerInviteCodeValidation(t *testing.T) {
	for _, bad := range []string{"", "O30", "O30E", "with space", "sla/sh", strings.Repeat("x", 33)} {
		if err := operation_setting.ValidatePartnerInviteCode(bad); err == nil {
			t.Fatalf("code %q accepted", bad)
		}
	}
	for _, good := range []string{"O30E-G1", "TOMMY-1", "abcde", strings.Repeat("x", 32)} {
		if err := operation_setting.ValidatePartnerInviteCode(good); err != nil {
			t.Fatalf("code %q rejected: %v", good, err)
		}
	}
	if err := operation_setting.ValidatePartnerInviteCodeInviterID(7); err == nil {
		t.Fatal("real-account id accepted as a synthetic inviter id")
	}
	if err := operation_setting.ValidatePartnerInviteCodeInviterID(operation_setting.PartnerSyntheticInviterIDFloor); err != nil {
		t.Fatalf("floor id rejected: %v", err)
	}
}

func TestUpsertPartnerInviteCodesReplacesAndResolves(t *testing.T) {
	first := operation_setting.PartnerSyntheticInviterIDFloor + 1
	second := operation_setting.PartnerSyntheticInviterIDFloor + 2
	entry, err := operation_setting.UpsertPartnerInviteCodes("test-codes", []operation_setting.PartnerInviteCode{
		{Code: "TEST-CODES-1", InviterID: first},
		{Code: "TEST-CODES-2", InviterID: second},
	}, nil)
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if len(entry.InviteCodes) != 2 {
		t.Fatalf("unexpected codes: %+v", entry)
	}
	if id, found := operation_setting.FindInviterIDByPartnerCode("TEST-CODES-2"); !found || id != second {
		t.Fatalf("code did not resolve: id=%d found=%v", id, found)
	}
	if !operation_setting.IsPartnerCodeInviter(second) {
		t.Fatal("code inviter not recognised")
	}
	if owner, found := operation_setting.FindPartnerByInviter(second); !found || owner.ID != "test-codes" {
		t.Fatalf("code inviter not attributed to its partner: %+v %v", owner, found)
	}

	// A real account keeps the site's own invite bookkeeping.
	if _, err := operation_setting.UpsertPartnerMembers("test-codes", []int{4242}, nil); err != nil {
		t.Fatalf("members: %v", err)
	}
	if operation_setting.IsPartnerCodeInviter(4242) {
		t.Fatal("mapped real account treated as a code inviter")
	}
	if owner, found := operation_setting.FindPartnerByInviter(4242); !found || owner.ID != "test-codes" {
		t.Fatalf("real account lost its partner: %+v %v", owner, found)
	}

	// Replace semantics: the console owns the list, so a dropped code stops
	// resolving while the kept one survives.
	if _, err := operation_setting.UpsertPartnerInviteCodes("test-codes", []operation_setting.PartnerInviteCode{
		{Code: "TEST-CODES-1", InviterID: first},
	}, nil); err != nil {
		t.Fatalf("replace: %v", err)
	}
	if _, found := operation_setting.FindInviterIDByPartnerCode("TEST-CODES-2"); found {
		t.Fatal("dropped code still resolves")
	}
	if id, found := operation_setting.FindInviterIDByPartnerCode("TEST-CODES-1"); !found || id != first {
		t.Fatalf("kept code lost: id=%d found=%v", id, found)
	}
	// The replaced list left the member list alone.
	if owner, found := operation_setting.FindPartnerByInviter(4242); !found || owner.ID != "test-codes" {
		t.Fatalf("member list was clobbered: %+v %v", owner, found)
	}
}

func TestUpsertPartnerInviteCodesRejectsBadInput(t *testing.T) {
	valid := operation_setting.PartnerInviteCode{Code: "TEST-CLASH-1", InviterID: operation_setting.PartnerSyntheticInviterIDFloor + 10}
	if _, err := operation_setting.UpsertPartnerInviteCodes("test-clash-a", []operation_setting.PartnerInviteCode{valid}, nil); err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	// The same code under another partner would attribute to whichever entry
	// the registration scan reaches first.
	if _, err := operation_setting.UpsertPartnerInviteCodes("test-clash-b", []operation_setting.PartnerInviteCode{valid}, nil); err == nil {
		t.Fatal("cross-partner duplicate accepted")
	}
	if _, err := operation_setting.UpsertPartnerInviteCodes("test-clash-c", []operation_setting.PartnerInviteCode{
		{Code: "TEST-CLASH-2", InviterID: operation_setting.PartnerSyntheticInviterIDFloor + 11},
		{Code: "TEST-CLASH-2", InviterID: operation_setting.PartnerSyntheticInviterIDFloor + 12},
	}, nil); err == nil {
		t.Fatal("duplicate within one request accepted")
	}
	if _, err := operation_setting.UpsertPartnerInviteCodes("test-clash-d", []operation_setting.PartnerInviteCode{
		{Code: "TEST-CLASH-3", InviterID: 12},
	}, nil); err == nil {
		t.Fatal("real-account inviter id accepted")
	}
	if _, err := operation_setting.UpsertPartnerInviteCodes("", nil, nil); err == nil {
		t.Fatal("empty partner id accepted")
	}
}

// TestPartnerInviteCodeLoadSanitizes makes a hand-edited option row harmless:
// a code that could shadow a real aff code, or an id outside the reserved
// range, is dropped instead of being trusted.
func TestPartnerInviteCodeLoadSanitizes(t *testing.T) {
	raw := `{"partners":[{"id":"test-sanitize","invite_codes":[` +
		`{"code":"O30E","inviter_id":1000000001},` +
		`{"code":"TEST-SANITIZE-OK","inviter_id":1000000002},` +
		`{"code":"TEST-SANITIZE-BADID","inviter_id":77},` +
		`{"code":"TEST-SANITIZE-OK","inviter_id":1000000003}]}]}`
	operation_setting.LoadPartnerSettingFromJSONString(raw)
	if id, found := operation_setting.FindInviterIDByPartnerCode("TEST-SANITIZE-OK"); !found || id != 1000000002 {
		t.Fatalf("valid code lost: id=%d found=%v", id, found)
	}
	if _, found := operation_setting.FindInviterIDByPartnerCode("O30E"); found {
		t.Fatal("four-character code survived hydration")
	}
	if _, found := operation_setting.FindInviterIDByPartnerCode("TEST-SANITIZE-BADID"); found {
		t.Fatal("out-of-range inviter id survived hydration")
	}
	if operation_setting.IsPartnerCodeInviter(77) {
		t.Fatal("real-account id treated as a code inviter after hydration")
	}
}
