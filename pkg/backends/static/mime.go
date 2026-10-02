/*
 * Copyright 2018 The Trickster Authors
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

package static

import (
	"mime"
	"net/http"
	"path"
	"strings"
)

const (
	contentTypeOctetStream = "application/octet-stream"
	contentTypeHTML        = "text/html; charset=utf-8"

	sniffLen = 512
)

// builtinTypes covers the common web content types, so resolution does not
// depend on the MIME databases installed on the host or container image.
var builtinTypes = map[string]string{
	".html":        contentTypeHTML,
	".htm":         contentTypeHTML,
	".xhtml":       "application/xhtml+xml",
	".css":         "text/css; charset=utf-8",
	".js":          "text/javascript; charset=utf-8",
	".mjs":         "text/javascript; charset=utf-8",
	".cjs":         "text/javascript; charset=utf-8",
	".json":        "application/json",
	".jsonld":      "application/ld+json",
	".map":         "application/json",
	".webmanifest": "application/manifest+json",
	".xml":         "application/xml",
	".rss":         "application/rss+xml",
	".atom":        "application/atom+xml",
	".txt":         "text/plain; charset=utf-8",
	".text":        "text/plain; charset=utf-8",
	".log":         "text/plain; charset=utf-8",
	".md":          "text/markdown; charset=utf-8",
	".csv":         "text/csv; charset=utf-8",
	".tsv":         "text/tab-separated-values; charset=utf-8",
	".yaml":        "application/yaml",
	".yml":         "application/yaml",
	".toml":        "application/toml",
	".ics":         "text/calendar; charset=utf-8",
	".vtt":         "text/vtt; charset=utf-8",
	".wasm":        "application/wasm",
	".pdf":         "application/pdf",
	".rtf":         "application/rtf",

	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".gif":  "image/gif",
	".webp": "image/webp",
	".avif": "image/avif",
	".apng": "image/apng",
	".svg":  "image/svg+xml",
	".ico":  "image/x-icon",
	".cur":  "image/x-icon",
	".bmp":  "image/bmp",
	".tif":  "image/tiff",
	".tiff": "image/tiff",
	".heic": "image/heic",
	".heif": "image/heif",
	".jxl":  "image/jxl",

	".woff":  "font/woff",
	".woff2": "font/woff2",
	".ttf":   "font/ttf",
	".otf":   "font/otf",
	".eot":   "application/vnd.ms-fontobject",

	".mp3":  "audio/mpeg",
	".m4a":  "audio/mp4",
	".aac":  "audio/aac",
	".oga":  "audio/ogg",
	".ogg":  "audio/ogg",
	".opus": "audio/ogg",
	".wav":  "audio/wav",
	".weba": "audio/webm",
	".flac": "audio/flac",
	".mid":  "audio/midi",
	".midi": "audio/midi",

	".mp4":  "video/mp4",
	".m4v":  "video/mp4",
	".m4s":  "video/iso.segment",
	".webm": "video/webm",
	".ogv":  "video/ogg",
	".mov":  "video/quicktime",
	".mkv":  "video/x-matroska",
	".avi":  "video/x-msvideo",
	".mpeg": "video/mpeg",
	".mpg":  "video/mpeg",
	".ts":   "video/mp2t",
	".m3u8": "application/vnd.apple.mpegurl",
	".mpd":  "application/dash+xml",

	".zip":  "application/zip",
	".gz":   "application/gzip",
	".tgz":  "application/gzip",
	".tar":  "application/x-tar",
	".bz2":  "application/x-bzip2",
	".xz":   "application/x-xz",
	".zst":  "application/zstd",
	".7z":   "application/x-7z-compressed",
	".rar":  "application/vnd.rar",
	".jar":  "application/java-archive",
	".bin":  contentTypeOctetStream,
	".exe":  contentTypeOctetStream,
	".dmg":  contentTypeOctetStream,
	".iso":  contentTypeOctetStream,
	".deb":  "application/vnd.debian.binary-package",
	".rpm":  "application/x-rpm",
	".apk":  "application/vnd.android.package-archive",
	".epub": "application/epub+zip",

	".doc":  "application/msword",
	".docx": "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
	".xls":  "application/vnd.ms-excel",
	".xlsx": "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
	".ppt":  "application/vnd.ms-powerpoint",
	".pptx": "application/vnd.openxmlformats-officedocument.presentationml.presentation",
	".odt":  "application/vnd.oasis.opendocument.text",
	".ods":  "application/vnd.oasis.opendocument.spreadsheet",
	".odp":  "application/vnd.oasis.opendocument.presentation",
}

// typeByExtension resolves a Content-Type from configured overrides, then the
// builtin table, then the host's MIME database. It returns "" when none match.
func typeByExtension(name string, overrides map[string]string) string {
	ext := strings.ToLower(path.Ext(name))
	if ext == "" {
		return ""
	}
	if ct, ok := overrides[ext]; ok {
		return ct
	}
	if ct, ok := builtinTypes[ext]; ok {
		return ct
	}
	return mime.TypeByExtension(ext)
}

// sniffType resolves a Content-Type from a file's leading bytes
func sniffType(head []byte) string {
	if len(head) == 0 {
		return contentTypeOctetStream
	}
	if len(head) > sniffLen {
		head = head[:sniffLen]
	}
	return http.DetectContentType(head)
}

// baseType returns a Content-Type without its parameters (e.g., charset)
func baseType(ct string) string {
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = ct[:i]
	}
	return strings.TrimSpace(ct)
}
