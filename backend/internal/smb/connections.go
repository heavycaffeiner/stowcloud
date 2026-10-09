//go:build linux

package smb

// Connection views expose only the caller's share names, never disk paths or
// another account's private exports.
type SMBFolderView struct {
	Label     string `json:"label"`
	Share     string `json:"share,omitempty"`
	Personal  bool   `json:"personal"`
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`
}

type SMBConnectionsView struct {
	Enabled bool            `json:"enabled"`
	Server  string          `json:"server,omitempty"`
	Folders []SMBFolderView `json:"folders"`
}
