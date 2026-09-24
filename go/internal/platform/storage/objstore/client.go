package objstore

import (
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/aws/smithy-go"
	"github.com/heavycaffeiner/stowcloud/go/internal/platform/storage/limits"
	"github.com/heavycaffeiner/stowcloud/go/internal/platform/storage/vfs"
)

const (
	dialTimeout            = 10 * time.Second
	tlsHandshakeTimeout    = 10 * time.Second
	responseHeaderTimeout  = 30 * time.Second
	idleConnTimeout        = 90 * time.Second
	metadataRequestTimeout = 30 * time.Second
	maxMetadataBodyBytes   = 1 << 20
	maxListResponseBytes   = 8 << 20
	maxListEntries         = 2_000
	maxObjectKeyBytes      = 1024
	maxRenameEntries       = 10_000
	maxObjectBodyBytes     = 64 << 30
)

func defaultHTTPClient() *http.Client {
	return &http.Client{CheckRedirect: refuseCrossHostRedirect, Transport: &http.Transport{
		DialContext:         (&net.Dialer{Timeout: dialTimeout}).DialContext,
		TLSHandshakeTimeout: tlsHandshakeTimeout, ResponseHeaderTimeout: responseHeaderTimeout,
		IdleConnTimeout: idleConnTimeout, ForceAttemptHTTP2: true,
	}}
}
func refuseCrossHostRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 5 {
		return errors.New("objstore: too many redirects")
	}
	if len(via) == 0 {
		return errors.New("objstore: refusing a redirect without an origin request")
	}
	previous := via[len(via)-1]
	if previous.URL.Scheme == "https" && req.URL.Scheme == "http" {
		return fmt.Errorf("objstore: refusing an HTTPS to HTTP redirect to %s", req.URL.Host)
	}
	if req.URL.Host != via[0].URL.Host {
		return fmt.Errorf("objstore: refusing a redirect from %s to a different host %s", via[0].URL.Host, req.URL.Host)
	}
	return errors.New("objstore: refusing an unsupported S3 redirect")
}
func readBounded(r io.Reader, limit int64, what string) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, fmt.Errorf("objstore: reading %s: %w", what, err)
	}
	if int64(len(body)) > limit {
		return nil, limits.Exceed(what, limit, int64(len(body)))
	}
	return body, nil
}

func classifyS3Error(status int, body []byte) error {
	code, msg := "", ""
	if len(body) > 0 {
		var e s3ErrorXML
		if unmarshalErr := xml.Unmarshal(body, &e); unmarshalErr == nil {
			code, msg = e.Code, e.Message
		}
	}
	detail := fmt.Sprintf("status %d", status)
	if code != "" {
		detail = fmt.Sprintf("status %d, %s: %s", status, code, msg)
	}
	switch {
	case status == 404 || code == "NoSuchKey" || code == "NoSuchBucket":
		return fmt.Errorf("objstore: %s: %w", detail, vfs.ErrNotFound)
	case status == 401 || status == 403 || code == "AccessDenied":
		return fmt.Errorf("objstore: %s: %w", detail, vfs.ErrDenied)
	case status == 409 || code == "BucketAlreadyExists" || code == "BucketAlreadyOwnedByYou":
		return fmt.Errorf("objstore: %s: %w", detail, vfs.ErrExists)
	default:
		return fmt.Errorf("objstore: %s", detail)
	}
}
func panicInfallibleWrite(err error) {
	if err != nil {
		panic(err)
	}
}

type s3ErrorXML struct {
	Code    string `xml:"Code"`
	Message string `xml:"Message"`
}

func sdkError(op string, err error) error {
	if err == nil {
		return nil
	}
	var api smithy.APIError
	if errors.As(err, &api) {
		switch api.ErrorCode() {
		case "NoSuchKey", "NoSuchBucket", "NotFound":
			return fmt.Errorf("objstore: %s: %w", op, vfs.ErrNotFound)
		case "AccessDenied", "InvalidAccessKeyId", "SignatureDoesNotMatch":
			return fmt.Errorf("objstore: %s: %w", op, vfs.ErrDenied)
		case "BucketAlreadyExists", "BucketAlreadyOwnedByYou":
			return fmt.Errorf("objstore: %s: %w", op, vfs.ErrExists)
		}
	}
	return fmt.Errorf("objstore: %s: %w", op, err)
}
