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
	"fmt"
	"math"
	"strings"
	"time"
)

const (
	hlsVersion     = 7
	pdtLayout      = "2006-01-02T15:04:05.000Z07:00"
	audioGroup     = "aud"
	subtitlesGroup = "subs"
)

func (s *Streamer) multivariantPlaylist() string {
	// the HLS entry point: the video variant with its audio and subtitle renditions
	var b strings.Builder
	fmt.Fprintf(&b, "#EXTM3U\n#EXT-X-VERSION:%d\n#EXT-X-INDEPENDENT-SEGMENTS\n", hlsVersion)
	attrs := ""
	if s.audio != nil {
		fmt.Fprintf(&b, "#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID=%q,NAME=\"Tone\",LANGUAGE=\"en\",DEFAULT=YES,"+
			"AUTOSELECT=YES,CHANNELS=\"%d\",URI=\"audio.m3u8\"\n", audioGroup, audioChannels)
		attrs += fmt.Sprintf(",AUDIO=%q", audioGroup)
	}
	if s.cfg.Captions {
		fmt.Fprintf(&b, "#EXT-X-MEDIA:TYPE=SUBTITLES,GROUP-ID=%q,NAME=\"Clock\",LANGUAGE=\"en\",DEFAULT=YES,"+
			"AUTOSELECT=YES,FORCED=NO,URI=\"subtitles.m3u8\"\n", subtitlesGroup)
		attrs += fmt.Sprintf(",SUBTITLES=%q", subtitlesGroup)
	}
	for _, t := range s.videos {
		r := t.rendition
		fmt.Fprintf(&b, "#EXT-X-STREAM-INF:BANDWIDTH=%d,AVERAGE-BANDWIDTH=%d,CODECS=%q,RESOLUTION=%dx%d,"+
			"FRAME-RATE=%d.000%s\n", s.bandwidth(t), s.bandwidth(t), s.codecs(t), r.Width, r.Height, s.cfg.FPS, attrs)
		b.WriteString(videoPlaylist(r) + "\n")
	}
	return b.String()
}

func videoPlaylist(r Rendition) string {
	return KindVideo + "-" + r.Name() + playlistExt
}

func trackPath(t *track) string {
	// where a track's media sits below the stream: a video rendition has a directory of its own
	if t.kind == KindVideo {
		return t.kind + "/" + t.rendition.Name()
	}
	return t.kind
}

func (s *Streamer) targetDuration() int {
	// the HLS target duration: every segment's duration, rounded, is at most this
	return max(int(math.Ceil(s.cfg.Segment.Seconds())), 1)
}

func (s *Streamer) mediaPlaylist(t *track) string {
	// lists a track's newest complete segments
	segs := t.window(s.cfg.Window)
	var b strings.Builder
	fmt.Fprintf(&b, "#EXTM3U\n#EXT-X-VERSION:%d\n#EXT-X-TARGETDURATION:%d\n", hlsVersion, s.targetDuration())
	first := t.startSeg
	if len(segs) > 0 {
		first = segs[0].number
	}
	fmt.Fprintf(&b, "#EXT-X-MEDIA-SEQUENCE:%d\n#EXT-X-MAP:URI=\"%s\"\n", first, s.mediaPath(trackPath(t), initName))
	for _, seg := range segs {
		fmt.Fprintf(&b, "#EXT-X-PROGRAM-DATE-TIME:%s\n#EXTINF:%s,\n%s\n", seg.start.UTC().Format(pdtLayout),
			seconds(seg.duration), s.mediaPath(trackPath(t), segmentName(seg.number)))
	}
	return b.String()
}

func (s *Streamer) mediaPath(kind, name string) string {
	// a segment's URL relative to the manifests, which sit one directory below the media
	return "../" + mediaDir + "/" + s.streamID + "/" + kind + "/" + name
}

func segmentName(n int64) string {
	return fmt.Sprintf("%d%s", n, segmentExt)
}

