package helper

import (
	"net/http"
	"strings"
)

// deepSeekV4OfficialStatus is the status the live endpoint uses for a
// validation text. The direct-official baseline captured against
// api.deepseek.com on 2026-10-20 (and a re-probe on 2026-10-21) separates the
// classes: the serde deserialization class is 422, the body-parse class and the
// business rejections are 400. Tests use this helper so a table entry cannot
// assert the status the code happens to return instead of the official one.
func deepSeekV4OfficialStatus(message string) int {
	if strings.HasPrefix(message, "Failed to deserialize the JSON body into the target type") {
		return http.StatusUnprocessableEntity
	}
	return http.StatusBadRequest
}
