package downloader

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"bili-saver/internal/biliapi"
	"bili-saver/internal/fileio"
)

type resumeIdentity struct {
	Version  int    `json:"version"`
	Identity string `json:"identity"`
	ETag     string `json:"etag"`
	Size     int64  `json:"size"`
}
type resumeKey struct{}

// Only strong validators authorize reuse. Unvalidated partial staging can be
// restarted safely; finished archive objects are never touched here.
func (d *Downloader) prepareResume(ctx context.Context, s *biliapi.DashStream, path string) (context.Context, *resumeIdentity, bool, error) {
	probeCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var resp *http.Response
	var err error
	for _, candidate := range d.streamURLs(s) {
		req, e := http.NewRequestWithContext(probeCtx, http.MethodGet, candidate, nil)
		if e != nil {
			err = e
			continue
		}
		req.Header.Set("Range", "bytes=0-0")
		req.Header.Set("User-Agent", d.API.UserAgent())
		req.Header.Set("Referer", "https://www.bilibili.com/")
		resp, err = d.API.DownloadClient().Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return ctx, nil, false, ctx.Err()
			}
			continue
		}
		if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusPartialContent {
			break
		}
		err = fmt.Errorf("resume probe returned HTTP %d", resp.StatusCode)
		resp.Body.Close()
		resp = nil
	}
	if resp == nil || err != nil {
		if err == nil {
			err = fmt.Errorf("no downloadable stream URL")
		}
		return ctx, nil, false, err
	}
	defer resp.Body.Close()
	ranged := resp.StatusCode == http.StatusPartialContent
	size := resp.ContentLength
	if ranged {
		var start, end int64
		if _, err = fmt.Sscanf(resp.Header.Get("Content-Range"), "bytes %d-%d/%d", &start, &end, &size); err != nil || start != 0 || end != 0 || size < 1 {
			return ctx, nil, false, fmt.Errorf("invalid resume probe range")
		}
	} else if resp.StatusCode != http.StatusOK {
		return ctx, nil, false, fmt.Errorf("resume probe returned HTTP %d", resp.StatusCode)
	}
	etag := resp.Header.Get("ETag")
	if !strings.HasPrefix(etag, `"`) || strings.HasPrefix(etag, "W/") {
		etag = ""
	}
	parsed, err := url.Parse(s.BaseURL)
	if err != nil {
		return ctx, nil, false, err
	}
	// URLs can rotate signed query parameters; identity includes selected stream
	// properties and path, with the strong ETag and total byte length as validators.
	body, _ := json.Marshal([]any{s.ID, s.Codecid, s.Codecs, s.MimeType, s.Bandwidth, s.Width, s.Height, s.FrameRate, parsed.EscapedPath(), etag, size})
	state := &resumeIdentity{Version: 1, Identity: fmt.Sprintf("%x", sha256.Sum256(body)), ETag: etag, Size: size}
	oldBody, readErr := os.ReadFile(path + ".resume.json")
	var old resumeIdentity
	reusable := readErr == nil && json.Unmarshal(oldBody, &old) == nil && old.Version == 1 && old.Identity == state.Identity && etag != "" && size > 0
	if !reusable {
		for _, p := range []string{path, path + segExt} {
			if err = os.Remove(p); err != nil && !os.IsNotExist(err) {
				return ctx, nil, false, err
			}
		}
	}
	if _, err = fileio.WriteBytes(path+".resume.json", mustJSON(state)); err != nil {
		return ctx, nil, false, err
	}
	return context.WithValue(ctx, resumeKey{}, state), state, ranged && etag != "", nil
}
func mustJSON(value any) []byte {
	b, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return b
}
func validateResponse(ctx context.Context, resp *http.Response, total int64) error {
	if state, ok := ctx.Value(resumeKey{}).(*resumeIdentity); ok {
		if state.ETag != "" && resp.Header.Get("ETag") != state.ETag {
			return fmt.Errorf("remote stream validator changed; restart the download")
		}
		if state.Size > 0 && total > 0 && state.Size != total {
			return fmt.Errorf("remote stream size changed; restart the download")
		}
	}
	return nil
}
