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
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

const (
	testVideoCodec = "avc1.64001f"
	testTicks      = 9000 // one frame at 10fps in a 90 kHz timescale
)

// testStreamer is a stream that started ago, with segments of synthetic video and audio chunks fed in
func testStreamer(t *testing.T, cfg Config, ago time.Duration, segments int) *Streamer {
	t.Helper()
	s, err := newStreamer(cfg, func() time.Time { return time.Now().Add(-ago) })
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range s.videos {
		init := "vinit-" + v.rendition.Name()
		v.Init(TrackInfo{Kind: KindVideo, Timescale: videoTimescale90k, Codec: testVideoCodec, Init: []byte(init)})
	}
	if s.audio != nil {
		s.audio.Init(TrackInfo{Kind: KindAudio, Timescale: SampleRate, Codec: codecAAC, Init: []byte("ainit")})
	}
	feedSegments(s, 0, segments)
	return s
}

// feedSegments delivers frames for segments [from, to) of the stream, then the first frame of segment to
func feedSegments(s *Streamer, from, to int) {
	per := int(s.cfg.FramesPerSegment())
	feedFrames(s, from*per, to*per)
}

// feedFrames delivers frames first through last of the stream
func feedFrames(s *Streamer, first, last int) {
	per := int(s.cfg.FramesPerSegment())
	for i := first; i <= last; i++ {
		for _, v := range s.videos {
			v.Chunk(Chunk{Data: []byte{byte(i)}, DecodeTime: uint64(i * testTicks), Duration: testTicks, Key: i%per == 0})
		}
		if s.audio != nil {
			s.audio.Chunk(Chunk{Data: []byte{byte(i)}, DecodeTime: uint64(i * SampleRate / s.cfg.FPS),
				Duration: uint64(SampleRate / s.cfg.FPS)})
		}
	}
}

