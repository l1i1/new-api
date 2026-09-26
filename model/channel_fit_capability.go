package model

import (
	"errors"
	"fmt"
	"strings"

	"gorm.io/gorm"
)

// Channel fit capabilities: the behaviour-level marks a channel has actually
// been measured to reproduce.
//
// This is a separate table rather than a field inside channels.settings on
// purpose. The channel row is saved whole by the generic UpdateChannel path, so
// anything living in that JSON would be silently reset by an unrelated edit; a
// dedicated table also lets a concurrent suite write be a conditional UPDATE
// instead of a read-modify-write of a large document.
//
// official_fit_models keeps its current meaning as the coarse official-behaviour
// source. This table only answers the narrower question "has this behaviour been
// verified on this channel", and an absent row means unknown, which keeps every
// existing request on today's behaviour.

const (
	// FitCapabilitySourceSuite marks a result produced by an automated suite run.
	FitCapabilitySourceSuite = "suite"
	// FitCapabilitySourceManual marks an operator override. Manual results are
	// sticky: a suite write cannot replace one unless it sets Force.
	FitCapabilitySourceManual = "manual"

	// DefaultFitCapabilityStaleAfterDays is how long a suite result stays fresh.
	DefaultFitCapabilityStaleAfterDays = 30
)

// Fit capability states. Each family/model × behaviour pair holds exactly one.
const (
	// FitCapabilityUnknown means no row exists: nothing is claimed either way.
	FitCapabilityUnknown = "unknown"
	// FitCapabilitySuiteFresh is a passing suite result inside the freshness window.
	FitCapabilitySuiteFresh = "suite_fresh"
	// FitCapabilitySuiteStale is a passing suite result past the window, or one
	// bound to a different policy/baseline. It cannot satisfy a requirement.
	FitCapabilitySuiteStale = "suite_stale"
	// FitCapabilitySuiteFailed is an explicit negative suite result. It is a
	// claim, not an absence of information.
	FitCapabilitySuiteFailed = "suite_failed"
	// FitCapabilityManualActive is an unexpired operator mark.
	FitCapabilityManualActive = "manual_active"
	// FitCapabilityManualExpired is an operator mark past its expiry. It falls
	// back to whatever the latest measured result says.
	FitCapabilityManualExpired = "manual_expired"
)

// ChannelFitCapability is one behaviour mark for one channel and model.
//
// Timestamps are Unix seconds (int64) rather than time.Time so the same schema
// migrates cleanly on SQLite, MySQL and PostgreSQL. ExpiresAt == 0 means "no
// expiry".
type ChannelFitCapability struct {
	Id        int    `json:"id" gorm:"primaryKey"`
	ChannelId int    `json:"channel_id" gorm:"uniqueIndex:idx_channel_fit_capability,priority:1;index:idx_channel_fit_cap_channel"`
	Family    string `json:"family" gorm:"type:varchar(64);uniqueIndex:idx_channel_fit_capability,priority:2"`
	Model     string `json:"model" gorm:"type:varchar(128);uniqueIndex:idx_channel_fit_capability,priority:3"`
	Behavior  string `json:"behavior" gorm:"type:varchar(64);uniqueIndex:idx_channel_fit_capability,priority:4"`

	Supported bool   `json:"supported"`
	Source    string `json:"source" gorm:"type:varchar(16)"`
	Suite     string `json:"suite" gorm:"type:varchar(64)"`
	Cases     string `json:"cases" gorm:"type:varchar(32)"`
	Rounds    int    `json:"rounds"`
	At        int64  `json:"at"`
	ExpiresAt int64  `json:"expires_at"`

	// Provenance binding. A suite result is only valid for the policy and
	// official baseline it was measured against; changing either makes it stale
	// rather than automatically valid.
	PolicyVersion int    `json:"policy_version"`
	PolicyHash    string `json:"policy_hash" gorm:"type:varchar(64)"`
	BaselineHash  string `json:"baseline_hash" gorm:"type:varchar(64)"`
	ReportId      string `json:"report_id" gorm:"type:varchar(128)"`
	RunId         string `json:"run_id" gorm:"type:varchar(128)"`

	// Force records that this write deliberately replaced a sticky manual mark.
	Force bool `json:"force"`

	// Revision is the CAS token. Every accepted write increments it by exactly
	// one, so a stale writer is rejected instead of overwriting a newer result.
	Revision  int64 `json:"revision"`
	UpdatedAt int64 `json:"updated_at"`
}

