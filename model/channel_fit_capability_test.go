package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// setupFitCapabilityDB gives each test its own SQLite database carrying the
// channel and capability tables.
func setupFitCapabilityDB(t *testing.T) {
	t.Helper()
	previousDB := DB
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&Channel{}, &Ability{}, &ChannelFitCapability{}))
	DB = db
	t.Cleanup(func() {
		DB = previousDB
		ResetFitCapabilityIndexForTest()
	})
}

func suiteWrite(revision int64) FitCapabilityWrite {
	return FitCapabilityWrite{
		ChannelId:        7,
		Family:           "kimi-k3",
		Model:            "kimi-k3",
		Behavior:         "tools.dynamic_names",
		Supported:        true,
		Source:           FitCapabilitySourceSuite,
		Suite:            "cdp-k3",
		Cases:            "30/30",
		Rounds:           3,
		At:               1_700_000_000,
		PolicyVersion:    1,
		PolicyHash:       "policy-a",
		BaselineHash:     "baseline-a",
		ReportId:         "report-1",
		RunId:            "run-1",
		ExpectedRevision: revision,
	}
}

func TestFitCapabilityWriteValidatesInput(t *testing.T) {
	setupFitCapabilityDB(t)

	cases := map[string]func(*FitCapabilityWrite){
		"missing channel":   func(w *FitCapabilityWrite) { w.ChannelId = 0 },
		"missing family":    func(w *FitCapabilityWrite) { w.Family = " " },
		"missing behavior":  func(w *FitCapabilityWrite) { w.Behavior = "" },
		"unknown source":    func(w *FitCapabilityWrite) { w.Source = "guesswork" },
		"negative revision": func(w *FitCapabilityWrite) { w.ExpectedRevision = -1 },
		"force on manual":   func(w *FitCapabilityWrite) { w.Source = FitCapabilitySourceManual; w.Force = true },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			write := suiteWrite(0)
			mutate(&write)
			_, err := ApplyChannelFitCapability(write, 1_700_000_100)
			require.Error(t, err)
			require.False(t, IsFitCapabilityConflict(err), "a bad request is not a CAS conflict")
		})
	}
}

func TestFitCapabilityCASLifecycle(t *testing.T) {
	setupFitCapabilityDB(t)

	created, err := ApplyChannelFitCapability(suiteWrite(0), 1_700_000_100)
	require.NoError(t, err)
	require.Equal(t, int64(1), created.Revision)
	require.Equal(t, "kimi-k3", created.Family)

	// A writer that read revision 1 may advance it.
	second := suiteWrite(1)
	second.Cases = "30/30"
	updated, err := ApplyChannelFitCapability(second, 1_700_000_200)
	require.NoError(t, err)
	require.Equal(t, int64(2), updated.Revision)

	// The same expected revision is now stale: the classic lost-update case.
	_, err = ApplyChannelFitCapability(suiteWrite(1), 1_700_000_300)
	require.Error(t, err)
	require.True(t, IsFitCapabilityConflict(err))
	var conflict *FitCapabilityConflictError
	require.ErrorAs(t, err, &conflict)
	require.NotNil(t, conflict.Current)
	require.Equal(t, int64(2), conflict.Current.Revision, "the conflict must report the stored revision so the caller can retry from it")

	// expected_revision 0 against an existing row is a conflict too: it asserts
	// "nothing exists yet" and that assertion is false.
	_, err = ApplyChannelFitCapability(suiteWrite(0), 1_700_000_400)
	require.True(t, IsFitCapabilityConflict(err))

	// A non-zero revision with no row is likewise a conflict.
	_, err = ApplyChannelFitCapability(FitCapabilityWrite{
		ChannelId: 99, Family: "kimi-k3", Model: "kimi-k3", Behavior: "x",
		Source: FitCapabilitySourceSuite, ExpectedRevision: 4,
	}, 1_700_000_500)
	require.True(t, IsFitCapabilityConflict(err))

	// The stored row is still the second write: a rejected write must not land.
	stored, found, err := GetChannelFitCapability(7, "KIMI-K3", " kimi-k3 ", "TOOLS.DYNAMIC_NAMES")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, int64(2), stored.Revision)
}

