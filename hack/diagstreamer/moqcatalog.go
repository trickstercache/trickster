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
	"encoding/hex"
	"encoding/json"
	"strconv"
)

// The catalogs describe the same CMAF tracks: each object is one moof and its mdat, each group one segment, and
// each track's initialization segment rides in the catalog (the MOQT Streaming Format, and moq-dev's hang)
const (
	msfCatalogTrack  = "catalog"
	hangCatalogTrack = "catalog.json"
	msfVersion       = "draft-01"
	packagingCMAF    = "cmaf"
	roleVideo        = "video"
	roleAudio        = "audio"
	initInline       = "inline"
	renderGroup      = 1
	altGroup         = 1
	videoSAPType     = 2
	audioSAPType     = 1
)

type msfCatalogDoc struct {
	Version      string     `json:"version"`
	Tracks       []msfTrack `json:"tracks"`
	InitDataList []msfInit  `json:"initDataList"`
}

type msfTrack struct {
	Name          string  `json:"name"`
	Packaging     string  `json:"packaging"`
	IsLive        bool    `json:"isLive"`
	Role          string  `json:"role"`
	Codec         string  `json:"codec"`
	Width         int     `json:"width,omitempty"`
	Height        int     `json:"height,omitempty"`
	Framerate     float64 `json:"framerate,omitempty"`
	Samplerate    int     `json:"samplerate,omitempty"`
	ChannelConfig string  `json:"channelConfig,omitempty"`
	Bitrate       int     `json:"bitrate,omitempty"`
	InitRef       string  `json:"initRef"`
	RenderGroup   int     `json:"renderGroup"`
	AltGroup      int     `json:"altGroup,omitempty"`
	MaxGrpSAP     int     `json:"maxGrpSapStartingType"`
	MaxObjSAP     int     `json:"maxObjSapStartingType"`
}

type msfInit struct {
	ID   string `json:"id"`
	Type string `json:"type"`
	Data []byte `json:"data"`
}

func msfCatalog(s *Streamer) ([]byte, error) {
	// the renditions share a render group and, when there are several, an alternate group a player picks one of
	doc := msfCatalogDoc{Version: msfVersion}
	for i, t := range s.tracks() {
		id := "init" + strconv.Itoa(i)
		tr := msfTrack{
			Name: moqTrackName(t), Packaging: packagingCMAF, IsLive: true, Codec: t.info.Codec, InitRef: id,
			RenderGroup: renderGroup,
		}
		if t.kind == KindVideo {
			tr.Role, tr.Width, tr.Height = roleVideo, t.rendition.Width, t.rendition.Height
			tr.Framerate, tr.Bitrate = float64(s.cfg.FPS), t.rendition.Bitrate
			tr.MaxGrpSAP, tr.MaxObjSAP = videoSAPType, videoSAPType
			if len(s.videos) > 1 {
				tr.AltGroup = altGroup
			}
		} else {
			tr.Role, tr.Samplerate, tr.ChannelConfig = roleAudio, SampleRate, strconv.Itoa(audioChannels)
			tr.Bitrate, tr.MaxGrpSAP, tr.MaxObjSAP = s.cfg.AudioBitrate, audioSAPType, audioSAPType
		}
		doc.Tracks = append(doc.Tracks, tr)
		doc.InitDataList = append(doc.InitDataList, msfInit{ID: id, Type: initInline, Data: t.info.Init})
	}
	return json.Marshal(doc)
}

type hangCatalogDoc struct {
	Video *hangSection[hangVideo] `json:"video,omitempty"`
	Audio *hangSection[hangAudio] `json:"audio,omitempty"`
}

type hangSection[T any] struct {
	Renditions map[string]T `json:"renditions"`
}

type hangContainer struct {
	Kind string `json:"kind"`
	Init []byte `json:"init"`
}

type hangVideo struct {
	Codec       string        `json:"codec"`
	Description string        `json:"description"`
	CodedWidth  int           `json:"codedWidth"`
	CodedHeight int           `json:"codedHeight"`
	Bitrate     int           `json:"bitrate"`
	Framerate   float64       `json:"framerate"`
	Container   hangContainer `json:"container"`
}

type hangAudio struct {
	Codec            string        `json:"codec"`
	SampleRate       int           `json:"sampleRate"`
	NumberOfChannels int           `json:"numberOfChannels"`
	Bitrate          int           `json:"bitrate"`
	Description      string        `json:"description"`
	Container        hangContainer `json:"container"`
}

func hangCatalog(s *Streamer) ([]byte, error) {
	// each rendition is named by its track, and its description is the hex of its decoder configuration
	doc := hangCatalogDoc{Video: &hangSection[hangVideo]{Renditions: map[string]hangVideo{}}}
	for _, t := range s.videos {
		avcC, err := avcConfig(t.info.Init)
		if err != nil {
			return nil, err
		}
		doc.Video.Renditions[moqTrackName(t)] = hangVideo{
			Codec: t.info.Codec, Description: hex.EncodeToString(avcC), CodedWidth: t.rendition.Width,
			CodedHeight: t.rendition.Height, Bitrate: t.rendition.Bitrate, Framerate: float64(s.cfg.FPS),
			Container: hangContainer{Kind: packagingCMAF, Init: t.info.Init},
		}
	}
	if a := s.audio; a != nil {
		asc, err := audioSpecificConfig(a.info.Init)
		if err != nil {
			return nil, err
		}
		doc.Audio = &hangSection[hangAudio]{Renditions: map[string]hangAudio{moqTrackName(a): {
			Codec: a.info.Codec, SampleRate: SampleRate, NumberOfChannels: audioChannels, Bitrate: s.cfg.AudioBitrate,
			Description: hex.EncodeToString(asc), Container: hangContainer{Kind: packagingCMAF, Init: a.info.Init},
		}}}
	}
	return json.Marshal(doc)
}
