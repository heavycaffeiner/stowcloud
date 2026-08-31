//go:build linux && !compat_nc

package lifecycle

import (
	"context"
	"encoding/xml"

	"github.com/heavycaffeiner/stowcloud/go/engine/http/dav"
	"github.com/heavycaffeiner/stowcloud/go/engine/service/core"
)

// The mount carries no compatibility vocabulary in a build without the tag:
// no alternative prefixes, no chunked-upload header names, which leaves the
// collection disabled, and no vendor properties, which leaves every vendor
// property a client asks for reported as missing.

func (e *Engine) davAliases() []DavAlias { return nil }

func (e *Engine) davUploadHeaders() dav.UploadHeaders { return dav.UploadHeaders{} }

func (e *Engine) davIsAssemblyMember(string) bool { return false }

func (e *Engine) davVendorProps() func(
	ctx context.Context, res core.Resolved, e core.Entry, want []xml.Name,
) []dav.Prop {
	return nil
}