// TestFitCapabilityInsertRaceReportsConflict covers the first-insert race: two
// writers both believe the row does not exist, and the unique key decides. The
// loser must learn the current state instead of silently overwriting the winner.
func TestFitCapabilityInsertRaceReportsConflict(t *testing.T) {
	setupFitCapabilityDB(t)

	first, err := ApplyChannelFitCapability(suiteWrite(0), 1_700_000_100)
	require.NoError(t, err)
	require.Equal(t, int64(1), first.Revision)

	race := suiteWrite(0)
	race.Supported = false
	race.Cases = "0/30"
	_, err = ApplyChannelFitCapability(race, 1_700_000_200)
	require.Error(t, err)
	require.True(t, IsFitCapabilityConflict(err))
	var conflict *FitCapabilityConflictError
	require.ErrorAs(t, err, &conflict)
	require.NotNil(t, conflict.Current)
	require.True(t, conflict.Current.Supported, "the loser must observe the winner's value")

	stored, _, err := GetChannelFitCapability(7, "kimi-k3", "kimi-k3", "tools.dynamic_names")
	require.NoError(t, err)
	require.True(t, stored.Supported)
	require.Equal(t, int64(1), stored.Revision, "a rejected insert must not bump the revision")
}

func TestFitCapabilityManualIsStickyAgainstSuite(t *testing.T) {
	setupFitCapabilityDB(t)

	manual := FitCapabilityWrite{
		ChannelId: 7, Family: "kimi-k3", Model: "kimi-k3", Behavior: "usage.thinking_counting",
		Supported: true, Source: FitCapabilitySourceManual, At: 1_700_000_000,
		ExpectedRevision: 0,
	}
	row, err := ApplyChannelFitCapability(manual, 1_700_000_100)
	require.NoError(t, err)
	require.Equal(t, int64(1), row.Revision)

	// A measured result must not silently replace an operator's live mark.
	measured := manual
	measured.Source = FitCapabilitySourceSuite
	measured.Suite = "cdp-k3"
	measured.ExpectedRevision = 1
	_, err = ApplyChannelFitCapability(measured, 1_700_000_200)
	require.Error(t, err)
	require.True(t, IsFitCapabilityConflict(err))

	// With force it may, and the write records that it did.
	measured.Force = true
	forced, err := ApplyChannelFitCapability(measured, 1_700_000_300)
	require.NoError(t, err)
	require.Equal(t, int64(2), forced.Revision)
	require.True(t, forced.Force)
	require.Equal(t, FitCapabilitySourceSuite, forced.Source)

	// An expired manual mark is not sticky, so no force is needed.
	expiring := manual
	expiring.Source = FitCapabilitySourceManual
	expiring.ExpectedRevision = 2
	expiring.ExpiresAt = 1_700_000_350
	expiredRow, err := ApplyChannelFitCapability(expiring, 1_700_000_400)
	require.NoError(t, err)
	require.Equal(t, int64(3), expiredRow.Revision)

	replacement := manual
	replacement.Source = FitCapabilitySourceSuite
	replacement.Suite = "cdp-k3"
	replacement.ExpectedRevision = 3
	replacement.At = 1_700_000_400
	replaced, err := ApplyChannelFitCapability(replacement, 1_700_000_500)
	require.NoError(t, err, "an expired manual mark must fall back to the measured result")
	require.Equal(t, int64(4), replaced.Revision)
}

func TestFitCapabilityStateMachine(t *testing.T) {
	const day = int64(24 * 60 * 60)
	now := int64(1_700_000_000)

	cases := []struct {
		name string
		row  *ChannelFitCapability
		want string
	}{
		{name: "no row is unknown", row: nil, want: FitCapabilityUnknown},
		{
			name: "fresh suite pass",
			row:  &ChannelFitCapability{Source: FitCapabilitySourceSuite, Supported: true, At: now - day},
			want: FitCapabilitySuiteFresh,
		},
		{
			name: "suite pass past the window is stale",
			row:  &ChannelFitCapability{Source: FitCapabilitySourceSuite, Supported: true, At: now - 40*day},
			want: FitCapabilitySuiteStale,
		},
		{
			name: "suite negative is an explicit failure, not staleness",
			row:  &ChannelFitCapability{Source: FitCapabilitySourceSuite, Supported: false, At: now - 40*day},
			want: FitCapabilitySuiteFailed,
		},
		{
			name: "policy change invalidates a fresh suite result",
			row:  &ChannelFitCapability{Source: FitCapabilitySourceSuite, Supported: true, At: now - day, PolicyHash: "policy-a"},
			want: FitCapabilitySuiteStale,
		},
		{
			name: "baseline change invalidates a fresh suite result",
			row:  &ChannelFitCapability{Source: FitCapabilitySourceSuite, Supported: true, At: now - day, PolicyHash: "policy-b", BaselineHash: "baseline-a"},
			want: FitCapabilitySuiteStale,
		},
		{
			name: "manual mark without expiry is active",
			row:  &ChannelFitCapability{Source: FitCapabilitySourceManual, Supported: true, At: now - 400*day},
			want: FitCapabilityManualActive,
		},
		{
			name: "manual mark past its expiry expires",
			row:  &ChannelFitCapability{Source: FitCapabilitySourceManual, Supported: true, At: now - 400*day, ExpiresAt: now - day},
			want: FitCapabilityManualExpired,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			policyHash, baselineHash := "policy-b", "baseline-b"
			got := FitCapabilityState(testCase.row, now, DefaultFitCapabilityStaleAfterDays, policyHash, baselineHash)
			assert.Equal(t, testCase.want, got)
		})
	}

	// Only a fresh measured pass or a live operator mark may satisfy a
	// requirement; a false suite result is a claim, not ignorance.
	assert.True(t, FitCapabilityStateSatisfies(FitCapabilitySuiteFresh))
	assert.True(t, FitCapabilityStateSatisfies(FitCapabilityManualActive))
	for _, state := range []string{
		FitCapabilityUnknown, FitCapabilitySuiteStale, FitCapabilitySuiteFailed, FitCapabilityManualExpired,
	} {
		assert.Falsef(t, FitCapabilityStateSatisfies(state), "%s must not satisfy a conservative requirement", state)
	}
}

