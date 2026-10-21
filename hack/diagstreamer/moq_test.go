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
	"encoding/hex"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/moq/codec"
	"github.com/trickstercache/trickster/v2/pkg/moq/message"
	"github.com/trickstercache/trickster/v2/pkg/moq/session"
	"github.com/trickstercache/trickster/v2/pkg/moq/session/memconn"
)

const testWait = 5 * time.Second

var testASC = []byte{0x11, 0x90}

func esdsBox(flags byte) []byte {
	// an ES descriptor with optional fields per flags, a decoder config, and the AudioSpecificConfig
	dsi := append([]byte{tagDecoderSpecific, byte(len(testASC))}, testASC...)
	dc := append(append([]byte{tagDecoderConfig, byte(decoderConfigBytes + len(dsi))}, make([]byte, decoderConfigBytes)...),
		dsi...)
	es := []byte{0, 1, flags}
	if flags&esFlagDependsOn != 0 {
		es = append(es, 0, 2)
	}
	if flags&esFlagURL != 0 {
		es = append(es, 3, 'u', 'r', 'l')
	}
	if flags&esFlagOCR != 0 {
		es = append(es, 0, 3)
	}
	es = append(es, dc...)
	// the size is written in the four-byte form some muxers use
	body := append([]byte{tagESDescriptor, 0x80, 0x80, 0x80, byte(len(es))}, es...)
	return fullbox(boxEsds, 0, 0, body)
}

func aacInit(flags byte) []byte {
	mdhd := fullbox(boxMdhd, 0, 0, u32(0), u32(0), u32(SampleRate), u32(0), u16(languageUnd), u16(0))
	return append(mkbox(boxFtyp, []byte("iso6")), testMoov(mdhd, mkbox(codecMP4A, zeros(audioSampleFields), esdsBox(flags)))...)
}

func avcInit() []byte {
	return append(mkbox(boxFtyp, []byte("iso6")), avcMoov()...)
}

// moqStreamer is a test stream whose tracks carry real initialization segments, as the catalogs read them
func moqStreamer(t *testing.T, cfg Config, segments int) *Streamer {
	t.Helper()
	s := testStreamer(t, cfg, time.Minute, 0)
	for _, v := range s.videos {
		v.info.Init = avcInit()
	}
	if s.audio != nil {
		s.audio.info.Init = aacInit(0)
	}
	feedSegments(s, 0, segments)
	return s
}

func TestInitConfigs(t *testing.T) {
	avcC, err := avcConfig(avcInit())
	if err != nil || !bytes.Equal(avcC, []byte{1, 0x64, 0x00, 0x1f, 0xff}) {
		t.Errorf("avcC %x %v", avcC, err)
	}
	for _, flags := range []byte{0, esFlagDependsOn | esFlagURL | esFlagOCR} {
		if asc, err := audioSpecificConfig(aacInit(flags)); err != nil || !bytes.Equal(asc, testASC) {
			t.Errorf("flags %#x: ASC %x %v", flags, asc, err)
		}
	}
	if _, err = avcConfig(aacInit(0)); err == nil {
		t.Error("an AAC init has no avcC")
	}
	if _, err = audioSpecificConfig(avcInit()); err == nil {
		t.Error("an H.264 init has no ASC")
	}
	if _, err = avcConfig(append(mkbox(boxFtyp), testMoov(fullbox(boxMdhd, 0, 0, zeros(20)), mkbox(codecAVC1, zeros(78)))...)); err == nil {
		t.Error("an avc1 entry without avcC is refused")
	}
	noESDS := append(mkbox(boxFtyp), testMoov(fullbox(boxMdhd, 0, 0, zeros(20)), mkbox(codecMP4A, zeros(audioSampleFields)))...)
	if _, err = audioSpecificConfig(noESDS); err == nil {
		t.Error("an mp4a entry without esds is refused")
	}
	if sampleEntry([]byte("nothing")) != nil {
		t.Error("no moov, no sample entry")
	}
	for _, b := range [][]byte{{0x03}, {0x03, 0x80, 0x80, 0x80, 0x80}, {0x03, 0x80}, {0x03, 9, 1}} {
		if _, _, _, ok := descriptor(b); ok {
			t.Errorf("descriptor % x is refused", b)
		}
	}
}

