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
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Track kinds
const (
	KindVideo = "video"
	KindAudio = "audio"
)

const (
	videoTimescale90k = 90000
	ticksPerFrame     = 1000
	ffmpegStderrLimit = 4096
)

// TrackInfo describes one CMAF track
type TrackInfo struct {
	Kind      string
	Timescale uint32
	// Codec is the RFC 6381 codec string, such as avc1.64001f
	Codec string
	// Init is the track's initialization segment: ftyp and moov
	Init []byte
}

// Chunk is one CMAF chunk: an optional styp, a moof and its mdat
type Chunk struct {
	Data []byte
	// DecodeTime and Duration are in the track's timescale, DecodeTime counted from the first frame encoded
	DecodeTime, Duration uint64
	// Key reports that the chunk begins with a sync sample
	Key bool
}

// TrackSink receives one track's initialization segment, then its chunks in order
type TrackSink interface {
	Init(TrackInfo)
	Chunk(Chunk)
}

// Encoder turns raw yuv420p frames and s16le stereo samples into encoded media
type Encoder interface {
	// WriteFrame takes the next frame of one rendition, by its index in the ladder
	WriteFrame(rendition int, yuv []byte) error
	WriteAudio(pcm []byte) error
	// Close ends the input and waits for the encoder to deliver everything it has
	Close() error
}

// an Encoder running one ffmpeg process: the top rendition's frames on stdin, samples on fd 3 and the
// other renditions' frames after it, then either one muxed fragmented MP4 on stdout or one output per track
type ffmpeg struct {
	cmd     *exec.Cmd
	video   []*feed
	audio   *feed
	wg      sync.WaitGroup
	errMu   sync.Mutex
	err     error
	stderr  limitedWriter
	stdin   io.WriteCloser
	audioIn io.WriteCloser
	videoIn []io.WriteCloser
}

// one input, written from its own goroutine through a short queue of reused buffers; ffmpeg reads its inputs
// interleaved, so a single writer blocked on one pipe would deadlock against ffmpeg waiting on the other
type feed struct {
	w     io.WriteCloser
	queue chan []byte
	free  chan []byte
	done  chan struct{}
	err   error
}

const feedDepth = 8

func newFeed(w io.WriteCloser) *feed {
	f := &feed{
		w: w, queue: make(chan []byte, feedDepth), free: make(chan []byte, feedDepth),
		done: make(chan struct{}),
	}
	go f.run()
	return f
}

func (f *feed) run() {
	defer close(f.done)
	for b := range f.queue {
		if f.err == nil {
			_, f.err = f.w.Write(b)
		}
		select {
		case f.free <- b:
		default:
		}
	}
	_ = f.w.Close()
}

func (f *feed) write(b []byte) error {
	// queues a copy of b, blocking only while the queue is full
	select {
	case <-f.done:
		return f.err
	default:
	}
	var buf []byte
	select {
	case buf = <-f.free:
	default:
	}
	buf = append(buf[:0], b...)
	select {
	case f.queue <- buf:
		return nil
	case <-f.done:
		return f.err
	}
}

func (f *feed) close() error {
	// drains the queue and closes the pipe
	close(f.queue)
	<-f.done
	return f.err
}

func videoTimescale(fps int) uint32 {
	if videoTimescale90k%fps == 0 {
		return videoTimescale90k
	}
	return uint32(fps * ticksPerFrame) //nolint:gosec // fps is at most maxFPS
}

func microseconds(d time.Duration) string {
	return strconv.FormatInt(d.Microseconds(), 10)
}

func movflags(cfg *Config, muxed bool) []string {
	// makes ffmpeg write CMAF fragments per chunk; a muxed output fragments by duration, since
	// frag_every_frame there repeats the audio decode time in video-only fragments
	flags := "empty_moov+default_base_moof+frag_keyframe+cmaf"
	switch {
	case cfg.Chunk > 0:
		return []string{"-movflags", flags, "-frag_duration", microseconds(cfg.Chunk)}
	case muxed:
		return []string{"-movflags", flags, "-frag_duration", microseconds(cfg.FrameDuration())}
	}
	return []string{"-movflags", flags + "+frag_every_frame"}
}