func TestTrackSegments(t *testing.T) {
	s := testStreamer(t, testConfig(), 0, 0)
	v := s.video
	v.Chunk(Chunk{Data: []byte{1}, DecodeTime: 0, Duration: testTicks}) // no keyframe: warned, kept
	if v.cur == nil || len(v.cur.chunks) != 2 {
		t.Fatal("chunks of one segment collect together")
	}
	feedSegments(s, 0, 8)
	if v.newest != v.startSeg+7 || v.oldest != v.startSeg+2 || v.segs[v.startSeg+1] != nil {
		t.Errorf("retention keeps two windows: oldest %d newest %d", v.oldest-v.startSeg, v.newest-v.startSeg)
	}
	w := v.window(3)
	if len(w) != 3 || w[0].number != v.startSeg+5 || w[2].duration != time.Second || w[2].size != 10 {
		t.Fatalf("window %+v", w)
	}
	if !w[0].start.Equal(s.p.startTime().Add(5 * time.Second)) {
		t.Error("segment start time")
	}
	ctx := context.Background()
	if v.segment(ctx, v.startSeg+6, 0) != w[1] {
		t.Error("a complete segment is returned at once")
	}
	if v.segment(ctx, v.startSeg+20, time.Hour) != nil || v.segment(ctx, v.startSeg, time.Hour) != nil {
		t.Error("segments far ahead or dropped are missing")
	}
	go func() {
		time.Sleep(20 * time.Millisecond)
		feedSegments(s, 8, 9)
	}()
	if seg := v.segment(ctx, v.startSeg+8, 5*time.Second); seg == nil || !seg.complete {
		t.Error("the segment in progress is waited for")
	}
	if v.segment(ctx, v.startSeg+10, 30*time.Millisecond) != nil {
		t.Error("a segment that has not begun times out")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if v.segment(cancelled, v.startSeg+9, time.Hour) != nil || v.segment(cancelled, v.startSeg+10, time.Hour) != nil {
		t.Error("waits end with the context")
	}
	v.finish()
	if v.segment(ctx, v.startSeg+9, 0) == nil || v.segment(ctx, v.startSeg+10, time.Hour) != nil {
		t.Error("finish completes the last segment and nothing more is due")
	}
	var early track
	early.Chunk(Chunk{Data: []byte{1}})
	if early.cur != nil {
		t.Error("chunks before the init segment are dropped")
	}
	v.Init(TrackInfo{Codec: "other"})
	if v.info.Codec != testVideoCodec {
		t.Error("the first init segment stays")
	}
}

func get(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	return w
}

func TestHandler(t *testing.T) {
	s := testStreamer(t, testConfig(), time.Minute, 5)
	h := s.Handler()
	first := s.video.startSeg
	media := "/media/" + s.streamID + "/"
	for path, want := range map[string]string{
		"/":                           "dash/manifest.mpd",
		"/hls/master.m3u8":            `CODECS="avc1.64001f,mp4a.40.2"`,
		"/hls/video.m3u8":             "#EXT-X-MEDIA-SEQUENCE:" + strconv.FormatInt(first+2, 10),
		"/hls/audio.m3u8":             "../media/" + s.streamID + "/audio/init.mp4",
		"/hls/subtitles.m3u8":         strconv.FormatInt(first+4, 10) + ".vtt",
		"/dash/manifest.mpd":          `startNumber="` + strconv.FormatInt(first, 10) + `"`,
		media + "video/180p/init.mp4": "vinit-180p",
		media + "audio/init.mp4":      "ainit",
		media + "text/" + strconv.FormatInt(first+1, 10) + ".vtt": "00:00:01.000 --> 00:00:02.000",
	} {
		w := get(t, h, path)
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), want) {
			t.Errorf("%s: %d does not have %q:\n%s", path, w.Code, want, w.Body)
		}
	}
	w := get(t, h, media+"video/180p/"+strconv.FormatInt(first+3, 10)+segmentExt)
	if w.Code != http.StatusOK || w.Body.Len() != 10 || w.Header().Get(headerCC) != ccImmutable ||
		w.Header().Get(headerCT) != "video/mp4" {
		t.Errorf("segment: %d %d %v", w.Code, w.Body.Len(), w.Header())
	}
	if w = get(t, h, "/hls/video.m3u8"); w.Header().Get(headerCC) != "public, max-age=1" {
		t.Errorf("manifests are short-lived: %v", w.Header())
	}
	if w = get(t, h, "/time"); w.Code != http.StatusOK {
		t.Error("time")
	} else if at, err := time.Parse(timeLayout, w.Body.String()); err != nil || time.Since(at) > time.Minute {
		t.Errorf("time %s %v", w.Body, err)
	}
	if w = get(t, h, media+"text/init.mp4"); !bytes.Equal(w.Body.Bytes(), s.textInit) {
		t.Error("text init")
	}
	if w = get(t, h, media+"text/"+strconv.FormatInt(first+2, 10)+segmentExt); w.Code != http.StatusOK ||
		w.Header().Get(headerCT) != ctMP4App {
		t.Errorf("wvtt segment %d", w.Code)
	}
	for _, path := range []string{"/hls/other.m3u8", "/hls/video-1080p.m3u8", "/media/s1/video/180p/init.mp4",
		media + "data/1.m4s", media + "video/init.mp4", media + "video/1080p/init.mp4",
		media + "video/180p/first.m4s", media + "video/180p/1.mp4",
		media + "video/180p/" + strconv.FormatInt(first-1, 10) + ".m4s",
		media + "text/" + strconv.FormatInt(first-1, 10) + ".vtt", media + "text/9999999999999.m4s", media + "text/x.vtt"} {
		if w = get(t, h, path); w.Code != http.StatusNotFound {
			t.Errorf("%s: %d", path, w.Code)
		}
	}
	mpd := s.mpd(time.Now())
	for _, want := range []string{`timescale="90000" duration="90000"`, `codecs="wvtt"`, `value="../time"`,
		`timeShiftBufferDepth="PT3S"`, `audioSamplingRate="48000"`, `<Period id="` + s.streamID + `"`} {
		if !strings.Contains(mpd, want) {
			t.Errorf("the MPD has %s:\n%s", want, mpd)
		}
	}
}

