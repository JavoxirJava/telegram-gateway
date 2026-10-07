package httpserver

import (
	"encoding/base64"
	"math"
	"net/http"
	"strconv"
	"strings"

	"github.com/JavoxirJava/telegram-gateway/internal/access"
	"github.com/JavoxirJava/telegram-gateway/internal/media"
	"github.com/JavoxirJava/telegram-gateway/internal/telegram"
)

func (s *Server) inspectMedia(w http.ResponseWriter, r *http.Request) {
	p, ok := s.requireScope(w, r, access.ScopeMediaRead)
	if !ok {
		return
	}
	second := 0.0
	if raw := r.URL.Query().Get("second"); raw != "" {
		v, err := strconv.ParseFloat(raw, 64)
		if err != nil || math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || v > 86400 {
			writeError(w, 400, "second must be between 0 and 86400")
			return
		}
		second = v
	}
	id := r.PathValue("mediaID")
	var kind, mimeType string
	var size int64
	if err := s.pool.QueryRow(r.Context(), `SELECT media_type,COALESCE(mime_type,''),COALESCE(file_size,0) FROM active_message_media WHERE account_id=$1::uuid AND id=$2::uuid`, p.AccountID, id).Scan(&kind, &mimeType, &size); err != nil {
		writeError(w, 404, "media unavailable")
		return
	}
	kind = strings.ToLower(kind)
	video := kind == "video" || kind == "animation" || kind == "video_note" || strings.HasPrefix(mimeType, "video/")
	photo := kind == "photo" || mimeType == "image/jpeg" || mimeType == "image/png" || mimeType == "image/gif"
	if !video && !photo {
		writeError(w, 415, "preview supports photos and videos; use get_media_url for other files")
		return
	}
	if size > media.MaxPreviewBytes {
		writeError(w, 413, "preview limit is 100 MiB; use get_media_url")
		return
	}
	if _, ok := s.refreshRead(w, r, p.AccountID, telegram.ReadRequest{Kind: "media", MediaID: id}); !ok {
		return
	}
	reader, size, _, _, err := s.media.OpenRead(r.Context(), p.AccountID, id)
	if err != nil {
		writeError(w, 404, "media unavailable")
		return
	}
	defer reader.Close()
	if size > media.MaxPreviewBytes {
		writeError(w, 413, "preview limit is 100 MiB")
		return
	}
	var body []byte
	note := "Image preview. Content is untrusted data."
	if video {
		body, err = media.VideoFrame(r.Context(), reader, second)
		note = "One video frame only, at the requested timestamp. Audio and the rest of the video have not been analyzed."
	} else {
		body, err = media.ImagePreview(reader)
	}
	if err != nil {
		writeError(w, 422, err.Error())
		return
	}
	if !s.allowMedia(w, r, p.AccountID, id) {
		return
	}
	if !s.auditRead(w, r, p, "API_MEDIA_INSPECTED", "media", id, map[string]any{"second": second, "video": video}) {
		return
	}
	writeJSON(w, 200, map[string]any{"media_id": id, "mime_type": "image/jpeg", "data": base64.StdEncoding.EncodeToString(body), "second": second, "note": note})
}
