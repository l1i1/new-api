package router

import (
	"bytes"
	"embed"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/controller"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/gin-contrib/gzip"
	"github.com/gin-contrib/static"
	"github.com/gin-gonic/gin"
)

// WebAssets holds the embedded dashboard frontend assets.
type WebAssets struct {
	BuildFS   embed.FS
	IndexPage []byte
}

// headHTMLPlaceholder marks the administrator-editable region inside the
// embedded index.html (see common.DefaultCustomHeadHTML).
var headHTMLPlaceholder = []byte("<!--head-html-->")

// rsbuildTitle is the empty <title> tag the build tool injects because the
// template no longer ships one (the title belongs to the editable head
// region); it must not leak into the served page next to the injected one.
var rsbuildTitle = []byte("<title></title>")

// renderIndexPage injects the current CustomHeadHTML option into the index
// template. Rendering per request keeps edits effective immediately and lets
// follower nodes pick up the synced option without a restart; the replacement
// is a couple of bytes.ReplaceAll calls over a small page, so the cost is
// negligible next to the existing gzip middleware.
func renderIndexPage(indexPage []byte) []byte {
	common.OptionMapRWMutex.RLock()
	headHTML := common.OptionMap["CustomHeadHTML"]
	common.OptionMapRWMutex.RUnlock()
	if strings.TrimSpace(headHTML) == "" {
		headHTML = common.DefaultCustomHeadHTML
	}
	page := bytes.ReplaceAll(indexPage, rsbuildTitle, nil)
	return bytes.ReplaceAll(page, headHTMLPlaceholder, []byte(headHTML))
}

func SetWebRouter(router *gin.Engine, assets WebAssets, pluginDispatcher gin.HandlerFunc) {
	frontendFS := common.EmbedFolder(assets.BuildFS, "web/dist")

	router.NoRoute(
		pluginDispatcher,
		middleware.RouteTag("web"),
		gzip.Gzip(gzip.DefaultCompression),
		middleware.AccessTokenAudit(),
		middleware.GlobalWebRateLimit(),
		middleware.Cache(),
		static.Serve("/", frontendFS),
		func(c *gin.Context) {
			if strings.HasPrefix(c.Request.RequestURI, "/v1") || strings.HasPrefix(c.Request.RequestURI, "/api") || strings.HasPrefix(c.Request.RequestURI, "/assets") {
				controller.RelayNotFound(c)
				return
			}
			c.Header("Cache-Control", "no-cache")
			c.Data(http.StatusOK, "text/html; charset=utf-8", renderIndexPage(assets.IndexPage))
		},
	)
}
