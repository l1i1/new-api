package model

import (
	"errors"
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/console_setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"gorm.io/gorm"
)

const retiredThemeOptionKey = "theme.frontend"

type legacyOptionTransform func(string) (string, error)

// MigrateRetiredFrontendOptions normalizes options that belonged to the
// removed dashboard frontend. Each legacy console setting is migrated in its
// own transaction so one malformed value cannot block the other settings.
func MigrateRetiredFrontendOptions() error {
	if DB == nil {
		return errors.New("database is not initialized")
	}

	var migrationErrors []error
	if err := normalizeRetiredThemeOption(); err != nil {
		migrationErrors = append(migrationErrors, fmt.Errorf("normalize %s: %w", retiredThemeOptionKey, err))
	}

	migrations := []struct {
		source    string
		target    string
		transform legacyOptionTransform
	}{
		{source: "ApiInfo", target: "console_setting.api_info", transform: transformLegacyAPIInfo},
		{source: "Announcements", target: "console_setting.announcements", transform: transformLegacyAnnouncements},
		{source: "FAQ", target: "console_setting.faq", transform: transformLegacyFAQ},
	}
	for _, migration := range migrations {
		if err := migrateLegacyOption(migration.source, migration.target, migration.transform); err != nil {
			migrationErrors = append(migrationErrors, err)
		}
	}
	if err := migrateLegacyUptimeOptions(); err != nil {
		migrationErrors = append(migrationErrors, err)
	}
	if err := migrateForceRetryStatusCodes(); err != nil {
		migrationErrors = append(migrationErrors, err)
	}
	return errors.Join(migrationErrors...)
}

// migrateForceRetryStatusCodes merges the retired force-retry list into the
// failover list, which is the single field the Routing Reliability page edits
// now. Both lists meant "this channel cannot serve the request, try another",
// so the union preserves behavior while the page shows one decision instead of
// two. The source row is deleted, which makes the migration idempotent and
// keeps a later options sync from re-adding codes an operator has since removed.
func migrateForceRetryStatusCodes() error {
	return DB.Transaction(func(tx *gorm.DB) error {
		var force Option
		if err := tx.Where(&Option{Key: "ForceRetryStatusCodes"}).First(&force).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			return fmt.Errorf("read legacy option ForceRetryStatusCodes: %w", err)
		}
		if strings.TrimSpace(force.Value) == "" {
			return tx.Delete(&force).Error
		}

		var automatic Option
		err := tx.Where(&Option{Key: "AutomaticRetryStatusCodes"}).First(&automatic).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("read option AutomaticRetryStatusCodes: %w", err)
		}
		// With no persisted row the effective value is the compiled default, so
		// the union must start from that: writing the legacy codes alone would
		// silently drop the defaults for an install that never saved the field.
		base := automatic.Value
		if errors.Is(err, gorm.ErrRecordNotFound) {
			base = operation_setting.AutomaticRetryStatusCodesToString()
			automatic = Option{Key: "AutomaticRetryStatusCodes"}
		}
		merged, mergeErr := operation_setting.MergeRetryStatusCodes(base, force.Value)
		if mergeErr != nil {
			// Leave both rows untouched: ShouldRetryByStatusCode still unions the
			// in-memory lists, and an operator can fix the malformed entry.
			common.SysError(fmt.Sprintf("force-retry status codes were not merged: %v", mergeErr))
			return nil
		}
		automatic.Value = merged
		if err := tx.Save(&automatic).Error; err != nil {
			return fmt.Errorf("write option AutomaticRetryStatusCodes: %w", err)
		}
		return tx.Delete(&force).Error
	})
}

func normalizeRetiredThemeOption() error {
	return DB.Transaction(func(tx *gorm.DB) error {
		var option Option
		err := tx.Where(&Option{Key: retiredThemeOptionKey}).First(&option).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return tx.Create(&Option{Key: retiredThemeOptionKey, Value: "default"}).Error
		}
		if err != nil {
			return err
		}
		if option.Value == "default" {
			return nil
		}
		return tx.Model(&option).Update("value", "default").Error
	})
}

func migrateLegacyOption(sourceKey, targetKey string, transform legacyOptionTransform) error {
	return DB.Transaction(func(tx *gorm.DB) error {
		var source Option
		if err := tx.Where(&Option{Key: sourceKey}).First(&source).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			return fmt.Errorf("read legacy option %s: %w", sourceKey, err)
		}

		var target Option
		err := tx.Where(&Option{Key: targetKey}).First(&target).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("read target option %s: %w", targetKey, err)
		}
		if err == nil {
			return tx.Delete(&source).Error
		}

		value, transformErr := transform(source.Value)
		if transformErr != nil {
			common.SysError(fmt.Sprintf("legacy option %s was not migrated: %v", sourceKey, transformErr))
			return nil
		}
		if errors.Is(err, gorm.ErrRecordNotFound) {
			target = Option{Key: targetKey}
		}
		target.Value = value
		if err := tx.Save(&target).Error; err != nil {
			return fmt.Errorf("write target option %s: %w", targetKey, err)
		}
		if err := tx.Delete(&source).Error; err != nil {
			return fmt.Errorf("delete legacy option %s: %w", sourceKey, err)
		}
		return nil
	})
}

