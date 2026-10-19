// Salad Simple Storage Service (S4) — a DIFFERENT host from the Public API
// (default https://storage-api.salad.com, override via SALAD_CLI_STORAGE_BASE_URL
// for tests). Used by the transcribe porcelain to stage local audio files.

package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/saladtechnologies/salad-cloud-cli/internal/clierr"
)

// DefaultStorageBaseURL is the production S4 base URL.
const DefaultStorageBaseURL = "https://storage-api.salad.com"

// storageTimeout bounds one upload. It is deliberately much longer than
// restTimeout — audio/video files are big.
const storageTimeout = 10 * time.Minute

// storageBaseURL resolves the S4 base URL, honoring the test override.
func storageBaseURL(getenv func(string) string) string {
	if override := getenv("SALAD_CLI_STORAGE_BASE_URL"); override != "" {
		return override
	}
	return DefaultStorageBaseURL
}

// uploadResponse is S4's PUT response document.
type uploadResponse struct {
	URL string `json:"url"`
}

// UploadFile PUTs one file into the organization's S4 storage as
// multipart/form-data (parts: file, mimeType, and — when sign is true —
// sign/signatureExp) and returns the file's URL (a signed URL when sign is
// set). remoteName may contain "/" as folder separators; each segment is
// path-escaped individually. The reader is streamed to the wire — the file
// is never buffered in memory.
func (c *Client) UploadFile(
	ctx context.Context,
	remoteName string,
	r io.Reader,
	mimeType string,
	sign bool,
	signatureExpSeconds int64,
) (string, *clierr.Error) {
	if err := c.RequireOrg(); err != nil {
		return "", err
	}
	endpoint := c.storageFileURL(remoteName)

	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	go func() {
		err := writeUploadForm(mw, remoteName, r, mimeType, sign, signatureExpSeconds)
		if cerr := mw.Close(); err == nil {
			err = cerr
		}
		pw.CloseWithError(err) // nil err closes cleanly
	}()

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, endpoint, pr)
	if err != nil {
		return "", clierr.New(clierr.CodeGeneric, clierr.KindAPIError, "cannot build the upload request: %v", err)
	}
	req.Header.Set("Salad-Api-Key", c.apiKey)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", userAgent())
	req.Header.Set("Content-Type", mw.FormDataContentType())

	resp, err := c.storageHTTP.Do(req)
	if err != nil {
		return "", transportErr(ctx, "network error uploading to storage", err, c.now)
	}
	respBody, err := io.ReadAll(resp.Body)
	c.dbg.closeFailed(resp.Body.Close())
	if err != nil {
		return "", readErr(ctx, "storage response", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", errorFromBody(resp.StatusCode, respBody)
	}
	var out uploadResponse
	if err := json.Unmarshal(respBody, &out); err != nil {
		return "", clierr.New(clierr.CodeGeneric, clierr.KindAPIError, "cannot decode the storage response: %v", err)
	}
	if out.URL == "" {
		return "", clierr.New(clierr.CodeGeneric, clierr.KindAPIError, "storage upload succeeded but returned no url")
	}
	return out.URL, nil
}

// storageFileURL is the S4 URL of one file; remoteName may contain "/"
// folder separators, each segment path-escaped individually.
func (c *Client) storageFileURL(remoteName string) string {
	segs := strings.Split(remoteName, "/")
	for i, s := range segs {
		segs[i] = seg(s)
	}
	return c.storageBaseURL + fmt.Sprintf("/organizations/%s/files/%s", seg(c.org), strings.Join(segs, "/"))
}

// DeleteFile removes one file from the organization's S4 storage.
func (c *Client) DeleteFile(ctx context.Context, remoteName string) *clierr.Error {
	if err := c.RequireOrg(); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.storageFileURL(remoteName), http.NoBody)
	if err != nil {
		return clierr.New(clierr.CodeGeneric, clierr.KindAPIError, "cannot build the delete request: %v", err)
	}
	req.Header.Set("Salad-Api-Key", c.apiKey)
	req.Header.Set("User-Agent", userAgent())
	resp, err := c.storageHTTP.Do(req)
	if err != nil {
		return transportErr(ctx, "network error deleting from storage", err, c.now)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		body = nil // a body that cannot be read is no body: the status still decides
	}
	c.dbg.closeFailed(resp.Body.Close())
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return errorFromBody(resp.StatusCode, body)
	}
	return nil
}

// writeUploadForm writes S4's multipart fields. Field parts go first so the
// server sees the metadata before the (potentially large) file part.
func writeUploadForm(
	mw *multipart.Writer,
	remoteName string,
	r io.Reader,
	mimeType string,
	sign bool,
	signatureExpSeconds int64,
) error {
	if err := mw.WriteField("mimeType", mimeType); err != nil {
		return fmt.Errorf("writing the upload form: %w", err)
	}
	if sign {
		if err := mw.WriteField("sign", "true"); err != nil {
			return fmt.Errorf("writing the upload form: %w", err)
		}
		if err := mw.WriteField("signatureExp", strconv.FormatInt(signatureExpSeconds, 10)); err != nil {
			return fmt.Errorf("writing the upload form: %w", err)
		}
	}
	part, err := mw.CreateFormFile("file", path.Base(remoteName))
	if err != nil {
		return fmt.Errorf("writing the upload form: %w", err)
	}
	if _, err := io.Copy(part, r); err != nil {
		return fmt.Errorf("copying the file into the upload: %w", err)
	}
	return nil
}