func TestHandlerWithoutAudioOrCaptions(t *testing.T) {
	cfg := testConfig()
	cfg.Audio, cfg.Captions = false, false
	s := testStreamer(t, cfg, time.Minute, 3)
	h := s.Handler()
	if w := get(t, h, "/hls/master.m3u8"); strings.Contains(w.Body.String(), "AUDIO") ||
		strings.Contains(w.Body.String(), "SUBTITLES") || !strings.Contains(w.Body.String(), `CODECS="avc1.64001f"`) {
		t.Errorf("master:\n%s", w.Body)
	}
	if strings.Contains(s.mpd(time.Now()), "wvtt") {
		t.Error("no text adaptation set")
	}
	for _, path := range []string{"/hls/audio.m3u8", "/hls/subtitles.m3u8", "/media/" + s.streamID + "/audio/init.mp4",
		"/media/" + s.streamID + "/text/init.mp4"} {
		if w := get(t, h, path); w.Code != http.StatusNotFound {
			t.Errorf("%s: %d", path, w.Code)
		}
	}
	if s.bandwidth(s.video) != 330_000 {
		t.Errorf("bandwidth %d", s.bandwidth(s.video))
	}
}

func TestHandlerStarting(t *testing.T) {
	s, err := New(testConfig())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, path := range []string{"/hls/master.m3u8", "/dash/manifest.mpd", "/media/" + s.streamID + "/video/180p/init.mp4"} {
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil).WithContext(ctx))
		if w.Code != http.StatusServiceUnavailable {
			t.Errorf("%s: %d", path, w.Code)
		}
	}
	if s.mediaPlaylist(s.video) == "" || !strings.Contains(s.subtitlesPlaylist(), "#EXT-X-MEDIA-SEQUENCE:") {
		t.Error("empty playlists render")
	}
}

func TestCaptionSegments(t *testing.T) {
	cfg := testConfig()
	cfg.Segment = 2 * time.Second
	s := testStreamer(t, cfg, time.Minute, 0)
	n := s.video.startSeg + 3
	vtt := s.webVTTSegment(n)
	cue := captionText(cfg.Title, s.p.startTime().Add(7*time.Second))
	if !strings.HasPrefix(vtt, "WEBVTT\nX-TIMESTAMP-MAP=MPEGTS:0,") || !strings.Contains(vtt, "00:00:07.000 --> 00:00:08.000\n"+cue) ||
		strings.Count(vtt, "-->") != 2 {
		t.Errorf("webvtt:\n%s", vtt)
	}
	seg := s.wvttSegment(n)
	moof := find(seg, boxMoof)
	dt, dur, key, err := fragmentTiming(moof)
	if err != nil || dt != 6000 || dur != 2000 || !key {
		t.Errorf("wvtt timing %d %d %v %v", dt, dur, key, err)
	}
	trun := find(moof[boxHeader:], boxTraf, boxTrun)
	offset := binary.BigEndian.Uint32(trun[boxHeader+8:])
	if string(seg[offset+4:offset+8]) != "vttc" || !bytes.Contains(seg, []byte(cue)) {
		t.Error("the data offset points at the first cue")
	}
}

type fakeEncoder struct {
	frames, audio int
	err           error
}

func (f *fakeEncoder) WriteFrame(int, []byte) error { f.frames++; return f.err }
func (f *fakeEncoder) WriteAudio(b []byte) error {
	f.audio += len(b)
	return nil
}
func (f *fakeEncoder) Close() error { return nil }