func transformLegacyAPIInfo(value string) (string, error) {
	if strings.TrimSpace(value) == "" {
		return "", errors.New("value is empty")
	}
	var items []map[string]any
	if err := common.UnmarshalJsonStr(value, &items); err != nil {
		return "", err
	}
	if len(items) > 50 {
		items = items[:50]
	}
	encoded, err := common.Marshal(items)
	if err != nil {
		return "", err
	}
	result := string(encoded)
	if err := console_setting.ValidateConsoleSettings(result, "ApiInfo"); err != nil {
		return "", err
	}
	return result, nil
}

func transformLegacyAnnouncements(value string) (string, error) {
	if strings.TrimSpace(value) == "" {
		return "", errors.New("value is empty")
	}
	if err := console_setting.ValidateConsoleSettings(value, "Announcements"); err != nil {
		return "", err
	}
	return value, nil
}

func transformLegacyFAQ(value string) (string, error) {
	if strings.TrimSpace(value) == "" {
		return "", errors.New("value is empty")
	}
	var legacyItems []map[string]any
	if err := common.UnmarshalJsonStr(value, &legacyItems); err != nil {
		return "", err
	}
	items := make([]map[string]any, 0, len(legacyItems))
	for index, item := range legacyItems {
		question, _ := item["question"].(string)
		if strings.TrimSpace(question) == "" {
			question, _ = item["title"].(string)
		}
		answer, _ := item["answer"].(string)
		if strings.TrimSpace(answer) == "" {
			answer, _ = item["content"].(string)
		}
		if strings.TrimSpace(question) == "" || strings.TrimSpace(answer) == "" {
			return "", fmt.Errorf("FAQ entry %d is missing a question or answer", index)
		}
		items = append(items, map[string]any{"question": question, "answer": answer})
	}
	if len(items) > 50 {
		items = items[:50]
	}
	encoded, err := common.Marshal(items)
	if err != nil {
		return "", err
	}
	result := string(encoded)
	if err := console_setting.ValidateConsoleSettings(result, "FAQ"); err != nil {
		return "", err
	}
	return result, nil
}

func migrateLegacyUptimeOptions() error {
	return DB.Transaction(func(tx *gorm.DB) error {
		var urlOption Option
		urlErr := tx.Where(&Option{Key: "UptimeKumaUrl"}).First(&urlOption).Error
		if urlErr != nil && !errors.Is(urlErr, gorm.ErrRecordNotFound) {
			return fmt.Errorf("read legacy option UptimeKumaUrl: %w", urlErr)
		}
		var slugOption Option
		slugErr := tx.Where(&Option{Key: "UptimeKumaSlug"}).First(&slugOption).Error
		if slugErr != nil && !errors.Is(slugErr, gorm.ErrRecordNotFound) {
			return fmt.Errorf("read legacy option UptimeKumaSlug: %w", slugErr)
		}
		if errors.Is(urlErr, gorm.ErrRecordNotFound) && errors.Is(slugErr, gorm.ErrRecordNotFound) {
			return nil
		}

		var target Option
		targetErr := tx.Where(&Option{Key: "console_setting.uptime_kuma_groups"}).First(&target).Error
		if targetErr != nil && !errors.Is(targetErr, gorm.ErrRecordNotFound) {
			return fmt.Errorf("read target option console_setting.uptime_kuma_groups: %w", targetErr)
		}
		if targetErr == nil {
			if urlErr == nil {
				if err := tx.Delete(&urlOption).Error; err != nil {
					return err
				}
			}
			if slugErr == nil {
				return tx.Delete(&slugOption).Error
			}
			return nil
		}

		if urlErr != nil || slugErr != nil || strings.TrimSpace(urlOption.Value) == "" || strings.TrimSpace(slugOption.Value) == "" {
			common.SysError("legacy Uptime Kuma options were not migrated: both URL and slug are required")
			return nil
		}
		groups := []map[string]any{{
			"id":           1,
			"categoryName": "old",
			"url":          urlOption.Value,
			"slug":         slugOption.Value,
			"description":  "",
		}}
		encoded, err := common.Marshal(groups)
		if err != nil {
			return err
		}
		value := string(encoded)
		if err := console_setting.ValidateConsoleSettings(value, "UptimeKumaGroups"); err != nil {
			common.SysError(fmt.Sprintf("legacy Uptime Kuma options were not migrated: %v", err))
			return nil
		}
		if errors.Is(targetErr, gorm.ErrRecordNotFound) {
			target = Option{Key: "console_setting.uptime_kuma_groups"}
		}
		target.Value = value
		if err := tx.Save(&target).Error; err != nil {
			return fmt.Errorf("write target option console_setting.uptime_kuma_groups: %w", err)
		}
		if err := tx.Delete(&urlOption).Error; err != nil {
			return err
		}
		return tx.Delete(&slugOption).Error
	})
}
