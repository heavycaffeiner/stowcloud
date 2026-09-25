// Package objstore adapts the public storage/s3 backend to the product VFS.
package objstore

import publics3 "github.com/stowcloud/storage/s3"

// Config is the persisted, secret-free S3 configuration.
type Config = publics3.Config

// ParseConfig validates persisted S3 configuration at the product trust boundary.
func ParseConfig(b []byte) (Config, error) { return publics3.ParseConfig(b) }
