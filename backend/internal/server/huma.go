//go:build linux

package server

import (
	"context"
	"encoding/json"
	"io"
	"strings"

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
		case 400, 415:
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

// requireResponseFields marks the fields a response always carries as
// required. Optional-by-default is a request validation rule; a response field
// without omitempty is always on the wire, and the generated client types say so.
func requireResponseFields(doc *huma.OpenAPI) {
	const prefix = "#/components/schemas/"
	registry := doc.Components.Schemas
	requests, responses := map[string]bool{}, map[string]bool{}
	for _, item := range doc.Paths {
		for _, operation := range []*huma.Operation{
			item.Get, item.Put, item.Post, item.Delete, item.Options, item.Head, item.Patch, item.Trace,
		} {
			if operation == nil {
				continue
			}
			if operation.RequestBody != nil {
				for _, media := range operation.RequestBody.Content {
					collectSchemaRefs(registry, media.Schema, requests)
				}
			}
			for code, response := range operation.Responses {
				if code == "default" {
					continue
				}
				for _, media := range response.Content {
					collectSchemaRefs(registry, media.Schema, responses)
				}
			}
		}
	}
	// A registry left at Huma's default derives required from omitempty and
	// the required tag, which is what encoding/json puts on the wire.
	strict := huma.NewMapRegistry(prefix, huma.DefaultSchemaNamer)
	for name := range responses {
		if requests[name] {
			continue
		}
		ref := prefix + name
		registry.SchemaFromRef(ref).Required = strict.Schema(registry.TypeFromRef(ref), false, name).Required
	}
}

// collectSchemaRefs adds every named schema reachable from s to into.
func collectSchemaRefs(registry huma.Registry, s *huma.Schema, into map[string]bool) {
	if s == nil {
		return
	}
	if s.Ref != "" {
		name := s.Ref[strings.LastIndex(s.Ref, "/")+1:]
		if !into[name] {
			into[name] = true
			collectSchemaRefs(registry, registry.SchemaFromRef(s.Ref), into)
		}
		return
	}
	collectSchemaRefs(registry, s.Items, into)
	collectSchemaRefs(registry, s.Not, into)
	if extra, ok := s.AdditionalProperties.(*huma.Schema); ok {
		collectSchemaRefs(registry, extra, into)
	}
	for _, property := range s.Properties {
		collectSchemaRefs(registry, property, into)
	}
	for _, group := range [][]*huma.Schema{s.OneOf, s.AnyOf, s.AllOf} {
		for _, sub := range group {
			collectSchemaRefs(registry, sub, into)
		}
	}
}

// typed is one access group's Huma API. A public group's operations are
// documented as needing no credential.
type typed struct {
	api    huma.API
	errs   *apierr.Classifier
	public bool
}

// newTyped mounts a Huma API on group. A declared body past the JSON bound is
// refused and drained before Huma reads it, so the client can read the 413.
func newTyped(router *gin.Engine, group *gin.RouterGroup, config huma.Config, errs *apierr.Classifier, public bool) typed {
	return typed{api: humagin.NewWithGroup(router, group.Group("", middleware.LimitJSON), config), errs: errs, public: public}
}

// op registers a typed operation. Every handler error is classified into the
// native envelope; Huma would otherwise answer 500 and drop it.
func op[I, O any](t typed, method, path, id string, h func(context.Context, *I) (*O, error)) {
	operation := huma.Operation{OperationID: id, Method: method, Path: path, MaxBodyBytes: limits.RequestBody}
	if t.public {
		operation.Security = []map[string][]string{{}}
	}
	huma.Register(t.api, operation, func(ctx context.Context, in *I) (*O, error) {
		out, err := h(ctx, in)
		if err != nil {
			return nil, t.errs.Response(err)
		}
		return out, nil
	})
}