func videoArgs(cfg *Config) []string {
	gop := strconv.FormatInt(cfg.FramesPerSegment(), 10)
	rate := strconv.Itoa(cfg.VideoBitrate)
	return []string{
		"-c:v", "libx264", "-preset", "veryfast", "-tune", "zerolatency", "-profile:v", "high", "-pix_fmt", "yuv420p",
		"-g", gop, "-keyint_min", gop, "-sc_threshold", "0", "-b:v", rate, "-maxrate", rate,
		"-bufsize", strconv.Itoa(cfg.VideoBitrate * 2), "-color_primaries", "bt709", "-color_trc", "bt709",
		"-colorspace", "bt709", "-color_range", "tv",
		"-video_track_timescale", strconv.FormatUint(uint64(videoTimescale(cfg.FPS)), 10),
	}
}

func audioArgs(cfg *Config) []string {
	return []string{
		"-c:a", "aac", "-b:a", strconv.Itoa(cfg.AudioBitrate), "-ac", strconv.Itoa(audioChannels),
		"-ar", strconv.Itoa(SampleRate),
	}
}

// where the pipes go: fd 3 carries samples, the lower renditions' frames follow, then one output per rendition
// and the audio output; with one rendition, video goes out on fd 4 and audio on fd 5
type fds struct {
	rungs int
}

func (f fds) videoIn(i int) int  { return 3 + i }
func (f fds) videoOut(i int) int { return 3 + f.rungs + i }
func (f fds) audioOut() int      { return 3 + 2*f.rungs }

func pipe(fd int) string {
	return "pipe:" + strconv.Itoa(fd)
}

func rawVideoInput(cfg *Config, r Rendition, fd int) []string {
	return []string{
		"-f", "rawvideo", "-pix_fmt", "yuv420p", "-video_size", fmt.Sprintf("%dx%d", r.Width, r.Height),
		"-framerate", strconv.Itoa(cfg.FPS), "-i", pipe(fd),
	}
}

func inputArgs(cfg *Config, ladder []Rendition) (args []string, video []int, audio int) {
	// inputs are numbered in order: the top rendition, the samples, then the lower renditions
	args = append([]string{"-hide_banner", "-loglevel", "error"}, rawVideoInput(cfg, ladder[0], 0)...)
	video = []int{0}
	next := 1
	if cfg.Audio {
		args = append(args, "-f", "s16le", "-ar", strconv.Itoa(SampleRate), "-ac", strconv.Itoa(audioChannels),
			"-i", pipe(3))
		audio, next = next, next+1
	}
	f := fds{rungs: len(ladder)}
	for i := 1; i < len(ladder); i++ {
		args = append(args, rawVideoInput(cfg, ladder[i], f.videoIn(i))...)
		video = append(video, next)
		next++
	}
	return args, video, audio
}

func muxedArgs(cfg *Config) []string {
	// makes one fragmented MP4 holding the top rendition and the audio, written to stdout
	ladder := cfg.Ladder()[:1]
	args, video, audio := inputArgs(cfg, ladder)
	args = append(args, "-map", strconv.Itoa(video[0])+":v")
	top := cfg.forRendition(ladder[0])
	args = append(args, videoArgs(&top)...)
	if cfg.Audio {
		args = append(args, "-map", strconv.Itoa(audio)+":a")
		args = append(args, audioArgs(cfg)...)
	}
	args = append(args, movflags(cfg, true)...)
	return append(args, "-f", "mp4", "pipe:1")
}

