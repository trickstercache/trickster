# diagstreamer

Generates a live diagnostic test stream with no media files. Go renders every frame and every
audio sample, FFmpeg encodes them, and Go packages the result as live HLS, live DASH, or one
muxed fragmented MP4 stream. It gives Trickster a real live origin for HLS/DASH caching tests,
and a real media feed for MoQ.

The picture carries:

- the UTC wall clock to the millisecond, the date, frame and segment numbers, and a timecode
- a title, the stream's size, frame rate and bit rates, and any `key=value` fields you pass
- the Trickster vertical logo beside classic 75% color bars (SMPTE EG 1, with the reverse bars
  under them), which show a hue, saturation or level error downstream at a glance
- a background color that changes on segment boundaries, named in the panel with its RGB value and the Y, Cb and Cr codes the encoder is
  given, so a color shift downstream can be spotted and measured
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

The Makefile at the repository root runs it too; `DIAG_ARGS` passes extra flags:

| Target | What it runs |
|---|---|
| `make diagstreamer` | live HLS and DASH at <http://127.0.0.1:8496/> |
| `make diagstreamer-moq` | publishes to `DIAG_MOQ_URL` (default `moqt://localhost:8490/`, the developer Trickster's moq listener), skipping certificate checks |
| `make diagstreamer-snapshot` | writes the current frame to `DIAG_SNAPSHOT` (default `diagstreamer.png`) |
| `make diagstreamer-docker` | the HTTP stream from the Docker image, with no local ffmpeg |

Open <http://127.0.0.1:8496/> for a page that plays the HLS and DASH streams side by side, or
point a player or Trickster at:

- `/hls/master.m3u8`: a multivariant playlist with a variant per video rendition, and audio and
  subtitle renditions
- `/dash/manifest.mpd`: a dynamic MPD with a video adaptation set holding every rendition, and
  audio and `wvtt` text adaptation sets
- `/time`: the clock DASH clients synchronize with (`UTCTiming`)

Segments are served with `Cache-Control: public, max-age=86400, immutable` and manifests with
a `max-age` of half a segment, as a live origin behind a CDN would serve them. Media URLs carry
a stream ID (`/media/s<first segment>/...`), so a restarted generator never collides with
segments a cache still holds.

### Renditions

The stream is an ABR ladder: by default 1080p at 6 Mb/s, 720p at 3 Mb/s, 480p at 1.5 Mb/s and
360p at 800 kb/s. Each rendition is rendered at its own size, so its picture names its own
resolution and bit rate, and its timing strip decodes at that size. FFmpeg encodes them with one
GOP length, so their segments align and a player can switch at any boundary.

- Without `-renditions`, `-width`, `-height` and `-video-bitrate` set the top rung, and the
  default rungs smaller than it complete the ladder: `-width 1280 -height 720` gives 720p, 480p
  and 360p.
- `-renditions` gives the whole ladder, such as `-renditions 1280x720@3M,640x360@800k`. A single
  rendition is a one-rung ladder: `-renditions 1280x720@3M`.
- HLS lists `video-<rung>.m3u8` per rendition, such as `video-720p.m3u8`; `video.m3u8` is the top
  rendition. Media sits under `/media/<stream>/video/<rung>/`.
- `-out fmp4` and `-snapshot` carry the top rendition only.

The default ladder renders in about 1 ms per frame. FFmpeg encodes it with about two thirds of
one core of an Apple M3 Max. A smaller machine, or a Docker host without much CPU, can fall
behind the clock, which the generator logs; give it a shorter ladder then.

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

`-out moq` publishes the stream to a MoQ relay: Trickster's, moq-dev's `moq-relay`, or any relay
that takes `PUBLISH_NAMESPACE`. The generator announces the broadcast and answers the relay's
`SUBSCRIBE`s and `FETCH`es, dialing again if the session ends:

```bash
go run ./cmd/diagstreamer -out moq -moq-url moqt://localhost:8490/ -moq-ca ../../docs/developer/environment/certs/trickster-dev.crt
go run ./cmd/diagstreamer -out moq -moq-url moqt://127.0.0.1:10443/ -moq-insecure -moq-catalog hang
```

- Each track is CMAF: a group per segment, starting at its keyframe, and an object per chunk,
  one `moof` and its `mdat`. Video tracks are `video-<rung>` and the audio track is `audio`.
- Both catalogs are served: the MOQT Streaming Format's on `catalog` (packaging `cmaf`, each
  init segment inline), and moq-dev's hang on `catalog.json` (container `cmaf`). Readers pick
  one by the broadcast name's suffix, so the default broadcast is `diagstreamer.msf`, or
  `diagstreamer.hang` with `-moq-catalog hang`. `-moq-broadcast` names it outright.
