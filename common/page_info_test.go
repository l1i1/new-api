package common

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

func pageQueryFor(t *testing.T, target string) *PageInfo {
	t.Helper()
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, target, nil)
	return GetPageQuery(c)
}

// TestGetPageQueryBoundsPageSizeBelowAsWellAsAbove is the root-cause gate for
// the paging contract.
//
// The upper bound was the only one enforced, and a negative size passed straight
// through to db.Limit(-1). GORM writes a LIMIT clause only when the value is >= 0
// (gorm.io/gorm/clause.Limit.Build), and it drops OFFSET with it, so a single
// query parameter turned every paged listing in this repository — channel, token,
// log, user, redemption — into a full-table export. The helper is the only place
// all of them share, which is why the bound belongs here rather than at each call
// site.
func TestGetPageQueryBoundsPageSizeBelowAsWellAsAbove(t *testing.T) {
	for _, testCase := range []struct {
		name         string
		target       string
		wantPage     int
		wantPageSize int
	}{
		{name: "absent", target: "/x", wantPage: 1, wantPageSize: ItemsPerPage},
		{name: "negative page size", target: "/x?page_size=-1", wantPage: 1, wantPageSize: ItemsPerPage},
		{name: "zero page size", target: "/x?page_size=0", wantPage: 1, wantPageSize: ItemsPerPage},
		{name: "non integer page size", target: "/x?page_size=abc", wantPage: 1, wantPageSize: ItemsPerPage},
		{name: "huge page size is clamped to the upper bound", target: "/x?page_size=100000", wantPage: 1, wantPageSize: 100},
		{name: "at the upper bound", target: "/x?page_size=100", wantPage: 1, wantPageSize: 100},
		{name: "negative size alias", target: "/x?ps=-1", wantPage: 1, wantPageSize: ItemsPerPage},
		{name: "negative token size alias", target: "/x?size=-1", wantPage: 1, wantPageSize: ItemsPerPage},
		{name: "negative page", target: "/x?p=-3", wantPage: 1, wantPageSize: ItemsPerPage},
		{name: "zero page", target: "/x?p=0", wantPage: 1, wantPageSize: ItemsPerPage},
		{name: "non integer page", target: "/x?p=abc", wantPage: 1, wantPageSize: ItemsPerPage},
		{name: "a usable request is untouched", target: "/x?p=3&page_size=25", wantPage: 3, wantPageSize: 25},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			pageInfo := pageQueryFor(t, testCase.target)
			assert.Equal(t, testCase.wantPage, pageInfo.GetPage(), "page")
			assert.Equal(t, testCase.wantPageSize, pageInfo.GetPageSize(), "page_size")
			assert.GreaterOrEqual(t, pageInfo.GetStartIdx(), 0,
				"a negative start index reaches GORM as Offset() and produces no OFFSET clause")
		})
	}
}

// TestGetPageQueryReportsAnExplicitlyNegativeValue pins the signal the clamping
// hides.
//
// A caller that must refuse such a request instead of silently paging it — the
// square-state listing in controller/model_meta.go, whose in-memory filter runs
// after the search — used to detect it by comparing Page/PageSize against zero.
// That comparison is dead once the bound is enforced here, so the fact travels
// out as Substituted instead, and it has to mean exactly what it meant before:
// only an explicitly negative value counts. `page_size=0`, a missing parameter
// and an unparsable one are "not supplied", which is a default, not a client
// error.
func TestGetPageQueryReportsAnExplicitlyNegativeValue(t *testing.T) {
	for _, testCase := range []struct {
		name        string
		target      string
		substituted bool
	}{
		{name: "absent", target: "/x"},
		{name: "usable", target: "/x?p=3&page_size=25"},
		{name: "zero page size is a default", target: "/x?page_size=0"},
		{name: "unparsable page size is a default", target: "/x?page_size=abc"},
		{name: "zero page is the legacy first page", target: "/x?p=0"},
		{name: "unparsable page is the legacy first page", target: "/x?p=abc"},
		{name: "negative page size", target: "/x?page_size=-1", substituted: true},
		{name: "negative size alias", target: "/x?ps=-1", substituted: true},
		{name: "negative token size alias", target: "/x?size=-1", substituted: true},
		{name: "negative page", target: "/x?p=-3", substituted: true},
		{name: "a negative page is not masked by a usable page size", target: "/x?p=-3&page_size=25", substituted: true},
		{name: "a negative alias is still a negative request", target: "/x?page_size=-1&ps=50", substituted: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			assert.Equal(t, testCase.substituted, pageQueryFor(t, testCase.target).Substituted)
		})
	}
}
