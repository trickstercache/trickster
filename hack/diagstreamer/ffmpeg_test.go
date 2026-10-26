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
	"crypto/tls"
	"image"
	"net"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/moq/codec"
	"github.com/trickstercache/trickster/v2/pkg/moq/message"
	"github.com/trickstercache/trickster/v2/pkg/moq/relay"
	"github.com/trickstercache/trickster/v2/pkg/moq/session"
	testtls "github.com/trickstercache/trickster/v2/pkg/testutil/tls"

	"github.com/quic-go/quic-go"
)

func needFFmpeg(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath(defaultFFmpeg)
	if err != nil {
		t.Skip("ffmpeg is not installed")
	}
	return path
}

func TestMuxedEncoderEndToEnd(t *testing.T) {
	ffmpegPath := needFFmpeg(t)
	t.Parallel()
	cfg := testConfig()
	cfg.FFmpeg = ffmpegPath
	var out bytes.Buffer
	if err := streamEncoder(context.Background(), cfg, &out, 15); err != nil {
		t.Fatal(err)
	}
	var types []string
	traks := 0
	if err := children(out.Bytes(), func(typ string, b []byte) error {
		types = append(types, typ)
		if typ == boxMoov {
			_ = children(b[boxHeader:], func(child string, _ []byte) error {
				if child == boxTrak {
					traks++
				}
				return nil
			})
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(types) < 4 || types[0] != boxFtyp || types[1] != boxMoov || traks != 2 || !strings.Contains(strings.Join(types, ","), "moof,mdat") {
		t.Fatalf("boxes %v with %d traks", types, traks)
	}
	// the first decoded frame's timing strip survives encoding and names a segment boundary
	cmd := exec.Command(ffmpegPath, "-v", "error", "-i", "pipe:0", "-frames:v", "1", "-f", "rawvideo", "-pix_fmt",
		"yuv420p", "pipe:1")
	cmd.Stdin = &out
	raw, err := cmd.Output()
	if err != nil || len(raw) != cfg.Width*cfg.Height*3/2 {
		t.Fatalf("decoding %d bytes: %v", len(raw), err)
	}
	ys, cs := cfg.Width*cfg.Height, cfg.Width*cfg.Height/4
	img := &image.YCbCr{Y: raw[:ys], Cb: raw[ys : ys+cs], Cr: raw[ys+cs:], YStride: cfg.Width, CStride: cfg.Width / 2,
		SubsampleRatio: image.YCbCrSubsampleRatio420, Rect: image.Rect(0, 0, cfg.Width, cfg.Height)}
	got, ok := DecodeTiming(img)
	if !ok || got.At.UnixMilli()%cfg.Segment.Milliseconds() != 0 ||
		got.Frame != uint32(got.At.UnixMilli()*int64(cfg.FPS)/1000) || time.Since(got.At) > time.Minute {
		t.Errorf("decoded %+v %v", got, ok)
	}
}

func TestTrackEncoderEndToEnd(t *testing.T) {
	ffmpegPath := needFFmpeg(t)
	t.Parallel()
	cfg := testConfig()
	cfg.FFmpeg = ffmpegPath
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	wait, waitCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer waitCancel()
	seg := s.video.segment(wait, s.video.startSeg, 10*time.Second)
	aseg := s.audio.segment(wait, s.audio.startSeg, 10*time.Second)
	cancel()
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	if seg == nil || aseg == nil {
		t.Fatal("the first segments complete")
	}
	if !strings.HasPrefix(s.video.info.Codec, codecAVC1+".") || s.audio.info.Codec != codecAAC ||
		s.video.info.Timescale != videoTimescale90k || s.audio.info.Timescale != SampleRate {
		t.Errorf("tracks %+v %+v", s.video.info.Codec, s.audio.info.Codec)
	}
	if len(seg.chunks) != int(cfg.FramesPerSegment()) || !seg.chunks[0].Key || seg.chunks[1].Key ||
		seg.duration != cfg.Segment {
		t.Errorf("video segment: %d chunks over %s", len(seg.chunks), seg.duration)
	}
	if aseg.duration < cfg.Segment-50*time.Millisecond || aseg.duration > cfg.Segment+50*time.Millisecond {
		t.Errorf("audio segment lasts %s", aseg.duration)
	}
}

func TestEncoderFailure(t *testing.T) {
	ffmpegPath := needFFmpeg(t)
	cfg := testConfig()
	cfg.FFmpeg = ffmpegPath
	cfg.VideoBitrate = -1 // ffmpeg refuses a negative bit rate
	var r recorder
	enc, err := NewTrackEncoder(context.Background(), cfg, []TrackSink{&r}, &r)
	if err != nil {
		t.Fatal(err)
	}
	frame := make([]byte, cfg.Width*cfg.Height*3/2)
	for range 100 {
		if err = enc.WriteFrame(0, frame); err != nil {
			break
		}
	}
	if err = enc.Close(); err == nil || !strings.Contains(err.Error(), "ffmpeg") {
		t.Errorf("ffmpeg's failure is reported: %v", err)
	}
}

func TestLadderEndToEnd(t *testing.T) {
	ffmpegPath := needFFmpeg(t)
	t.Parallel()
	cfg := testConfig()
	cfg.FFmpeg = ffmpegPath
	cfg.Renditions, _ = ParseRenditions("320x180@300k,256x144@100k")
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	wait, waitCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer waitCancel()
	segs := make([]*segment, len(s.videos))
	for i, v := range s.videos {
		segs[i] = v.segment(wait, v.startSeg, 10*time.Second)
	}
	cancel()
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	// every rung's first segment starts on a keyframe at the same frame, so a player can switch at its boundary
	var frames []uint32
	for i, seg := range segs {
		if seg == nil || len(seg.chunks) != int(cfg.FramesPerSegment()) || !seg.chunks[0].Key {
			t.Fatalf("rung %d segment %+v", i, seg)
		}
		r := s.videos[i].rendition
		mp4 := bytes.Clone(s.videos[i].info.Init)
		for _, c := range seg.chunks {
			mp4 = append(mp4, c.Data...)
		}
		raw := decodeFirstFrame(t, ffmpegPath, mp4, r)
		got, ok := DecodeTiming(raw)
		if !ok {
			t.Fatalf("rung %s timing strip", r.Name())
		}
		frames = append(frames, got.Frame)
	}
	if frames[0] != frames[1] {
		t.Errorf("rungs start at frames %v", frames)
	}
}

func decodeFirstFrame(t *testing.T, ffmpegPath string, mp4 []byte, r Rendition) *image.YCbCr {
	t.Helper()
	cmd := exec.Command(ffmpegPath, "-v", "error", "-i", "pipe:0", "-frames:v", "1", "-f", "rawvideo", "-pix_fmt",
		"yuv420p", "pipe:1")
	cmd.Stdin = bytes.NewReader(mp4)
	raw, err := cmd.Output()
	if err != nil || len(raw) != r.Width*r.Height*3/2 {
		t.Fatalf("decoding %d bytes: %v", len(raw), err)
	}
	ys, cs := r.Width*r.Height, r.Width*r.Height/4
	return &image.YCbCr{Y: raw[:ys], Cb: raw[ys : ys+cs], Cr: raw[ys+cs:], YStride: r.Width, CStride: r.Width / 2,
		SubsampleRatio: image.YCbCrSubsampleRatio420, Rect: image.Rect(0, 0, r.Width, r.Height)}
}

func startTestRelay(t *testing.T) string {
	t.Helper()
	addr, _ := startTestRelayAt(t, "127.0.0.1:0")
	return addr
}

func startTestRelayAt(t *testing.T, addr string) (string, func()) {
	t.Helper()
	// a Trickster relay that accepts publishers, on a QUIC listener with a certificate for localhost; stop
	// closes it and every session on it
	key, cert, err := testtls.GetTestKeyAndCertWithNames("localhost")
	if err != nil {
		t.Fatal(err)
	}
	kp, err := tls.X509KeyPair(cert, key)
	if err != nil {
		t.Fatal(err)
	}
	ua, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		t.Fatal(err)
	}
	uc, err := net.ListenUDP("udp", ua)
	if err != nil {
		t.Fatal(err)
	}
	tr := &quic.Transport{Conn: uc}
	ln, err := tr.Listen(&tls.Config{
		Certificates: []tls.Certificate{kp}, NextProtos: codec.ALPNs(codec.ServerPreference), MinVersion: tls.VersionTLS13,
	}, &quic.Config{EnableDatagrams: true, EnableStreamResetPartialDelivery: true})
	if err != nil {
		t.Fatal(err)
	}
	r := relay.New(relay.Options{}, nil, nil)
	r.SetAccess(relay.Access{Publish: true})
	var (
		once     sync.Once
		mu       sync.Mutex
		sessions []*session.Session
	)
	stop := func() {
		once.Do(func() {
			mu.Lock()
			for _, s := range sessions {
				_ = s.Close(message.CodeNoError, "")
			}
			mu.Unlock()
			_ = tr.Close()
			_ = uc.Close()
			r.Close()
		})
	}
	t.Cleanup(stop)
	go func() {
		for {
			conn, err := ln.Accept(context.Background())
			if err != nil {
				return
			}
			c, _ := codec.ForALPN(conn.ConnectionState().TLS.NegotiatedProtocol)
			s, _ := session.New(session.FromQUIC(conn), session.Options{
				Role: session.RoleServer, Codec: c, Handler: r.Handler(relay.Scope{Authority: "localhost", Path: "/"}),
			})
			mu.Lock()
			sessions = append(sessions, s)
			mu.Unlock()
			go func() { _ = s.Start(context.Background()) }()
		}
	}()
	return ln.Addr().String(), stop
}

func TestStreamMoQThroughRelay(t *testing.T) {
	ffmpegPath := needFFmpeg(t)
	addr := startTestRelay(t)
	cfg := testConfig()
	cfg.FFmpeg, cfg.Output, cfg.MoQInsecure = ffmpegPath, OutputMoQ, true
	cfg.MoQURL = "moqt://" + addr + "/"
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, cfg) }()
	body := relayCatalog(t, &cfg)
	if !strings.Contains(body, `"packaging":"cmaf"`) || !strings.Contains(body, `"name":"video-180p"`) {
		t.Errorf("catalog %s", body)
	}
	cancel()
	if err := <-done; err != nil {
		t.Errorf("the output ends cleanly: %v", err)
	}
}

