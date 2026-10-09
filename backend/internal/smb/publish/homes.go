//go:build linux

package publish

import (
	"context"
	"fmt"
	"strings"

	"github.com/heavycaffeiner/stowcloud/backend/internal/auth"
	"github.com/heavycaffeiner/stowcloud/backend/internal/files"
	"github.com/heavycaffeiner/stowcloud/backend/internal/smb"
)

func (p *Publisher) homeShares(ctx context.Context) ([]smb.ShareDef, error) {
	credentials, err := p.deps.Auth.SMBCredentials(ctx)
	if err != nil {
		return nil, err
	}
	used := make(map[string]bool)
	for _, def := range p.deps.Core.Shares() {
		used[strings.ToLower(strings.TrimSpace(def.Name))] = true
	}
	var out []smb.ShareDef
	for _, credential := range credentials {
		if auth.ValidUsername(credential.Name) != nil {
			continue
		}
		id, err := p.deps.Auth.UserIDByName(ctx, credential.Name)
		if err != nil {
			return nil, err
		}
		dir, err := p.deps.Core.HomeDirectory(ctx, files.UserID(id))
		if err != nil {
			continue // Disabled, unprepared and unsafe homes are never published.
		}
		name := "Home-" + credential.Name
		for suffix := 0; used[strings.ToLower(name)]; suffix++ {
			// Existing installations may already contain a share with this
			// name. Keep its authority, and give the private export a unique name.
			name = fmt.Sprintf("Home-%s-%d-%d", credential.Name, id, suffix)
		}
		used[strings.ToLower(name)] = true
		out = append(out, smb.ShareDef{
			Name: name, Path: dir, Private: true, SharedExternally: true,
			ValidUsers: []string{credential.Name}, WriteList: []string{credential.Name},
		})
	}
	return out, nil
}
