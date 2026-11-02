package objstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
)

// PutReader uploads size bytes from r to key (a full key; see Key) without
// holding them in memory. r is read twice: once to hash the payload for
// SigV4 and once to send it.
func (s *S3) PutReader(ctx context.Context, key string, r io.ReadSeeker, size int64, contentType string) error {
	h := sha256.New()
	if _, err := io.Copy(h, r); err != nil {
		return err
	}
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, s.objectURL(key).String(), io.NopCloser(r))
	if err != nil {
		return err
	}
	req.ContentLength = size
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	req.Header.Set("Content-Type", contentType)
	s.signHash(req, hex.EncodeToString(h.Sum(nil)))
	resp, err := s.cfg.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("s3 put: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("s3 put %s: %s", key, errorBody(resp))
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	return nil
}
