//go:build linux

package app

import (
	"context"
	"encoding/json"
	"io"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humagin"
	"github.com/gin-gonic/gin"

	"github.com/heavycaffeiner/stowcloud/backend/internal/platform/protocol/limits"
	"github.com/heavycaffeiner/stowcloud/backend/internal/server/apierr"
	"github.com/heavycaffeiner/stowcloud/backend/internal/server/middleware"
)

// humaConfig is shared by every typed group, so all of them write into one
// OpenAPI document.
func humaConfig() huma.Config {
	config := huma.DefaultConfig("Stowcloud API", "1")
	config.Info.Description = "Typed JSON endpoints. Streaming and compatibility protocols are mounted separately."
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
	// Only the authenticated admin route publishes the specification.
	config.OpenAPIPath = ""
	config.DocsPath = ""
	config.SchemasPath = ""
	// Huma's own problem bodies become the native error envelope, and a
	// handler failure keeps its cause for the access log.
	config.Transformers = []huma.Transformer{func(ctx huma.Context, _ string, value any) (any, error) {
		if failure, ok := value.(*apierr.Response); ok {
			middleware.SetCause(humagin.Unwrap(ctx), failure.Unwrap())
			return value, nil
		}
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
	return config
}

// typed is one access group's Huma API.
type typed struct {
	api  huma.API
	errs *apierr.Classifier
}

func newTyped(router *gin.Engine, group *gin.RouterGroup, config huma.Config, errs *apierr.Classifier) typed {
	return typed{api: humagin.NewWithGroup(router, group, config), errs: errs}
}

// op registers a typed operation. Every handler error is classified into the
// native envelope; Huma would otherwise answer 500 and drop it.
func op[I, O any](t typed, method, path, id string, h func(context.Context, *I) (*O, error)) {
	huma.Register(t.api, huma.Operation{
		OperationID: id, Method: method, Path: path, MaxBodyBytes: limits.RequestBody,
	}, func(ctx context.Context, in *I) (*O, error) {
		out, err := h(ctx, in)
		if err != nil {
			return nil, t.errs.Response(err)
		}
		return out, nil
	})
}
