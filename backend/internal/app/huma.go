//go:build linux

package app

import (
	"encoding/json"
	"io"
	"strings"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humagin"
	"github.com/gin-gonic/gin"
	hanamiapi "github.com/heavycaffeiner/hanami/api"
	"github.com/heavycaffeiner/stowcloud/backend/internal/files"
	"github.com/heavycaffeiner/stowcloud/backend/internal/http/api/adminlogs"
	"github.com/heavycaffeiner/stowcloud/backend/internal/http/api/adminshares"
	"github.com/heavycaffeiner/stowcloud/backend/internal/http/api/adminstorage"
	"github.com/heavycaffeiner/stowcloud/backend/internal/http/api/encryption"
	filehttp "github.com/heavycaffeiner/stowcloud/backend/internal/http/api/files"
	"github.com/heavycaffeiner/stowcloud/backend/internal/http/api/handler"
	"github.com/heavycaffeiner/stowcloud/backend/internal/http/api/humabridge"
	"github.com/heavycaffeiner/stowcloud/backend/internal/http/api/jobs"
	"github.com/heavycaffeiner/stowcloud/backend/internal/http/api/links"
	"github.com/heavycaffeiner/stowcloud/backend/internal/http/api/smbaccount"
	trashhttp "github.com/heavycaffeiner/stowcloud/backend/internal/http/api/trash"
	"github.com/heavycaffeiner/stowcloud/backend/internal/http/server"
	"github.com/heavycaffeiner/stowcloud/backend/internal/server/apierr"
	"github.com/heavycaffeiner/stowcloud/backend/internal/server/middleware"
)

// humaNames declares the typed part of the native route table. The server
// verifies that every name is registered on Gin before it starts listening.
func humaNames() []string {
	return []string{
		"jobs.list", "jobs.get", "jobs.cancel", "jobs.retry", "jobs.pause", "jobs.resume",
		"trash.list", "trash.restore", "trash.purge",
		"admin.logs.list", "admin.logs.timeline", "admin.storage",
		"links.list", "links.create", "links.update", "links.delete", "admin.links.list",
		"encryption.list", "admin.encryption.enable", "admin.encryption.disable",
		"account.smb.create", "account.smb.password.set", "account.smb.password.delete",
		"admin.shares.list", "admin.shares.create", "admin.shares.update", "admin.shares.retry", "admin.shares.delete",
		"admin.grants.list", "admin.grants.create", "admin.grants.update", "admin.grants.delete",
	}
}

func (e *Engine) mountHuma(router *gin.Engine) (huma.API, error) {
	config := huma.DefaultConfig("Stowcloud API", "1")
	config.Info.Description = "Typed JSON endpoints. Streaming and compatibility protocols are mounted separately."
	config.Servers = []*huma.Server{{URL: server.Base}}
	// Native JSON request fields are optional unless a handler's product rules
	// require them. Huma otherwise makes every body field required by default.
	config.FieldsOptionalByDefault = true
	nativeJSON := huma.Format{
		Marshal: func(w io.Writer, value any) error {
			body, err := json.Marshal(value)
			if err != nil {
				return err
			}
			_, err = w.Write(body)
			return err
		},
		Unmarshal: json.Unmarshal,
	}
	config.Formats = map[string]huma.Format{"application/json": nativeJSON, "json": nativeJSON}
	config.Components.SecuritySchemes = map[string]*huma.SecurityScheme{
		"browserSession": {
			Type: "apiKey", In: "cookie", Name: middleware.SessionCookieName,
			Description: "Browser session. Mutating requests also require the Sc-Csrf header.",
		},
	}
	config.Security = []map[string][]string{{"browserSession": {}}}
	// Only the authenticated admin route publishes the specification. The
	// default public docs and schema endpoints bypass product route metadata.
	config.OpenAPIPath = ""
	config.DocsPath = ""
	config.SchemasPath = ""
	// The default response transformer adds a schema link and response field;
	// native clients must keep the existing JSON contract.
	config.Transformers = []huma.Transformer{func(ctx huma.Context, _ string, value any) (any, error) {
		problem, ok := value.(*huma.ErrorModel)
		if !ok {
			return value, nil
		}
		var class apierr.Class
		switch problem.Status {
		case 400:
			class = apierr.Malformed
		case 413:
			class = apierr.BodyTooLarge
		case 422:
			class = apierr.Unprocessable
		default:
			return value, nil
		}
		ctx.SetHeader("Content-Type", "application/json")
		_, body := apierr.REST(apierr.Classified{Class: class})
		return body, nil
	}}
	config.CreateHooks = nil
	api := hanamiapi.New(router, config, server.Base)
	humabridge.Install(api, e.errs)
	api.UseMiddleware(func(ctx huma.Context, next func(huma.Context)) {
		c := humagin.Unwrap(ctx)
		if name, ok := middleware.RouteNameOf(c); ok && strings.HasPrefix(name, "admin.") {
			if _, ok := handler.Admin(c, e.Auth); !ok {
				return
			}
		}
		next(ctx)
	})

	jobs.Register(api, jobs.Deps{Core: e.Core, State: e.State, Owner: handler.Owner, StartJobs: e.Core.StartJobs, NowNs: e.now})
	trashhttp.Register(api, trashhttp.Deps{Core: e.Core, Owner: handler.Owner, Resolve: filehttp.Resolve(e.Core), Errors: e.errs})
	adminlogs.Register(api, adminlogs.Deps{Logs: e.Logs, Auth: e.Auth})
	adminstorage.Register(api, adminstorage.Deps{Core: e.Core, State: e.State})
	adminshares.Register(api, adminshares.Deps{
		Core: e.Core, MarkSearchIncomplete: e.searchController.MarkIncomplete,
		WatchShare: e.watchShare, UnwatchShare: e.unwatchShare, Logger: e.logger,
	})
	links.Register(api, links.NativeDeps{
		Core: e.Core, Auth: e.Auth, Owner: handler.Owner,
		Resolve: filehttp.Resolve(e.Core), Now: e.now,
		VpathOf: func(l files.Link) string {
			vp, err := e.Core.VpathFor(l.Owner, l.Share, l.Path)
			if err != nil {
				return ""
			}
			return vp.String()
		},
	})
	encryption.Register(api, encryption.Deps{Core: e.Core})
	smbaccount.Register(api, smbaccount.Deps{Auth: e.Auth})
	return api, nil
}