func TestStreamMoQRedialsAfterTheRelayStops(t *testing.T) {
	ffmpegPath := needFFmpeg(t)
	addr, stop := startTestRelayAt(t, "127.0.0.1:0")
	cfg := testConfig()
	cfg.FFmpeg, cfg.Output, cfg.MoQInsecure = ffmpegPath, OutputMoQ, true
	cfg.MoQURL = "moqt://" + addr + "/"
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- StreamMoQ(ctx, cfg) }()
	relayCatalog(t, &cfg)
	// the session ends with the relay, and the stream is published again once a relay is back at the address
	stop()
	startTestRelayAt(t, addr)
	relayCatalog(t, &cfg)
	cancel()
	if err := <-done; err != nil {
		t.Errorf("the output ends cleanly: %v", err)
	}
}

func relayCatalog(t *testing.T, cfg *Config) string {
	t.Helper()
	// a viewer of the relay reads the catalog the generator published, once the namespace is announced
	viewer, closeViewer, err := dialMoQ(context.Background(), cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer closeViewer()
	track := message.FullTrackName{Namespace: moqNamespace(cfg.Broadcast()), Name: []byte(msfCatalogTrack)}
	var cat *relaySide
	deadline := time.Now().Add(10 * time.Second)
	for cat == nil || cat.groups() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the catalog never arrived through the relay")
		}
		rs := newRelaySide()
		req, err := viewer.Open(context.Background(), &message.Subscribe{Track: track}, rs, rs)
		if err != nil {
			t.Fatal(err)
		}
		rctx, rcancel := context.WithTimeout(context.Background(), time.Second)
		resp, err := req.Response(rctx)
		rcancel()
		if err == nil && resp.Kind() == message.KindSubscribeOK {
			cat = rs
			until(t, "the catalog object", func() bool { return rs.groups() == 1 })
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	cat.mu.Lock()
	defer cat.mu.Unlock()
	var body string
	for p := range cat.payload {
		body = p
	}
	return body
}
