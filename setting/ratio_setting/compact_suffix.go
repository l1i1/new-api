package ratio_setting

import "strings"

const CompactModelSuffix = "-openai-compact"
const CompactWildcardModelKey = "*" + CompactModelSuffix

func WithCompactModelSuffix(modelName string) string {
	if strings.HasSuffix(modelName, CompactModelSuffix) {
		return modelName
	}
	return modelName + CompactModelSuffix
}

func CompactBaseModelName(modelName string) (string, bool) {
	if !strings.HasSuffix(modelName, CompactModelSuffix) {
		return "", false
	}
	baseModel := strings.TrimSuffix(modelName, CompactModelSuffix)
	if baseModel == "" {
		return "", false
	}
	return baseModel, true
}

func WithCompactModelVariants(models []string) []string {
	variants := make([]string, 0, len(models)*2)
	seen := make(map[string]struct{}, len(models)*2)
	for _, model := range models {
		if _, ok := seen[model]; ok {
			continue
		}
		seen[model] = struct{}{}
		variants = append(variants, model)
	}
	for _, model := range models {
		compactModel := WithCompactModelSuffix(model)
		if _, ok := seen[compactModel]; ok {
			continue
		}
		seen[compactModel] = struct{}{}
		variants = append(variants, compactModel)
	}
	return variants
}
