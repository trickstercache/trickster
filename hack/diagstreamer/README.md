# diagstreamer

Generates a live diagnostic test stream with no media files. Go renders every frame and every
audio sample, FFmpeg encodes them, and Go packages the result as live HLS, live DASH, or one
muxed fragmented MP4 stream. It gives Trickster a real live origin for HLS/DASH caching tests,
and a real media feed for MoQ.

The picture carries:

- the UTC wall clock to the millisecond, the date, frame and segment numbers, and a timecode
- a title, the stream's size, frame rate and bit rates, and any `key=value` fields you pass
- the Trickster logo bouncing off the edges, and a background color that changes on segment
  boundaries
- a flash box during the first tenth of each second, in step with the audio's beep (1 kHz each
  second, 2 kHz each minute), for A/V sync checks
- a caption line, the same text the HLS WebVTT and DASH `wvtt` caption tracks carry
- a timing strip along the bottom: the frame number and Unix milliseconds as wide black and
  white cells with a CRC-8, which survive compression; `diagstreamer.DecodeTiming` reads it
  back from a decoded frame, so a test can measure end-to-end latency

The timeline is anchored to the Unix epoch. Frame and segment numbers count from it, encoding
starts on a segment boundary, and DASH availability and HLS program date times line up with
the clock in the picture.

## Running it

It needs Go and `ffmpeg` with `libx264` on the `PATH`:

```bash
cd hack/diagstreamer
go run ./cmd/diagstreamer
```

Open <http://127.0.0.1:8496/> for a page that plays the HLS and DASH streams side by side, or
point a player or Trickster at:

- `/hls/master.m3u8`: a multivariant playlist with video, audio and subtitle renditions
- `/dash/manifest.mpd`: a dynamic MPD with video, audio and `wvtt` text adaptation sets
- `/time`: the clock DASH clients synchronize with (`UTCTiming`)

Segments are served with `Cache-Control: public, max-age=86400, immutable` and manifests with
a `max-age` of half a segment, as a live origin behind a CDN would serve them. Media URLs carry
a stream ID (`/media/s<first segment>/...`), so a restarted generator never collides with
segments a cache still holds.

`-snapshot frame.png` writes the current frame and exits, which is handy for checking a layout.

### Docker

The image is `golang:1.27-alpine` with `ffmpeg` added. It runs the tool from the repository,
mounted read-only, so nothing is built into it:

```bash
docker build -t trickster-diagstreamer hack/diagstreamer
docker run --rm -p 8496:8496 -v "$PWD":/repo:ro trickster-diagstreamer
docker run --rm -p 8496:8496 -v "$PWD":/repo:ro -e DIAG_TITLE="Edge 3" -e DIAG_FIELDS="site=lab" trickster-diagstreamer
```

The developer environment's `moq` profile runs it as the `diagstreamer` service on
`127.0.0.1:8496` (`make developer-start-moq`).

### MoQ

The `moq` output is not built yet. For now, write fragmented MP4 to stdout and publish it with
moq-dev's CLI, into a relay or a Trickster MoQ backend that accepts publishers:

```bash
go run ./cmd/diagstreamer -out fmp4 | moq --connect moqt://localhost:8490/ --broadcast demo import fmp4
```

## Configuration

Each flag has an environment variable; a flag wins over its variable.

| Flag | Variable | Default | |
|---|---|---|---|
| `-width`, `-height` | `DIAG_WIDTH`, `DIAG_HEIGHT` | `1280`, `720` | even, 128 to 7680 |
| `-fps` | `DIAG_FPS` | `30` | whole frames per second, up to 120 |
| `-segment` | `DIAG_SEGMENT` | `2s` | segment and GOP length, a whole number of frames |
| `-chunk` | `DIAG_CHUNK` | `0` (one frame) | CMAF chunk length; must divide the segment |
| `-video-bitrate` | `DIAG_VIDEO_BITRATE` | `3M` | `k` and `M` suffixes |
| `-audio` | `DIAG_AUDIO` | `true` | the beeping tone track |
| `-audio-bitrate` | `DIAG_AUDIO_BITRATE` | `128k` | |
| `-title` | `DIAG_TITLE` | `Trickster Diagnostic Stream` | |
| `-field k=v` (repeatable) | `DIAG_FIELDS` (`k=v,k=v`) | | command-line fields replace the variable's |
| `-captions` | `DIAG_CAPTIONS` | `true` | needs whole-second segments |
| `-color-period` | `DIAG_COLOR_PERIOD` | `10s` | rounded up to whole segments; `0` keeps one color |
| `-logo-speed` | `DIAG_LOGO_SPEED` | `160` | pixels per second at 720 lines |
| `-window` | `DIAG_WINDOW` | `10` | segments in a live manifest, at least 3 |
| `-listen` | `DIAG_LISTEN` | `:8496` | |
| `-out` | `DIAG_OUT` | `http` | `http`, `fmp4` (to stdout), or `moq` |
| `-moq-url` | `DIAG_MOQ_URL` | | the relay for the `moq` output |
| `-ffmpeg` | `DIAG_FFMPEG` | `ffmpeg` | |

## As a package

```go
cfg := diagstreamer.DefaultConfig()
cfg.Fields = []diagstreamer.Field{{Key: "site", Value: "lab"}}

h, err := diagstreamer.StreamHLS(ctx, cfg)          // or StreamDASH: one handler serves both
err = diagstreamer.StreamEncoder(ctx, cfg, w)       // one muxed fragmented MP4 to w
r, err := diagstreamer.NewRenderer(cfg)             // frames without encoding
t, ok := diagstreamer.DecodeTiming(decodedFrame)    // read a frame's timing strip
```

FFmpeg sits behind the `Encoder` interface, so another encoder can replace it.

## Tests

```bash
cd hack/diagstreamer && go test ./...
```

Tests that encode skip when `ffmpeg` is not installed. One of them decodes an encoded frame
and checks its timing strip.