func TestCatalogs(t *testing.T) {
	cfg := testConfig()
	cfg.Renditions, _ = ParseRenditions("320x180@300k,256x144@150k")
	s := moqStreamer(t, cfg, 1)
	raw, err := msfCatalog(s)
	if err != nil {
		t.Fatal(err)
	}
	var msf msfCatalogDoc
	if err = json.Unmarshal(raw, &msf); err != nil {
		t.Fatal(err)
	}
	if msf.Version != msfVersion || len(msf.Tracks) != 3 || len(msf.InitDataList) != 3 {
		t.Fatalf("msf %s", raw)
	}
	v, a := msf.Tracks[1], msf.Tracks[2]
	if v.Name != "video-144p" || v.Role != roleVideo || v.Packaging != packagingCMAF || v.Width != 256 ||
		v.AltGroup != altGroup || v.Bitrate != 150_000 || v.InitRef != msf.InitDataList[1].ID ||
		!bytes.Equal(msf.InitDataList[1].Data, avcInit()) {
		t.Errorf("msf video %+v", v)
	}
	if a.Name != KindAudio || a.Role != roleAudio || a.Samplerate != SampleRate || a.ChannelConfig != "2" || a.AltGroup != 0 {
		t.Errorf("msf audio %+v", a)
	}
	raw, err = hangCatalog(s)
	if err != nil {
		t.Fatal(err)
	}
	var hang hangCatalogDoc
	if err = json.Unmarshal(raw, &hang); err != nil {
		t.Fatal(err)
	}
	hv, ok := hang.Video.Renditions["video-180p"]
	if !ok || len(hang.Video.Renditions) != 2 || hv.Description != "0164001fff" || hv.CodedHeight != 180 ||
		hv.Container.Kind != packagingCMAF || !bytes.Equal(hv.Container.Init, avcInit()) {
		t.Errorf("hang video %s", raw)
	}
	if ha := hang.Audio.Renditions[KindAudio]; ha.Description != hex.EncodeToString(testASC) || ha.NumberOfChannels != 2 {
		t.Errorf("hang audio %+v", ha)
	}
	// a track whose init cannot be read has no hang catalog
	s.videos[1].info.Init = aacInit(0)
	if _, err = hangCatalog(s); err == nil {
		t.Error("a video init without avcC is refused")
	}
	s.videos[1].info.Init = avcInit()
	s.audio.info.Init = avcInit()
	if _, err = hangCatalog(s); err == nil {
		t.Error("an audio init without an ASC is refused")
	}
}

func TestMoQNames(t *testing.T) {
	if ns := moqNamespace("/live//studio/diag.msf"); len(ns) != 3 || string(ns[2]) != "diag.msf" {
		t.Errorf("namespace %q", ns)
	}
	cfg := DefaultConfig()
	if cfg.Broadcast() != "diagstreamer.msf" {
		t.Errorf("default broadcast %s", cfg.Broadcast())
	}
	cfg.MoQCatalog = CatalogHang
	if cfg.Broadcast() != "diagstreamer.hang" {
		t.Errorf("hang broadcast %s", cfg.Broadcast())
	}
	cfg.MoQBroadcast = "studio/a"
	if cfg.Broadcast() != "studio/a" {
		t.Error("a named broadcast is kept")
	}
	styp := mkbox(boxStyp, []byte("msdh"))
	frag := append(mkbox(boxMoof, []byte("m")), mkbox(boxMdat, []byte("d"))...)
	if !bytes.Equal(moqObject(Chunk{Data: append(styp, frag...)}), frag) || !bytes.Equal(moqObject(Chunk{Data: frag}), frag) {
		t.Error("an object is the moof and mdat, without the styp")
	}
	if junk := []byte("junk-data"); !bytes.Equal(moqObject(Chunk{Data: junk}), junk) {
		t.Error("data that is not boxes is passed on as is")
	}
	cfg = DefaultConfig()
	cfg.Output, cfg.MoQURL, cfg.MoQCatalog = OutputMoQ, "moqt://relay.example.com/", "loc"
	if cfg.Validate() == nil {
		t.Error("an unknown catalog format is refused")
	}
}

