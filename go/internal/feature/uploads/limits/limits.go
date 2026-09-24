// Package limits defines bounds for upload sessions and transfer state.
package limits

import "time"

const (
	UploadIntervalRuns           = 4096
	UploadReservedBytesPerUser   = 100 << 30
	UploadFreeSpaceMargin        = 2 << 30
	UploadFreeSpaceMarginDivisor = 20
	UploadsInFlightPerUser       = 32
	UploadSessionsPerUser        = 256
	UploadSpooledNames           = 4096
	UploadChunkFloor             = 5 << 20
	UploadChunkMinDefault        = 5 << 20
	UploadChunkSizeDefault       = 10 << 20
	NameBytes                    = 255
)

const (
	UploadSessionTTL = 24 * time.Hour
)