func TestProducer(t *testing.T) {
	clock := time.Date(2026, 10, 20, 6, 0, 0, 500_000_000, time.UTC)
	cfg := testConfig()
	p, err := newProducer(cfg, cfg.Ladder(), func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	if !p.startTime().Equal(clock.Add(500 * time.Millisecond)) {
		t.Errorf("encoding starts on the next segment boundary, not %s", p.startTime())
	}
	p.sleep = func(_ context.Context, d time.Duration) error {
		clock = clock.Add(d)
		return nil
	}
	var enc fakeEncoder
	if err = p.run(context.Background(), &enc, 20); err != nil || enc.frames != 20 || enc.audio != 20*SampleRate/10*4 {
		t.Errorf("frames %d, audio bytes %d, %v", enc.frames, enc.audio, err)
	}
	// far behind the clock, frames are written without waiting
	clock = clock.Add(time.Hour)
	enc = fakeEncoder{err: errors.New("broken")}
	if err = p.run(context.Background(), &enc, 5); err == nil || enc.frames != 1 {
		t.Errorf("an encoder error stops the run: %v", err)
	}
	p.sleep = sleepContext
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	p.now = time.Now
	if err = p.run(ctx, &enc, 0); err != nil {
		t.Errorf("a cancelled run ends cleanly: %v", err)
	}
	if sleepContext(context.Background(), time.Millisecond) != nil || sleepContext(ctx, time.Hour) == nil ||
		sleepContext(ctx, 0) == nil {
		t.Error("sleepContext")
	}
}

func TestRunWithoutFFmpeg(t *testing.T) {
	cfg := testConfig()
	cfg.FFmpeg = "/nonexistent/ffmpeg"
	ctx := context.Background()
	if err := Run(ctx, Config{}); err == nil {
		t.Error("an invalid config is refused")
	}
	cfg.Output, cfg.MoQURL = OutputMoQ, "moqt://localhost:8490/"
	if err := Run(ctx, cfg); err == nil || !strings.Contains(err.Error(), "starting") {
		t.Errorf("moq: %v", err)
	}
	cfg.Output, cfg.Listen = OutputHTTP, "127.0.0.1:0"
	if err := Run(ctx, cfg); err == nil || !strings.Contains(err.Error(), "starting") {
		t.Errorf("http: %v", err)
	}
	cfg.Listen = "256.0.0.1:0"
	if err := Run(ctx, cfg); err == nil {
		t.Error("a bad listen address is refused")
	}
	if err := StreamEncoder(ctx, cfg, io.Discard); err == nil {
		t.Error("fmp4 needs ffmpeg")
	}
	if _, err := StreamHLS(ctx, cfg); err != nil {
		t.Errorf("the handler is ready before the stream: %v", err)
	}
	if _, err := StreamDASH(ctx, Config{}); err == nil {
		t.Error("an invalid config has no handler")
	}
	if StreamEncoder(ctx, Config{}, io.Discard) == nil {
		t.Error("an invalid config has no encoder")
	}
}

type failingWriter struct{ closed bool }

func (f *failingWriter) Write([]byte) (int, error) { return 0, errors.New("pipe closed") }
func (f *failingWriter) Close() error              { f.closed = true; return nil }

func TestFeedAndLimits(t *testing.T) {
	var fw failingWriter
	f := newFeed(&fw)
	_ = f.write([]byte("one"))
	<-time.After(10 * time.Millisecond)
	for range feedDepth + 2 {
		_ = f.write([]byte("more"))
	}
	if err := f.close(); err == nil || !fw.closed {
		t.Errorf("the write error is kept and the pipe closed: %v", err)
	}
	if f.write([]byte("late")) == nil {
		t.Error("writes after the pipe failed are refused")
	}
	l := limitedWriter{limit: 4}
	if n, _ := l.Write([]byte("abcdef")); n != 6 || l.String() != "abcd" {
		t.Error("the writer keeps only its limit")
	}
	e := &ffmpeg{}
	e.stderr.limit = 64
	_, _ = e.stderr.Write([]byte("Unknown encoder\n"))
	if err := e.writeErr(io.ErrClosedPipe); err == nil || !strings.Contains(err.Error(), "Unknown encoder") {
		t.Errorf("errors carry ffmpeg's output: %v", err)
	}
	if e.writeErr(nil) != nil || (&ffmpeg{}).writeErr(io.ErrClosedPipe).Error() != "ffmpeg: io: read/write on closed pipe" {
		t.Error("writeErr")
	}
}

func TestEncoderArgs(t *testing.T) {
	cfg := testConfig()
	joined := func(args []string) string { return strings.Join(args, " ") }
	if got := joined(movflags(&cfg, false)); !strings.HasSuffix(got, "+cmaf+frag_every_frame") {
		t.Errorf("track output fragments every frame: %s", got)
	}
	if got := joined(movflags(&cfg, true)); !strings.HasSuffix(got, "-frag_duration 100000") {
		t.Errorf("muxed output fragments by duration: %s", got)
	}
	cfg.Chunk = 500 * time.Millisecond
	if got := joined(movflags(&cfg, false)); !strings.HasSuffix(got, "-frag_duration 500000") {
		t.Errorf("chunks set the fragment duration: %s", got)
	}
	if got := joined(trackArgs(&cfg)); !strings.Contains(got, "pipe:4") || !strings.Contains(got, "pipe:5") ||
		!strings.Contains(got, "-g 10") {
		t.Errorf("track args: %s", got)
	}
	cfg.Audio = false
	if got := joined(muxedArgs(&cfg)); strings.Contains(got, "pipe:3") || strings.Contains(got, "aac") {
		t.Errorf("no audio input or encoder: %s", got)
	}
	if videoTimescale(30) != videoTimescale90k || videoTimescale(7) != 7000 {
		t.Error("video timescale")
	}
}

func TestLadderArgs(t *testing.T) {
	cfg := testConfig()
	cfg.Renditions, _ = ParseRenditions("320x180@300k,256x144@150k,192x108@100k")
	cfg.Width, cfg.Height = 192, 108 // the ladder decides the top rung
	got := strings.Join(trackArgs(&cfg), " ")
	// inputs: the top rung on stdin, the samples on fd 3, the lower rungs on fds 4 and 5; outputs on fds 6 to 9
	for _, want := range []string{"-video_size 320x180 -framerate 10 -i pipe:0", "-i pipe:3",
		"-video_size 256x144 -framerate 10 -i pipe:4", "-video_size 192x108 -framerate 10 -i pipe:5",
		"-map 0:v", "-map 2:v", "-map 3:v", "-map 1:a", "pipe:6", "pipe:7", "pipe:8", "pipe:9", "-b:v 150000"} {
		if !strings.Contains(got, want) {
			t.Errorf("track args lack %q:\n%s", want, got)
		}
	}
	muxed := strings.Join(muxedArgs(&cfg), " ")
	if strings.Contains(muxed, "pipe:4") || !strings.Contains(muxed, "-b:v 300000") {
		t.Errorf("the muxed output carries the top rung only:\n%s", muxed)
	}
	cfg.Audio = false
	if got = strings.Join(trackArgs(&cfg), " "); !strings.Contains(got, "-map 1:v") || strings.Contains(got, "pipe:9") {
		t.Errorf("without audio, the lower rungs are inputs 1 and 2:\n%s", got)
	}
}

func TestLadderManifests(t *testing.T) {
	cfg := testConfig()
	cfg.Renditions, _ = ParseRenditions("320x180@300k,256x144@150k")
	s := testStreamer(t, cfg, time.Minute, 3)
	h := s.Handler()
	master := get(t, h, "/hls/master.m3u8").Body.String()
	for _, want := range []string{"RESOLUTION=320x180", "RESOLUTION=256x144", "video-180p.m3u8", "video-144p.m3u8",
		"BANDWIDTH=235400"} {
		if !strings.Contains(master, want) {
			t.Errorf("the multivariant playlist lacks %s:\n%s", want, master)
		}
	}
	if w := get(t, h, "/hls/video-144p.m3u8"); w.Code != http.StatusOK ||
		!strings.Contains(w.Body.String(), "/video/144p/init.mp4") {
		t.Errorf("rung playlist: %d\n%s", w.Code, w.Body)
	}
	mpd := s.mpd(time.Now())
	for _, want := range []string{`<Representation id="180p"`, `<Representation id="144p"`, `bandwidth="150000"`,
		"video/$RepresentationID$/$Number$.m4s", `maxWidth="320"`} {
		if !strings.Contains(mpd, want) {
			t.Errorf("the MPD lacks %s:\n%s", want, mpd)
		}
	}
	if strings.Count(mpd, `contentType="video"`) != 1 {
		t.Error("the renditions share one adaptation set")
	}
	media := "/media/" + s.streamID + "/video/144p/"
	if w := get(t, h, media+"init.mp4"); w.Body.String() != "vinit-144p" {
		t.Errorf("rung init %q", w.Body)
	}
	if w := get(t, h, media+strconv.FormatInt(s.video.startSeg+1, 10)+segmentExt); w.Code != http.StatusOK {
		t.Errorf("rung segment %d", w.Code)
	}
}