- moq-dev's CLI reads it: `moq --connect <relay> --broadcast diagstreamer.msf export fmp4`.
  The 0.14.0 CLI cannot read an MSF catalog with CMAF packaging, even one it published itself, so
  add `--catalog-format hang` with that release.
- `-moq-url` takes `moqt://` for native QUIC and `https://` for WebTransport. `-moq-insecure`
  skips certificate checks, and `-moq-ca` trusts a PEM bundle.

`-out fmp4` piped into moq-dev's importer also works, as one muxed rendition:

```bash
go run ./cmd/diagstreamer -out fmp4 | moq --connect moqt://localhost:8490/ --broadcast demo.hang import fmp4
```

## Configuration

Each flag has an environment variable; a flag wins over its variable.

| Flag | Variable | Default | |
|---|---|---|---|
| `-renditions` | `DIAG_RENDITIONS` | | the whole ladder, largest first, as `WxH@bitrate,...` |
| `-width`, `-height` | `DIAG_WIDTH`, `DIAG_HEIGHT` | `1920`, `1080` | the top rendition; even, 128 to 7680 |
| `-fps` | `DIAG_FPS` | `30` | whole frames per second, up to 120 |
| `-segment` | `DIAG_SEGMENT` | `2s` | segment and GOP length, a whole number of frames |
| `-chunk` | `DIAG_CHUNK` | `0` (one frame) | CMAF chunk length; must divide the segment |
| `-video-bitrate` | `DIAG_VIDEO_BITRATE` | `6M` | the top rendition's; `k` and `M` suffixes |
| `-audio` | `DIAG_AUDIO` | `true` | the beeping tone track |
| `-audio-bitrate` | `DIAG_AUDIO_BITRATE` | `128k` | |
| `-title` | `DIAG_TITLE` | `Trickster Diagnostic Stream` | |
| `-field k=v` (repeatable) | `DIAG_FIELDS` (`k=v,k=v`) | | command-line fields replace the variable's |
| `-captions` | `DIAG_CAPTIONS` | `true` | needs whole-second segments |
| `-color-period` | `DIAG_COLOR_PERIOD` | `10s` | rounded up to whole segments; `0` keeps one color |
| `-window` | `DIAG_WINDOW` | `10` | segments in a live manifest, at least 3 |
| `-listen` | `DIAG_LISTEN` | `:8496` | |
| `-out` | `DIAG_OUT` | `http` | `http`, `fmp4` (to stdout), or `moq` |
| `-moq-url` | `DIAG_MOQ_URL` | | the relay for the `moq` output, `moqt://` or `https://` |
| `-moq-catalog` | `DIAG_MOQ_CATALOG` | `msf` | `msf` or `hang`: the catalog the default broadcast name advertises |
| `-moq-broadcast` | `DIAG_MOQ_BROADCAST` | `diagstreamer.<catalog>` | the broadcast name, split on `/` into its namespace |
| `-moq-insecure` | `DIAG_MOQ_INSECURE` | `false` | skip verifying the relay's certificate |
| `-moq-ca` | `DIAG_MOQ_CA` | | a PEM bundle trusted for the relay's certificate |
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