func (s *Streamer) mpd(now time.Time) string {
	// the dynamic DASH manifest. The period starts with the stream, so segment number N begins N segments
	// after the Unix epoch and each track's decode time counts from the period start.
	segDur := s.cfg.Segment
	periodStart := s.p.startTime().Sub(time.Unix(0, 0))
	depth := time.Duration(s.cfg.Window) * segDur
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	fmt.Fprintf(&b, `<MPD xmlns="urn:mpeg:dash:schema:mpd:2011" profiles="urn:mpeg:dash:profile:isoff-live:2011" `+
		`type="dynamic" availabilityStartTime="1970-01-01T00:00:00Z" publishTime="%s" minimumUpdatePeriod="%s" `+
		`minBufferTime="%s" timeShiftBufferDepth="%s" suggestedPresentationDelay="%s" maxSegmentDuration="%s">`+"\n",
		now.UTC().Format(time.RFC3339), isoDuration(segDur), isoDuration(segDur), isoDuration(depth),
		isoDuration(3*segDur), isoDuration(segDur))
	fmt.Fprintf(&b, `  <Period id="%s" start="%s">`+"\n", s.streamID, isoDuration(periodStart))
	// the renditions share one adaptation set, aligned on segment boundaries, so a player switches between them
	top := s.video
	vts := uint64(top.info.Timescale)
	fmt.Fprintf(&b, `    <AdaptationSet id="%d" contentType="video" mimeType="video/mp4" segmentAlignment="true" `+
		`startWithSAP="1" maxWidth="%d" maxHeight="%d" frameRate="%d" par="%d:%d">`+"\n", adaptationID(KindVideo),
		top.rendition.Width, top.rendition.Height, s.cfg.FPS, top.rendition.Width, top.rendition.Height)
	fmt.Fprintf(&b, `      <SegmentTemplate timescale="%d" duration="%d" startNumber="%d" initialization="%s" `+
		`media="%s"/>`+"\n", vts, uint64(segDur.Seconds()*float64(vts)), top.startSeg,
		s.mediaPath(KindVideo+"/$RepresentationID$", initName),
		s.mediaPath(KindVideo+"/$RepresentationID$", "$Number$"+segmentExt))
	for _, t := range s.videos {
		r := t.rendition
		fmt.Fprintf(&b, `      <Representation id="%s" codecs="%s" bandwidth="%d" width="%d" height="%d" sar="1:1"/>`+
			"\n", r.Name(), t.info.Codec, r.Bitrate, r.Width, r.Height)
	}
	b.WriteString("    </AdaptationSet>\n")
	if a := s.audio; a != nil {
		ats := uint64(a.info.Timescale)
		fmt.Fprintf(&b, `    <AdaptationSet id="%d" contentType="audio" mimeType="audio/mp4" segmentAlignment="true" `+
			`startWithSAP="1">`+"\n", adaptationID(KindAudio))
		fmt.Fprintf(&b, `      <SegmentTemplate timescale="%d" duration="%d" startNumber="%d" initialization="%s" `+
			`media="%s"/>`+"\n", ats, uint64(segDur.Seconds()*float64(ats)), a.startSeg, s.mediaPath(KindAudio, initName),
			s.mediaPath(KindAudio, "$Number$"+segmentExt))
		fmt.Fprintf(&b, `      <Representation id="audio" codecs="%s" bandwidth="%d" audioSamplingRate="%d">`+
			"\n"+`        <AudioChannelConfiguration schemeIdUri="urn:mpeg:dash:23003:3:audio_channel_configuration:2011" `+
			`value="%d"/>`+"\n      </Representation>\n    </AdaptationSet>\n", a.info.Codec, s.cfg.AudioBitrate,
			SampleRate, audioChannels)
	}
	if s.cfg.Captions {
		fmt.Fprintf(&b, `    <AdaptationSet id="%d" contentType="text" mimeType="application/mp4" lang="en">`+"\n"+
			`      <Role schemeIdUri="urn:mpeg:dash:role:2011" value="subtitle"/>`+"\n"+
			`      <SegmentTemplate timescale="%d" duration="%d" startNumber="%d" initialization="%s" media="%s"/>`+"\n"+
			`      <Representation id="text" codecs="wvtt" bandwidth="%d"/>`+"\n    </AdaptationSet>\n",
			adaptationID(KindText), textTimescale, segDur.Milliseconds(), s.video.startSeg,
			s.mediaPath(KindText, initName), s.mediaPath(KindText, "$Number$"+segmentExt), textBandwidth)
	}
	b.WriteString("  </Period>\n")
	b.WriteString(`  <UTCTiming schemeIdUri="urn:mpeg:dash:utc:http-iso:2014" value="../time"/>` + "\n</MPD>\n")
	return b.String()
}

func adaptationID(kind string) int {
	switch kind {
	case KindVideo:
		return 1
	case KindAudio:
		return 2
	}
	return 3
}