type relaySide struct {
	mu      sync.Mutex
	objects map[uint64][]message.SubgroupObject
	fetched []message.FetchObject
	payload map[string]int
	msgs    chan message.Message
	fetches chan struct{}
}

func newRelaySide() *relaySide {
	return &relaySide{objects: map[uint64][]message.SubgroupObject{}, payload: map[string]int{},
		msgs: make(chan message.Message, 16), fetches: make(chan struct{}, 4)}
}

func (r *relaySide) HandleMessage(_ *session.Request, m message.Message) { r.msgs <- m }
func (r *relaySide) HandleClose(*session.Request, error)                 {}
func (r *relaySide) HandleDatagram(*session.Request, *message.Datagram)  {}

func (r *relaySide) HandleSubgroup(_ *session.Request, in *session.IncomingSubgroup) {
	g := in.Header().Group
	for {
		o, err := in.Next()
		if err != nil {
			return
		}
		b, _ := io.ReadAll(in)
		r.mu.Lock()
		r.objects[g] = append(r.objects[g], o)
		r.payload[string(b)]++
		r.mu.Unlock()
	}
}

func (r *relaySide) HandleFetch(_ *session.Request, in *session.IncomingFetch) {
	defer func() { r.fetches <- struct{}{} }()
	for {
		fo, err := in.Next()
		if err != nil {
			return
		}
		_, _ = io.ReadAll(in)
		r.mu.Lock()
		r.fetched = append(r.fetched, fo)
		r.mu.Unlock()
	}
}

func (r *relaySide) groups() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.objects)
}

// moqPair connects a publisher, as a client, to a session the test drives as the relay
func moqPair(t *testing.T, p *moqPublisher) *session.Session {
	t.Helper()
	c, _ := codec.ForDraft(22)
	cc, sc := memconn.Pair()
	client, err := session.New(cc, session.Options{Role: session.RoleClient, Codec: c, Handler: p})
	if err != nil {
		t.Fatal(err)
	}
	server, err := session.New(sc, session.Options{Role: session.RoleServer, Codec: c})
	if err != nil {
		t.Fatal(err)
	}
	errs := make(chan error, 2)
	go func() { errs <- client.Start(context.Background()) }()
	go func() { errs <- server.Start(context.Background()) }()
	for range 2 {
		select {
		case err := <-errs:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(testWait):
			t.Fatal("the sessions did not start")
		}
	}
	t.Cleanup(func() {
		_ = client.Close(message.CodeNoError, "")
		_ = server.Close(message.CodeNoError, "")
	})
	return server
}

func until(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(testWait)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func open(t *testing.T, relay *session.Session, m message.Message, rs *relaySide) (*session.Request, message.Message) {
	t.Helper()
	req, err := relay.Open(context.Background(), m, rs, rs)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), testWait)
	defer cancel()
	resp, err := req.Response(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return req, resp
}

