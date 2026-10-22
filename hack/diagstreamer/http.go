/*
 * Copyright 2026 The Trickster Authors
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package diagstreamer

import (
	_ "embed"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	mediaDir    = "media"
	initName    = "init.mp4"
	segmentExt  = ".m4s"
	playlistExt = ".m3u8"
	timeLayout  = "2006-01-02T15:04:05.000Z"
	headerCC    = "Cache-Control"
	headerCT    = "Content-Type"
	ctHLS       = "application/vnd.apple.mpegurl"
	ctDASH      = "application/dash+xml"
	ctText      = "text/plain; charset=utf-8"
	ctHTML      = "text/html; charset=utf-8"
	ctVTT       = "text/vtt; charset=utf-8"
	ctMP4App    = "application/mp4"
	mp4Suffix   = "/mp4"
	ccImmutable = "public, max-age=86400, immutable"
	ccNoStore   = "no-store"
)

//go:embed assets/player.html
var playerHTML []byte

// Handler serves the live stream: HLS under /hls/, DASH under /dash/, the shared CMAF media under /media/, the
// clock DASH clients synchronize with at /time, and a player page at /
func (s *Streamer) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set(headerCT, ctHTML)
		w.Header().Set(headerCC, ccNoStore)
		_, _ = w.Write(playerHTML)
	})
	mux.HandleFunc("GET /time", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set(headerCT, ctText)
		w.Header().Set(headerCC, ccNoStore)
		_, _ = w.Write([]byte(time.Now().UTC().Format(timeLayout)))
	})
	mux.HandleFunc("GET /hls/{playlist}", s.serveHLS)
	mux.HandleFunc("GET /dash/manifest.mpd", func(w http.ResponseWriter, r *http.Request) {
		if !s.ready(r.Context()) {
			http.Error(w, "the stream is starting", http.StatusServiceUnavailable)
			return
		}
		s.manifest(w, ctDASH, s.mpd(time.Now()))
	})
	mux.HandleFunc("GET /"+mediaDir+"/{stream}/{kind}/{name}", s.serveMedia)
	mux.HandleFunc("GET /"+mediaDir+"/{stream}/"+KindVideo+"/{rendition}/{name}", s.serveMedia)
	return mux
}

func (s *Streamer) manifestCC() string {
	// lets a cache in front hold a manifest for at most half a segment, as live origins usually do
	return "public, max-age=" + strconv.Itoa(max(int(s.cfg.Segment.Seconds()/2), 1))
}

func (s *Streamer) manifest(w http.ResponseWriter, contentType, body string) {
	w.Header().Set(headerCT, contentType)
	w.Header().Set(headerCC, s.manifestCC())
	_, _ = w.Write([]byte(body))
}

func (s *Streamer) serveHLS(w http.ResponseWriter, r *http.Request) {
	if !s.ready(r.Context()) {
		http.Error(w, "the stream is starting", http.StatusServiceUnavailable)
		return
	}
	switch r.PathValue("playlist") {
	case "master.m3u8":
		s.manifest(w, ctHLS, s.multivariantPlaylist())
	case "video.m3u8":
		// the top rendition, as a stream of one rendition is played without the multivariant playlist
		s.manifest(w, ctHLS, s.mediaPlaylist(s.video))
	case "audio.m3u8":
		if s.audio == nil {
			http.NotFound(w, r)
			return
		}
		s.manifest(w, ctHLS, s.mediaPlaylist(s.audio))
	case "subtitles.m3u8":
		if !s.cfg.Captions {
			http.NotFound(w, r)
			return
		}
		s.manifest(w, ctHLS, s.subtitlesPlaylist())
	default:
		name, ok := strings.CutPrefix(strings.TrimSuffix(r.PathValue("playlist"), playlistExt), KindVideo+"-")
		if t := s.rendition(name); ok && t != nil {
			s.manifest(w, ctHLS, s.mediaPlaylist(t))
			return
		}
		http.NotFound(w, r)
	}
}

func (s *Streamer) trackFor(r *http.Request) *track {
	// a video rendition is named in its own path segment; audio has one track
	if rendition := r.PathValue("rendition"); rendition != "" {
		return s.rendition(rendition)
	}
	if r.PathValue("kind") == KindAudio {
		return s.audio
	}
	return nil
}

func (s *Streamer) serveMedia(w http.ResponseWriter, r *http.Request) {
	if r.PathValue("stream") != s.streamID {
		http.NotFound(w, r)
		return
	}
	if r.PathValue("kind") == KindText {
		s.serveText(w, r)
		return
	}
	t := s.trackFor(r)
	if t == nil {
		http.NotFound(w, r)
		return
	}
	name := r.PathValue("name")
	if name == initName {
		if !s.ready(r.Context()) {
			http.Error(w, "the stream is starting", http.StatusServiceUnavailable)
			return
		}
		s.media(w, t.kind+mp4Suffix, t.info.Init)
		return
	}
	n, err := strconv.ParseInt(strings.TrimSuffix(name, segmentExt), 10, 64)
	if err != nil || !strings.HasSuffix(name, segmentExt) {
		http.NotFound(w, r)
		return
	}
	// a request a little ahead of the segment's completion is held until it completes
	seg := t.segment(r.Context(), n, s.cfg.Segment+time.Second)
	if seg == nil {
		http.NotFound(w, r)
		return
	}
	s.segmentMedia(w, t.kind+mp4Suffix, seg)
}

func (s *Streamer) segmentMedia(w http.ResponseWriter, contentType string, seg *segment) {
	// a segment is written chunk by chunk, as a segment still in progress would be
	w.Header().Set(headerCT, contentType)
	w.Header().Set(headerCC, ccImmutable)
	w.Header().Set("Content-Length", strconv.Itoa(seg.size))
	for _, c := range seg.chunks {
		if _, err := w.Write(c.Data); err != nil {
			return
		}
	}
}

func (s *Streamer) media(w http.ResponseWriter, contentType string, b []byte) {
	w.Header().Set(headerCT, contentType)
	w.Header().Set(headerCC, ccImmutable)
	w.Header().Set("Content-Length", strconv.Itoa(len(b)))
	_, _ = w.Write(b)
}

func (s *Streamer) serveText(w http.ResponseWriter, r *http.Request) {
	// builds a caption segment for any segment number from the stream's start to the one now playing
	if !s.cfg.Captions {
		http.NotFound(w, r)
		return
	}
	name := r.PathValue("name")
	if name == initName {
		s.media(w, ctMP4App, s.textInit)
		return
	}
	ext := segmentExt
	if strings.HasSuffix(name, vttExt) {
		ext = vttExt
	}
	n, err := strconv.ParseInt(strings.TrimSuffix(name, ext), 10, 64)
	if err != nil || !strings.HasSuffix(name, ext) || n < s.video.startSeg || n > s.p.tl.segment(s.p.tl.frameAt(time.Now())) {
		http.NotFound(w, r)
		return
	}
	if ext == vttExt {
		w.Header().Set(headerCT, ctVTT)
		w.Header().Set(headerCC, ccImmutable)
		_, _ = w.Write([]byte(s.webVTTSegment(n)))
		return
	}
	s.media(w, ctMP4App, s.wvttSegment(n))
}