func (ChannelFitCapability) TableName() string {
	return "channel_fit_capabilities"
}

// FitCapabilityWrite is one requested capability change.
type FitCapabilityWrite struct {
	ChannelId     int
	Family        string
	Model         string
	Behavior      string
	Supported     bool
	Source        string
	Suite         string
	Cases         string
	Rounds        int
	At            int64
	ExpiresAt     int64
	PolicyVersion int
	PolicyHash    string
	BaselineHash  string
	ReportId      string
	RunId         string
	Force         bool
	// ExpectedRevision is mandatory. 0 means "no row may exist yet"; any other
	// value must match the stored revision exactly.
	ExpectedRevision int64
}

// FitCapabilityConflictError reports a rejected capability write. Every case is
// an HTTP 409 at the endpoint: the caller's view of the row was stale, or the
// write would have replaced a mark that is deliberately sticky.
type FitCapabilityConflictError struct {
	Reason  string
	Current *ChannelFitCapability
}

func (e *FitCapabilityConflictError) Error() string {
	if e == nil {
		return ""
	}
	return "fit capability conflict: " + e.Reason
}

// IsFitCapabilityConflict reports whether err is a CAS/sticky rejection.
func IsFitCapabilityConflict(err error) bool {
	var conflict *FitCapabilityConflictError
	return errors.As(err, &conflict)
}

