package main

import (
	"flag"
	"fmt"
	"os"
	"strconv"

	"github.com/agentshield-ai/agentshield/internal/weft/objstore"
	"github.com/agentshield-ai/agentshield/internal/weft/store"
)

// filesFlags configure the optional S3-compatible backend for file contents.
type filesFlags struct {
	backend, endpoint, region, pathStyle *string
}

func registerFilesFlags() *filesFlags {
	return &filesFlags{
		backend: flag.String("files-backend", os.Getenv("WEFT_FILES_BACKEND"),
			"store file contents in an S3-compatible bucket, e.g. s3://bucket/prefix (env WEFT_FILES_BACKEND); empty keeps them in SQLite"),
		endpoint: flag.String("s3-endpoint", firstEnv("WEFT_S3_ENDPOINT", "AWS_ENDPOINT_URL_S3", "AWS_ENDPOINT_URL"),
			"S3 endpoint URL for --files-backend, e.g. http://127.0.0.1:9000 (env WEFT_S3_ENDPOINT, AWS_ENDPOINT_URL_S3, AWS_ENDPOINT_URL); empty means AWS"),
		region: flag.String("s3-region", firstEnv("WEFT_S3_REGION", "AWS_REGION", "AWS_DEFAULT_REGION"),
			"S3 region for --files-backend (env WEFT_S3_REGION, AWS_REGION); default us-east-1"),
		pathStyle: flag.String("s3-path-style", os.Getenv("WEFT_S3_PATH_STYLE"),
			"true/false: path-style bucket addressing (env WEFT_S3_PATH_STYLE); default true when --s3-endpoint is set"),
	}
}

func firstEnv(names ...string) string {
	for _, n := range names {
		if v := os.Getenv(n); v != "" {
			return v
		}
	}
	return ""
}

// apply configures st's files backend. Credentials come from
// WEFT_S3_ACCESS_KEY_ID / WEFT_S3_SECRET_ACCESS_KEY / WEFT_S3_SESSION_TOKEN or
// the standard AWS_* variables. It returns a description for the startup log.
func (f *filesFlags) apply(st *store.Store) (string, error) {
	if *f.backend == "" {
		return "sqlite", nil
	}
	bucket, prefix, err := objstore.ParseURL(*f.backend)
	if err != nil {
		return "", err
	}
	cfg := objstore.Config{Bucket: bucket, Prefix: prefix, Endpoint: *f.endpoint, Region: *f.region,
		Creds: objstore.Credentials{
			AccessKeyID:     firstEnv("WEFT_S3_ACCESS_KEY_ID", "AWS_ACCESS_KEY_ID"),
			SecretAccessKey: firstEnv("WEFT_S3_SECRET_ACCESS_KEY", "AWS_SECRET_ACCESS_KEY"),
			SessionToken:    firstEnv("WEFT_S3_SESSION_TOKEN", "AWS_SESSION_TOKEN"),
		}}
	if *f.pathStyle != "" {
		v, err := strconv.ParseBool(*f.pathStyle)
		if err != nil {
			return "", fmt.Errorf("invalid --s3-path-style %q", *f.pathStyle)
		}
		cfg.PathStyle = &v
	}
	b, err := objstore.New(cfg)
	if err != nil {
		return "", err
	}
	st.SetFileBackend(b)
	return *f.backend, nil
}