func trackArgs(cfg *Config) []string {
	// makes one fragmented MP4 per track: each rendition's video, then the audio
	ladder := cfg.Ladder()
	f := fds{rungs: len(ladder)}
	args, video, audio := inputArgs(cfg, ladder)
	for i, r := range ladder {
		rc := cfg.forRendition(r)
		args = append(args, "-map", strconv.Itoa(video[i])+":v")
		args = append(args, videoArgs(&rc)...)
		args = append(args, movflags(cfg, false)...)
		args = append(args, "-f", "mp4", pipe(f.videoOut(i)))
	}
	if cfg.Audio {
		args = append(args, "-map", strconv.Itoa(audio)+":a")
		args = append(args, audioArgs(cfg)...)
		args = append(args, movflags(cfg, false)...)
		args = append(args, "-f", "mp4", pipe(f.audioOut()))
	}
	return args
}

// NewMuxedEncoder starts an ffmpeg encoder that writes one muxed fragmented MP4 to w
func NewMuxedEncoder(ctx context.Context, cfg Config, w io.Writer) (Encoder, error) {
	e := &ffmpeg{}
	e.cmd = exec.CommandContext(ctx, cfg.FFmpeg, muxedArgs(&cfg)...) //nolint:gosec // the operator names ffmpeg
	e.cmd.Stdout = w
	return e, e.start(&cfg, 1, nil)
}

// NewTrackEncoder starts an ffmpeg encoder that delivers each rendition's video track to its sink in video, in
// ladder order, and the audio track to audio
func NewTrackEncoder(ctx context.Context, cfg Config, video []TrackSink, audio TrackSink) (Encoder, error) {
	cfg = cfg.normalized()
	if len(video) != len(cfg.Ladder()) {
		return nil, fmt.Errorf("%d video sinks for %d renditions", len(video), len(cfg.Ladder()))
	}
	e := &ffmpeg{}
	e.cmd = exec.CommandContext(ctx, cfg.FFmpeg, trackArgs(&cfg)...) //nolint:gosec // the operator names ffmpeg
	outs := make([]trackOut, 0, len(video)+1)
	for _, v := range video {
		outs = append(outs, trackOut{KindVideo, v})
	}
	if cfg.Audio {
		outs = append(outs, trackOut{KindAudio, audio})
	}
	return e, e.start(&cfg, len(video), outs)
}

type trackOut struct {
	kind string
	sink TrackSink
}

func (e *ffmpeg) start(cfg *Config, rungs int, outs []trackOut) error {
	var err error
	if e.stdin, err = e.cmd.StdinPipe(); err != nil {
		return err
	}
	e.stderr.limit = ffmpegStderrLimit
	e.cmd.Stderr = &e.stderr
	var closeAfterStart []*os.File
	if cfg.Audio {
		r, w, err := os.Pipe()
		if err != nil {
			return err
		}
		e.cmd.ExtraFiles = append(e.cmd.ExtraFiles, r)
		e.audioIn = w
		closeAfterStart = append(closeAfterStart, r)
	} else if len(outs) > 0 {
		// the later fds must stay where the arguments name them, so fd 3 is held by an unused pipe
		r, w, err := os.Pipe()
		if err != nil {
			return err
		}
		_ = w.Close()
		e.cmd.ExtraFiles = append(e.cmd.ExtraFiles, r)
		closeAfterStart = append(closeAfterStart, r)
	}
	for range rungs - 1 {
		r, w, err := os.Pipe()
		if err != nil {
			return err
		}
		e.cmd.ExtraFiles = append(e.cmd.ExtraFiles, r)
		e.videoIn = append(e.videoIn, w)
		closeAfterStart = append(closeAfterStart, r)
	}
	readers := make([]*os.File, 0, len(outs))
	for range outs {
		r, w, err := os.Pipe()
		if err != nil {
			return err
		}
		e.cmd.ExtraFiles = append(e.cmd.ExtraFiles, w)
		readers = append(readers, r)
		closeAfterStart = append(closeAfterStart, w)
	}
	if err = e.cmd.Start(); err != nil {
		for _, f := range closeAfterStart {
			_ = f.Close()
		}
		if e.audioIn != nil {
			_ = e.audioIn.Close()
		}
		for _, w := range e.videoIn {
			_ = w.Close()
		}
		return fmt.Errorf("starting %s: %w", cfg.FFmpeg, err)
	}
	for _, f := range closeAfterStart {
		_ = f.Close()
	}
	e.video = append(e.video, newFeed(e.stdin))
	for _, w := range e.videoIn {
		e.video = append(e.video, newFeed(w))
	}
	if e.audioIn != nil {
		e.audio = newFeed(e.audioIn)
	}
	for i, out := range outs {
		e.wg.Go(func() {
			defer readers[i].Close()
			if err := readTrack(readers[i], out.kind, out.sink); err != nil {
				e.fail(err)
			}
		})
	}
	return nil
}

