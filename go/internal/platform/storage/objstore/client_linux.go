//go:build linux

package objstore

import (
	"context"
	"errors"

	"github.com/aws/aws-sdk-go-v2/aws"
	sdkconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

func (r *Root) ensureSDK() error {
	if r.s3 != nil {
		return nil
	}
	access, secret := r.cfg.AccessKey, ""
	if access == "" {
		access = r.credentials.accessKey
	}
	secret = string(r.credentials.secret)
	if access == "" || secret == "" {
		return errors.New("objstore: direct transfer unsupported")
	}
	creds := credentials.NewStaticCredentialsProvider(access, secret, "")
	httpClient := r.http
	if httpClient == nil {
		httpClient = defaultHTTPClient()
	}
	cfg, err := sdkconfig.LoadDefaultConfig(context.Background(), sdkconfig.WithRegion(r.cfg.Region), sdkconfig.WithCredentialsProvider(creds), sdkconfig.WithHTTPClient(httpClient))
	if err != nil {
		return err
	}
	endpoint := r.cfg.Endpoint
	r.s3 = s3.NewFromConfig(cfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(endpoint)
		o.UsePathStyle = r.cfg.PathStyle
	})
	r.sdkPresign = s3.NewPresignClient(r.s3)
	return nil
}
