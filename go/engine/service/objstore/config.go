// Package objstore serves an S3-compatible bucket as a Stowcloud share.
//
// Nothing here mounts anything: the engine's seccomp filter refuses mount
// outright and the shipped image drops every capability, so a remote bucket
// is reached the only way that is left, an HTTPS (or, for a private-network
// MinIO, plain HTTP) client speaking the S3 REST API, hand rolled on
// net/http and crypto/hmac because no dependency for either is on
// go/deps.allow. A file whose bytes live in the bucket is materialized into
// server-owned scratch space before this package hands back a descriptor on
// it, exactly as vfs.Root documents: the handle type stays a real *vfs.File
// throughout the engine, and a backend that cannot offer one stages it.
package objstore

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/heavycaffeiner/stowcloud/go/engine/infra/vfs"
)

// maxPrefixBytes bounds the configured prefix on its own, ahead of the
// general path-length bound ParseSafePath already applies, since a prefix
// this long is a misconfiguration worth naming distinctly from "too many
// path components."
const maxPrefixBytes = 512

// validRegion is deliberately narrower than a real AWS region name has to
// be: every S3-compatible region string seen in practice is lowercase
// letters, digits and hyphens, and a value outside that is far more likely
// to be a stray credential or endpoint pasted into the wrong field than a
// region this build has not heard of.
func validRegion(s string) bool {
	if s == "" {
		return false
	}
	for i := range s {
		c := s[i]
		if !(c >= 'a' && c <= 'z') && !(c >= '0' && c <= '9') && c != '-' {
			return false
		}
	}
	return true
}

// Config is the secret-free half of an s3 share's configuration: everything
// that is persisted verbatim in share_definition.backend_config. The
// credential travels beside it, sealed, and never through this type.
type Config struct {
	Endpoint  string `json:"endpoint"`
	Region    string `json:"region"`
	Bucket    string `json:"bucket"`
	Prefix    string `json:"prefix"`
	AccessKey string `json:"access_key_id"`
	PathStyle bool   `json:"path_style"`
}

// ParseConfig is the trust boundary for the "s3" object of a share
// definition: every field on it arrived either from an admin's request body
// or from a database row, and either could have been edited by hand. Every
// refusal here happens before a byte is ever sent to the endpoint named.
func ParseConfig(b []byte) (Config, error) {
	var cfg Config
	if err := json.Unmarshal(b, &cfg); err != nil {
		return Config{}, fmt.Errorf("objstore: parse config: %w", err)
	}

	if cfg.Bucket == "" {
		return Config{}, errors.New("objstore: bucket is required")
	}
	if len(cfg.Prefix) > maxPrefixBytes {
		return Config{}, fmt.Errorf("objstore: prefix exceeds %d bytes", maxPrefixBytes)
	}
	// A trailing slash is a common, harmless way to spell a prefix ("team/")
	// and is trimmed before validation. A leading slash is not trimmed: it
	// is refused, since it reads as an absolute path rather than a prefix.
	prefix := strings.TrimSuffix(cfg.Prefix, "/")
	if err := validateConfigPath("bucket", cfg.Bucket); err != nil {
		return Config{}, err
	}
	if err := validateConfigPath("prefix", prefix); err != nil {
		return Config{}, err
	}
	if !validRegion(cfg.Region) {
		return Config{}, fmt.Errorf("objstore: region %q is not a lowercase [a-z0-9-]+ name", cfg.Region)
	}
	if err := validateEndpoint(cfg.Endpoint); err != nil {
		return Config{}, err
	}

	cfg.Prefix = prefix
	return cfg, nil
}

// validateConfigPath applies the same refusal table to the bucket and the
// prefix: a control character is checked by hand, since ParseSafePath only
// refuses the NUL byte among them, and everything else (a leading slash, a
// ".", a "..", an over-length or reserved-looking component) is refused by
// ParseSafePath itself, which is the one table the rest of this domain
// already trusts for "does this component name something a filesystem
// could hold."
func validateConfigPath(field, s string) error {
	for i := range s {
		if c := s[i]; c < 0x20 || c == 0x7f {
			return fmt.Errorf("objstore: %s contains a control character", field)
		}
	}
	if s == "" {
		return nil
	}
	if _, err := vfs.ParseSafePath(s); err != nil {
		return fmt.Errorf("objstore: %s: %w", field, err)
	}
	return nil
}

// validateEndpoint refuses anything but a bare scheme and authority. A path,
// query or fragment on the endpoint is refused rather than silently joined
// with the bucket and key later, since a silent join is exactly how a typo
// here turns into every request going somewhere the admin did not type.
func validateEndpoint(endpoint string) error {
	u, err := url.Parse(endpoint)
	if err != nil {
		return fmt.Errorf("objstore: endpoint: %w", err)
	}
	if !u.IsAbs() || (u.Scheme != "http" && u.Scheme != "https") {
		return errors.New("objstore: endpoint must be an absolute http or https URL")
	}
	if u.Host == "" {
		return errors.New("objstore: endpoint must name a host")
	}
	if u.Path != "" && u.Path != "/" {
		return errors.New("objstore: endpoint must not carry a path")
	}
	if u.RawQuery != "" {
		return errors.New("objstore: endpoint must not carry a query")
	}
	if u.Fragment != "" {
		return errors.New("objstore: endpoint must not carry a fragment")
	}
	if u.User != nil {
		return errors.New("objstore: endpoint must not carry userinfo")
	}
	return nil
}

// Marshal renders c back to the bytes share_definition.backend_config
// stores. It carries no secret: the access key id is a bucket-scoped
// identifier, not the credential, which travels through secret.Secret and
// this package's Options instead.
func (c Config) Marshal() ([]byte, error) {
	b, err := json.Marshal(c)
	if err != nil {
		return nil, fmt.Errorf("objstore: marshal config: %w", err)
	}
	return b, nil
}

// Describe renders the redacted, human-readable location an operator sees
// in a share's source field. It carries the scheme, since a plain http
// endpoint (an in-network MinIO, the common case) is a fact worth an
// operator noticing at a glance rather than discovering during an incident.
func (c Config) Describe() string {
	loc := "s3://" + c.Bucket
	if c.Prefix != "" {
		loc += "/" + c.Prefix
	}
	return loc + " at " + c.Endpoint
}