func (e *ffmpeg) fail(err error) {
	e.errMu.Lock()
	defer e.errMu.Unlock()
	if e.err == nil {
		e.err = err
	}
}

func (e *ffmpeg) WriteFrame(rendition int, yuv []byte) error {
	if rendition < 0 || rendition >= len(e.video) {
		return fmt.Errorf("ffmpeg: no rendition %d", rendition)
	}
	return e.writeErr(e.video[rendition].write(yuv))
}

func (e *ffmpeg) WriteAudio(pcm []byte) error {
	if e.audio == nil {
		return nil
	}
	return e.writeErr(e.audio.write(pcm))
}

func (e *ffmpeg) writeErr(err error) error {
	if err == nil {
		return nil
	}
	if msg := strings.TrimSpace(e.stderr.String()); msg != "" {
		return fmt.Errorf("ffmpeg: %w: %s", err, msg)
	}
	return fmt.Errorf("ffmpeg: %w", err)
}

func (e *ffmpeg) Close() error {
	// both inputs close together, since ffmpeg may wait on either before it finishes
	var wg sync.WaitGroup
	for _, v := range e.video {
		wg.Go(func() { _ = v.close() })
	}
	if e.audio != nil {
		wg.Go(func() { _ = e.audio.close() })
	}
	wg.Wait()
	err := e.cmd.Wait()
	e.wg.Wait()
	e.errMu.Lock()
	defer e.errMu.Unlock()
	if e.err != nil {
		return e.err
	}
	if err != nil {
		return e.writeErr(err)
	}
	return nil
}

func readTrack(r io.Reader, kind string, sink TrackSink) error {
	// splits one fragmented MP4 into its initialization segment and chunks
	br := bufio.NewReaderSize(r, 1<<16)
	var init, pending []byte
	for {
		typ, b, err := readBox(br)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		switch typ {
		case boxFtyp:
			init = append(init[:0], b...)
		case boxMoov:
			init = append(init, b...)
			timescale, codec, err := trackInfo(b)
			if err != nil {
				return err
			}
			sink.Init(TrackInfo{Kind: kind, Timescale: timescale, Codec: codec, Init: init})
		case boxStyp, boxMoof:
			pending = append(pending, b...)
		case boxMdat:
			chunk := append(pending, b...) //nolint:gocritic // pending is handed off whole
			pending = nil
			moof := find(chunk, boxMoof)
			if moof == nil {
				return fmt.Errorf("%w: mdat without moof", errBoxFormat)
			}
			dt, dur, key, err := fragmentTiming(moof)
			if err != nil {
				return err
			}
			sink.Chunk(Chunk{Data: chunk, DecodeTime: dt, Duration: dur, Key: key})
		}
	}
}

// a writer keeping the first limit bytes written, so a chatty process cannot grow memory without bound
type limitedWriter struct {
	mu    sync.Mutex
	buf   bytes.Buffer
	limit int
}

func (l *limitedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if room := l.limit - l.buf.Len(); room > 0 {
		l.buf.Write(p[:min(room, len(p))])
	}
	return len(p), nil
}

func (l *limitedWriter) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}