func TestMoQPublisher(t *testing.T) {
	s := moqStreamer(t, testConfig(), 2)
	ns := moqNamespace("diag.msf")
	p, err := newMoQPublisher(s, ns)
	if err != nil {
		t.Fatal(err)
	}
	relay := moqPair(t, p)
	name := func(track string) message.FullTrackName {
		return message.FullTrackName{Namespace: ns, Name: []byte(track)}
	}

	// the catalog is sent to each subscriber at once
	cat := newRelaySide()
	if _, resp := open(t, relay, &message.Subscribe{Track: name(msfCatalogTrack)}, cat); resp.Kind() != message.KindSubscribeOK {
		t.Fatalf("catalog subscription answered %v", resp)
	}
	until(t, "the catalog", func() bool { return cat.groups() == 1 })

	// media streams from the segment in progress, after the Largest Object the answer reports
	media := newRelaySide()
	req, resp := open(t, relay, &message.Subscribe{Track: name("video-180p")}, media)
	ok, isOK := resp.(*message.SubscribeOK)
	if !isOK || ok.Params.LargestObject.Group != uint64(s.video.startSeg+1) || ok.Params.LargestObject.Object != 9 {
		t.Fatalf("media subscription answered %+v", resp)
	}
	per := int(s.cfg.FramesPerSegment())
	feedFrames(s, 2*per+1, 4*per)
	until(t, "two streamed groups", func() bool { return media.groups() >= 2 })
	media.mu.Lock()
	first := media.objects[uint64(s.video.startSeg+2)]
	media.mu.Unlock()
	if len(first) != 10 || first[0].ID != 0 || first[9].ID != 9 {
		t.Errorf("a group is a segment, an object per chunk: %+v", first)
	}

	// FETCH reads the segments held
	f := newRelaySide()
	var p0 message.Params
	p0.SetLocationFilter(message.LocationFilter{Type: message.FilterAbsoluteRange, StartGroup: uint64(s.video.startSeg),
		EndGroupDelta: 1})
	if _, resp = open(t, relay, &message.Fetch{Track: name("video-180p"), Params: p0}, f); resp.Kind() != message.KindFetchOK {
		t.Fatalf("FETCH answered %+v", resp)
	}
	select {
	case <-f.fetches:
	case <-time.After(testWait):
		t.Fatal("the fetch stream did not end")
	}
	if len(f.fetched) == 0 || f.fetched[0].Group != uint64(s.video.startSeg) {
		t.Errorf("fetched %+v", f.fetched)
	}
	cf := newRelaySide()
	if _, resp = open(t, relay, &message.Fetch{Track: name(hangCatalogTrack), Params: p0}, cf); resp.Kind() != message.KindFetchOK {
		t.Errorf("catalog FETCH answered %+v", resp)
	}

	// requests for other tracks, namespaces or kinds are refused
	for _, m := range []message.Message{
		&message.Subscribe{Track: name("nothing")},
		&message.Subscribe{Track: message.FullTrackName{Namespace: moqNamespace("other"), Name: []byte(KindAudio)}},
		&message.Fetch{Track: name("nothing"), Params: p0},
		&message.TrackStatus{Track: name(KindAudio)},
	} {
		if _, resp := open(t, relay, m, newRelaySide()); resp.Kind() != message.KindRequestError {
			t.Errorf("%T answered %+v", m, resp)
		}
	}

	// an update is accepted, and the end of the stream ends the subscription
	if _, err = req.Update(message.Params{}); err != nil {
		t.Fatal(err)
	}
	for _, tr := range s.tracks() {
		tr.finish()
	}
	deadline := time.After(testWait)
	for {
		select {
		case m := <-media.msgs:
			if d, isDone := m.(*message.PublishDone); isDone {
				if d.Code != message.DoneTrackEnded || d.StreamCount < 2 {
					t.Errorf("done %+v", d)
				}
				return
			}
		case <-deadline:
			t.Fatal("no PUBLISH_DONE at the end of the stream")
		}
	}
}

func TestStreamMoQRefusals(t *testing.T) {
	cfg := testConfig()
	cfg.Output = OutputMoQ
	if err := StreamMoQ(context.Background(), cfg); err == nil {
		t.Error("the moq output needs a relay")
	}
	cfg.MoQURL = "ftp://relay.example.com/"
	cfg.FFmpeg = "/nonexistent/ffmpeg"
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := StreamMoQ(ctx, cfg); err == nil || !strings.Contains(err.Error(), "starting") {
		t.Errorf("an encoder that cannot start ends the output: %v", err)
	}
	c := testConfig()
	c.MoQURL = "ftp://relay.example.com/"
	if _, _, err := dialMoQ(context.Background(), &c, nil); err == nil {
		t.Error("an unknown scheme is refused")
	}
	c.MoQURL, c.MoQCA = "moqt://127.0.0.1:1/", "/nonexistent/ca.pem"
	if _, _, err := dialMoQ(context.Background(), &c, nil); err == nil {
		t.Error("a missing CA is refused")
	}
}