func TestFitCapabilityIndexIsConservativeAndFailsOpen(t *testing.T) {
	setupFitCapabilityDB(t)

	// An unbuilt index verifies nothing.
	ResetFitCapabilityIndexForTest()
	require.False(t, FitCapabilityIndexReady())
	require.False(t, ChannelSatisfiesFitMarks(7, "kimi-k3", []string{"tools.dynamic_names"}, false))
	require.True(t, ChannelSatisfiesFitMarks(7, "kimi-k3", nil, false), "an empty requirement is satisfied by definition")

	// A permissive policy does not treat absence as a failure, but it also does
	// not invent a positive result.
	require.True(t, ChannelSatisfiesFitMarks(7, "kimi-k3", []string{"tools.dynamic_names"}, true))

	now := common.GetTimestamp()
	_, err := ApplyChannelFitCapability(FitCapabilityWrite{
		ChannelId: 7, Family: "kimi-k3", Model: "kimi-k3", Behavior: "tools.dynamic_names",
		Supported: true, Source: FitCapabilitySourceSuite, At: now, ExpectedRevision: 0,
	}, now)
	require.NoError(t, err)

	InitFitCapabilityIndex()
	require.True(t, FitCapabilityIndexReady())
	require.True(t, ChannelSatisfiesFitMarks(7, "kimi-k3", []string{"tools.dynamic_names"}, false))
	require.False(t, ChannelSatisfiesFitMarks(7, "kimi-k3", []string{"tools.dynamic_names", "history.assistant_first"}, false),
		"every required behaviour must be verified, not just one")
	require.False(t, ChannelSatisfiesFitMarks(8, "kimi-k3", []string{"tools.dynamic_names"}, false),
		"a mark belongs to exactly one channel")

	// A stored failure never satisfies, under either policy.
	_, err = ApplyChannelFitCapability(FitCapabilityWrite{
		ChannelId: 9, Family: "kimi-k3", Model: "kimi-k3", Behavior: "tools.choice_semantics",
		Supported: false, Source: FitCapabilitySourceSuite, At: now, ExpectedRevision: 0,
	}, now)
	require.NoError(t, err)
	InitFitCapabilityIndex()
	require.False(t, ChannelSatisfiesFitMarks(9, "kimi-k3", []string{"tools.choice_semantics"}, false))
	require.False(t, ChannelSatisfiesFitMarks(9, "kimi-k3", []string{"tools.choice_semantics"}, true),
		"an explicit negative result is not the same as an unknown one")
}

func TestChannelDeletesRemoveCapabilities(t *testing.T) {
	setupFitCapabilityDB(t)

	for _, id := range []int{1, 2, 3} {
		require.NoError(t, DB.Create(&Channel{
			Id: id, Name: "ch", Status: common.ChannelStatusEnabled, Group: "default", Models: "kimi-k3", Key: "sk",
		}).Error)
		_, err := ApplyChannelFitCapability(FitCapabilityWrite{
			ChannelId: id, Family: "kimi-k3", Model: "kimi-k3", Behavior: "tools.dynamic_names",
			Supported: true, Source: FitCapabilitySourceSuite, At: 1_700_000_000, ExpectedRevision: 0,
		}, 1_700_000_100)
		require.NoError(t, err)
	}

	require.NoError(t, (&Channel{Id: 1}).Delete())
	rows, err := ListChannelFitCapabilities(1)
	require.NoError(t, err)
	require.Empty(t, rows, "deleting a channel must not leave orphan capability marks")

	if _, err := BatchDeleteChannels([]int{2, 3}); err != nil {
		t.Fatalf("batch delete: %v", err)
	}
	all, err := ListAllChannelFitCapabilities()
	require.NoError(t, err)
	require.Empty(t, all, "batch delete must clean capability marks for every removed channel")
}