func normalizeFitCapabilityToken(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func (w *FitCapabilityWrite) normalize() error {
	w.Family = normalizeFitCapabilityToken(w.Family)
	w.Model = normalizeFitCapabilityToken(w.Model)
	w.Behavior = normalizeFitCapabilityToken(w.Behavior)
	w.Source = normalizeFitCapabilityToken(w.Source)
	if w.ChannelId <= 0 {
		return fmt.Errorf("fit capability: channel_id is required")
	}
	if w.Family == "" || w.Model == "" || w.Behavior == "" {
		return fmt.Errorf("fit capability: family, model and behavior are required")
	}
	switch w.Source {
	case FitCapabilitySourceSuite, FitCapabilitySourceManual:
	default:
		return fmt.Errorf("fit capability: source must be %q or %q", FitCapabilitySourceSuite, FitCapabilitySourceManual)
	}
	if w.ExpectedRevision < 0 {
		return fmt.Errorf("fit capability: expected_revision must not be negative")
	}
	if w.Force && w.Source != FitCapabilitySourceSuite {
		return fmt.Errorf("fit capability: force only applies to a suite write")
	}
	return nil
}

// GetChannelFitCapability reads one mark. found=false means unknown.
func GetChannelFitCapability(channelID int, family, model, behavior string) (*ChannelFitCapability, bool, error) {
	row := &ChannelFitCapability{}
	err := DB.Where(
		"channel_id = ? AND family = ? AND model = ? AND behavior = ?",
		channelID, normalizeFitCapabilityToken(family), normalizeFitCapabilityToken(model), normalizeFitCapabilityToken(behavior),
	).First(row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return row, true, nil
}

// ListChannelFitCapabilities returns every mark for a channel.
func ListChannelFitCapabilities(channelID int) ([]ChannelFitCapability, error) {
	var rows []ChannelFitCapability
	err := DB.Where("channel_id = ?", channelID).Order("family, model, behavior").Find(&rows).Error
	return rows, err
}

// ListAllChannelFitCapabilities returns every mark, for the capability index.
func ListAllChannelFitCapabilities() ([]ChannelFitCapability, error) {
	var rows []ChannelFitCapability
	err := DB.Find(&rows).Error
	return rows, err
}

// ApplyChannelFitCapability performs one capability write under compare-and-swap.
//
// The CAS is a conditional UPDATE on the row's own revision, not a row lock:
// lockForUpdate silently drops FOR UPDATE on SQLite, so a lock-based design would
// be correct on MySQL and wrong on the dialect used by small deployments and by
// every test. The unique key covers the first-insert race; when an INSERT loses
// that race the row is re-read and reported as a conflict rather than retried
// blindly.
func ApplyChannelFitCapability(write FitCapabilityWrite, now int64) (*ChannelFitCapability, error) {
	if err := write.normalize(); err != nil {
		return nil, err
	}
	// A mark must belong to a channel that exists. Without this check a writer
	// could create exactly the orphan row the delete path works to prevent — and
	// it would be invisible, because nothing lists marks by channel id except the
	// channel that no longer exists.
	if err := ensureFitCapabilityChannelExists(write.ChannelId); err != nil {
		return nil, err
	}

	if write.ExpectedRevision == 0 {
		row := buildFitCapabilityRow(write, now, 1)
		if err := DB.Create(row).Error; err != nil {
			// Either the row appeared between the caller's read and this insert,
			// or the write genuinely failed. Re-read to tell them apart.
			if current, found, readErr := GetChannelFitCapability(write.ChannelId, write.Family, write.Model, write.Behavior); readErr == nil && found {
				return nil, &FitCapabilityConflictError{Reason: "a capability already exists for this channel/model/behaviour", Current: current}
			}
			return nil, err
		}
		return row, nil
	}

	current, found, err := GetChannelFitCapability(write.ChannelId, write.Family, write.Model, write.Behavior)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, &FitCapabilityConflictError{Reason: "no capability exists for this channel/model/behaviour"}
	}
	if current.Revision != write.ExpectedRevision {
		return nil, &FitCapabilityConflictError{Reason: "revision does not match the stored capability", Current: current}
	}
	if err := guardFitCapabilitySticky(current, write, now); err != nil {
		return nil, err
	}

	updates := map[string]any{
		"supported":      write.Supported,
		"source":         write.Source,
		"suite":          write.Suite,
		"cases":          write.Cases,
		"rounds":         write.Rounds,
		"at":             write.At,
		"expires_at":     write.ExpiresAt,
		"policy_version": write.PolicyVersion,
		"policy_hash":    write.PolicyHash,
		"baseline_hash":  write.BaselineHash,
		"report_id":      write.ReportId,
		"run_id":         write.RunId,
		"force":          write.Force,
		"updated_at":     now,
		// The revision increment is what makes RowsAffected a reliable CAS signal
		// on every dialect: MySQL reports zero affected rows when an UPDATE writes
		// the values a row already holds, so a conditional update that changed
		// nothing but matched a row would be indistinguishable from a lost race.
		// Incrementing the revision guarantees the row always differs.
		"revision": gorm.Expr("revision + 1"),
	}
	result := DB.Model(&ChannelFitCapability{}).
		Where(
			"channel_id = ? AND family = ? AND model = ? AND behavior = ? AND revision = ?",
			write.ChannelId, write.Family, write.Model, write.Behavior, write.ExpectedRevision,
		).
		Updates(updates)
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected != 1 {
		// The revision was correct when read but changed before the update
		// landed. Report the current state so the caller can retry from it.
		latest, _, _ := GetChannelFitCapability(write.ChannelId, write.Family, write.Model, write.Behavior)
		return nil, &FitCapabilityConflictError{Reason: "revision changed concurrently", Current: latest}
	}
	updated, _, err := GetChannelFitCapability(write.ChannelId, write.Family, write.Model, write.Behavior)
	return updated, err
}

// ensureFitCapabilityChannelExists rejects a write aimed at a channel that is
// not present. Channel rows are hard-deleted, so a plain count is authoritative.
func ensureFitCapabilityChannelExists(channelID int) error {
	var count int64
	if err := DB.Model(&Channel{}).Where("id = ?", channelID).Count(&count).Error; err != nil {
		return err
	}
	if count == 0 {
		return fmt.Errorf("fit capability: channel %d does not exist", channelID)
	}
	return nil
}

// guardFitCapabilitySticky enforces the conflict semantics: a measured result
// never silently replaces an operator's unexpired manual mark. An expired manual
// mark is not sticky, which is what stops a stale override from living forever.
func guardFitCapabilitySticky(current *ChannelFitCapability, write FitCapabilityWrite, now int64) error {
	if current.Source != FitCapabilitySourceManual || write.Source != FitCapabilitySourceSuite {
		return nil
	}
	if write.Force {
		return nil
	}
	if !FitCapabilityExpired(current, now) {
		return &FitCapabilityConflictError{
			Reason:  "a manual mark is still active; an automated write needs force",
			Current: current,
		}
	}
	return nil
}

func buildFitCapabilityRow(write FitCapabilityWrite, now int64, revision int64) *ChannelFitCapability {
	return &ChannelFitCapability{
		ChannelId:     write.ChannelId,
		Family:        write.Family,
		Model:         write.Model,
		Behavior:      write.Behavior,
		Supported:     write.Supported,
		Source:        write.Source,
		Suite:         write.Suite,
		Cases:         write.Cases,
		Rounds:        write.Rounds,
		At:            write.At,
		ExpiresAt:     write.ExpiresAt,
		PolicyVersion: write.PolicyVersion,
		PolicyHash:    write.PolicyHash,
		BaselineHash:  write.BaselineHash,
		ReportId:      write.ReportId,
		RunId:         write.RunId,
		Force:         write.Force,
		Revision:      revision,
		UpdatedAt:     now,
	}
}

// FitCapabilityExpired reports whether a manual mark's expiry has passed. A zero
// expiry never expires.
func FitCapabilityExpired(row *ChannelFitCapability, now int64) bool {
	return row != nil && row.ExpiresAt > 0 && now >= row.ExpiresAt
}

// FitCapabilityState resolves the state of one stored mark.
//
// policyHash and baselineHash are the values currently in force. A suite result
// measured against a different policy or baseline is stale rather than valid:
// the behaviour may have changed underneath it, and the spec forbids letting it
// recover silently.
func FitCapabilityState(row *ChannelFitCapability, now int64, staleAfterDays int, policyHash, baselineHash string) string {
	if row == nil {
		return FitCapabilityUnknown
	}
	if staleAfterDays <= 0 {
		staleAfterDays = DefaultFitCapabilityStaleAfterDays
	}
	switch row.Source {
	case FitCapabilitySourceManual:
		if FitCapabilityExpired(row, now) {
			return FitCapabilityManualExpired
		}
		return FitCapabilityManualActive
	case FitCapabilitySourceSuite:
		if !row.Supported {
			return FitCapabilitySuiteFailed
		}
		// An explicit expiry applies to a measured result too. Ignoring it would
		// let a suite row that asked to expire keep vouching for a channel.
		if FitCapabilityExpired(row, now) {
			return FitCapabilitySuiteStale
		}
		if policyHash != "" && row.PolicyHash != "" && row.PolicyHash != policyHash {
			return FitCapabilitySuiteStale
		}
		if baselineHash != "" && row.BaselineHash != "" && row.BaselineHash != baselineHash {
			return FitCapabilitySuiteStale
		}
		staleAfterSeconds := int64(staleAfterDays) * 24 * 60 * 60
		if row.At <= 0 || now-row.At > staleAfterSeconds {
			return FitCapabilitySuiteStale
		}
		return FitCapabilitySuiteFresh
	}
	return FitCapabilityUnknown
}

// FitCapabilityStateSatisfies reports whether a state may satisfy a requirement
// under the conservative policy. Only a fresh measured result or an unexpired
// operator mark counts; unknown, stale, failed and expired all mean "not
// verified", and a false result is an explicit negative rather than ignorance.
func FitCapabilityStateSatisfies(state string) bool {
	return state == FitCapabilitySuiteFresh || state == FitCapabilityManualActive
}

// DeleteChannelFitCapabilities removes every mark belonging to the given
// channels. Callers must run it inside the same transaction as the channel
// delete so a deleted channel cannot leave orphan marks behind.
//
// When the table does not exist yet there is nothing that could be orphaned, so
// this is a no-op rather than an error: a node that has not run the migration
// must still be able to delete a channel, and failing the delete would be a far
// worse outcome than an absent table. When the table does exist, cleanup
// failures propagate and roll the whole delete back.
func DeleteChannelFitCapabilities(tx *gorm.DB, channelIDs []int) error {
	if len(channelIDs) == 0 {
		return nil
	}
	if !tx.Migrator().HasTable(&ChannelFitCapability{}) {
		return nil
	}
	return tx.Where("channel_id IN ?", channelIDs).Delete(&ChannelFitCapability{}).Error
}
