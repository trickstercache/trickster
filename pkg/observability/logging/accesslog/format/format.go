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

package format

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// clfTimeLayout is the NCSA Common Log Format timestamp layout
const clfTimeLayout = "02/Jan/2006:15:04:05 -0700"

const dash = "-"

// Named format presets; "combined" is the default
const (
	Common   = "common"
	Combined = "combined"
	Extended = "extended"
	JSON     = "json"

	DefaultFormatName = Combined
)

var presets = map[string]string{
	Common:   `%h %l %u %t "%r" %>s %b`,
	Combined: `%h %l %u %t "%r" %>s %b "%{Referer}i" "%{User-Agent}i"`,
	Extended: `%h %l %u %t "%r" %>s %b "%{Referer}i" "%{User-Agent}i"` +
		` %{ms}T %{cache-status}x %{backend}x`,
}

type emitter func(b []byte, f *Fields) []byte

// Formatter renders a compiled access log format for a request's Fields
type Formatter struct {
	emitters []emitter
}

// Render appends the formatted log line, including a trailing newline, to b
func (fm *Formatter) Render(b []byte, f *Fields) []byte {
	for _, e := range fm.emitters {
		b = e(b, f)
	}
	return append(b, '\n')
}

// ParseFormat compiles a named preset or custom %-token format string into
// a Formatter; unknown tokens return an error
func ParseFormat(input string) (*Formatter, error) {
	if input == "" {
		input = DefaultFormatName
	}
	if input == JSON {
		return &Formatter{emitters: jsonEmitters()}, nil
	}
	if p, ok := presets[input]; ok {
		input = p
	}
	fm := &Formatter{emitters: make([]emitter, 0, 16)}
	var literal strings.Builder
	flushLiteral := func() {
		if literal.Len() > 0 {
			s := literal.String()
			literal.Reset()
			fm.emitters = append(fm.emitters, func(b []byte, _ *Fields) []byte {
				return append(b, s...)
			})
		}
	}
	for i := 0; i < len(input); {
		c := input[i]
		if c != '%' {
			literal.WriteByte(c)
			i++
			continue
		}
		i++
		if i >= len(input) {
			return nil, fmt.Errorf("%w: trailing %%", ErrInvalidFormatToken)
		}
		var arg string
		var hasArg bool
		switch input[i] {
		case '{':
			end := strings.IndexByte(input[i:], '}')
			if end < 0 {
				return nil, fmt.Errorf("%w: unterminated %%{", ErrInvalidFormatToken)
			}
			arg = input[i+1 : i+end]
			hasArg = true
			i += end + 1
			if i >= len(input) {
				return nil, fmt.Errorf("%w: %%{%s} missing token letter",
					ErrInvalidFormatToken, arg)
			}
		case '>':
			// the Apache "final status" modifier; equivalent to %s here
			i++
			if i >= len(input) || input[i] != 's' {
				return nil, fmt.Errorf("%w: %%> must be followed by s",
					ErrInvalidFormatToken)
			}
		}
		e, err := compileToken(input[i], arg, hasArg)
		if err != nil {
			return nil, err
		}
		flushLiteral()
		fm.emitters = append(fm.emitters, e)
		i++
	}
	flushLiteral()
	return fm, nil
}

func compileToken(token byte, arg string, hasArg bool) (emitter, error) {
	if hasArg {
		return compileArgToken(token, arg)
	}
	switch token {
	case '%':
		return func(b []byte, _ *Fields) []byte { return append(b, '%') }, nil
	case 'h', 'a':
		return func(b []byte, f *Fields) []byte {
			return appendOrDash(b, f.ClientIP)
		}, nil
	case 'l':
		return func(b []byte, _ *Fields) []byte { return append(b, dash...) }, nil
	case 'u':
		return func(b []byte, f *Fields) []byte {
			return appendEscapedOrDash(b, f.User)
		}, nil
	case 't':
		return func(b []byte, f *Fields) []byte {
			b = append(b, '[')
			b = f.StartTime.AppendFormat(b, clfTimeLayout)
			return append(b, ']')
		}, nil
	case 'r':
		return func(b []byte, f *Fields) []byte {
			b = appendEscaped(b, f.Method)
			b = append(b, ' ')
			b = appendEscaped(b, f.RequestURI)
			b = append(b, ' ')
			return appendEscaped(b, f.Proto)
		}, nil
	case 'm':
		return func(b []byte, f *Fields) []byte {
			return appendOrDash(b, f.Method)
		}, nil
	case 'U':
		return func(b []byte, f *Fields) []byte {
			return appendEscapedOrDash(b, f.Path)
		}, nil
	case 'q':
		return func(b []byte, f *Fields) []byte {
			if f.Query == "" {
				return b
			}
			b = append(b, '?')
			return appendEscaped(b, f.Query)
		}, nil
	case 'H':
		return func(b []byte, f *Fields) []byte {
			return appendOrDash(b, f.Proto)
		}, nil
	case 's':
		return func(b []byte, f *Fields) []byte {
			return strconv.AppendInt(b, int64(f.Status), 10)
		}, nil
	case 'b':
		return func(b []byte, f *Fields) []byte {
			if f.BytesWritten == 0 {
				return append(b, dash...)
			}
			return strconv.AppendInt(b, f.BytesWritten, 10)
		}, nil
	case 'B':
		return func(b []byte, f *Fields) []byte {
			return strconv.AppendInt(b, f.BytesWritten, 10)
		}, nil
	case 'D':
		return func(b []byte, f *Fields) []byte {
			return strconv.AppendInt(b, f.Duration.Microseconds(), 10)
		}, nil
	case 'T':
		return func(b []byte, f *Fields) []byte {
			return strconv.AppendInt(b, int64(f.Duration/time.Second), 10)
		}, nil
	case 'v':
		return func(b []byte, f *Fields) []byte {
			return appendEscapedOrDash(b, f.Host)
		}, nil
	case 'p':
		return func(b []byte, f *Fields) []byte {
			return appendOrDash(b, f.LocalPort)
		}, nil
	case 'A':
		return func(b []byte, f *Fields) []byte {
			return appendOrDash(b, f.LocalIP)
		}, nil
	}
	return nil, fmt.Errorf("%w: %%%s", ErrInvalidFormatToken, string(token))
}

func compileArgToken(token byte, arg string) (emitter, error) {
	switch token {
	case 'i':
		key := http.CanonicalHeaderKey(arg)
		return func(b []byte, f *Fields) []byte {
			return appendHeader(b, f.ReqHeader, key)
		}, nil
	case 'o':
		key := http.CanonicalHeaderKey(arg)
		return func(b []byte, f *Fields) []byte {
			return appendHeader(b, f.RespHeader, key)
		}, nil
	case 'c':
		return func(b []byte, f *Fields) []byte {
			return appendCookie(b, f.ReqHeader, arg)
		}, nil
	case 't':
		return compileTimeToken(arg)
	case 'T':
		switch arg {
		case "us":
			return func(b []byte, f *Fields) []byte {
				return strconv.AppendInt(b, f.Duration.Microseconds(), 10)
			}, nil
		case "ms":
			return func(b []byte, f *Fields) []byte {
				return strconv.AppendInt(b, f.Duration.Milliseconds(), 10)
			}, nil
		case "s":
			return func(b []byte, f *Fields) []byte {
				return strconv.AppendInt(b, int64(f.Duration/time.Second), 10)
			}, nil
		}
		return nil, fmt.Errorf("%w: %%{%s}T", ErrInvalidFormatToken, arg)
	case 'x':
		return compileExtensionToken(arg)
	}
	return nil, fmt.Errorf("%w: %%{%s}%s", ErrInvalidFormatToken, arg, string(token))
}

func compileTimeToken(arg string) (emitter, error) {
	switch arg {
	case "sec":
		return func(b []byte, f *Fields) []byte {
			return strconv.AppendInt(b, f.StartTime.Unix(), 10)
		}, nil
	case "msec":
		return func(b []byte, f *Fields) []byte {
			return strconv.AppendInt(b, f.StartTime.UnixMilli(), 10)
		}, nil
	case "usec":
		return func(b []byte, f *Fields) []byte {
			return strconv.AppendInt(b, f.StartTime.UnixMicro(), 10)
		}, nil
	case "":
		return nil, fmt.Errorf("%w: %%{}t", ErrInvalidFormatToken)
	}
	// any other argument is a Go time layout
	return func(b []byte, f *Fields) []byte {
		return f.StartTime.AppendFormat(b, arg)
	}, nil
}

func compileExtensionToken(arg string) (emitter, error) {
	switch arg {
	case "backend":
		return func(b []byte, f *Fields) []byte {
			return appendOrDash(b, f.Backend)
		}, nil
	case "provider":
		return func(b []byte, f *Fields) []byte {
			return appendOrDash(b, f.Provider)
		}, nil
	case "cache-status":
		return func(b []byte, f *Fields) []byte {
			return appendOrDash(b, f.CacheStatus)
		}, nil
	case "engine":
		return func(b []byte, f *Fields) []byte {
			return appendOrDash(b, f.Engine)
		}, nil
	case "path-config":
		return func(b []byte, f *Fields) []byte {
			return appendEscapedOrDash(b, f.PathConfig)
		}, nil
	}
	return nil, fmt.Errorf("%w: %%{%s}x", ErrInvalidFormatToken, arg)
}

func appendOrDash(b []byte, s string) []byte {
	if s == "" {
		return append(b, dash...)
	}
	return append(b, s...)
}

func appendEscapedOrDash(b []byte, s string) []byte {
	if s == "" {
		return append(b, dash...)
	}
	return appendEscaped(b, s)
}

func appendHeader(b []byte, h http.Header, key string) []byte {
	if h == nil {
		return append(b, dash...)
	}
	return appendEscapedOrDash(b, h.Get(key))
}

func appendCookie(b []byte, h http.Header, name string) []byte {
	if h != nil {
		if cookies, err := http.ParseCookie(h.Get("Cookie")); err == nil {
			for _, c := range cookies {
				if c.Name == name {
					return appendEscapedOrDash(b, c.Value)
				}
			}
		}
	}
	return append(b, dash...)
}

// appendEscaped appends s, backslash-escaping quotes, backslashes and
// control bytes so untrusted values cannot corrupt the log line structure
func appendEscaped(b []byte, s string) []byte {
	if !needsEscape(s) {
		return append(b, s...)
	}
	for i := range len(s) {
		c := s[i]
		switch {
		case c == '"' || c == '\\':
			b = append(b, '\\', c)
		case c == '\n':
			b = append(b, '\\', 'n')
		case c == '\r':
			b = append(b, '\\', 'r')
		case c == '\t':
			b = append(b, '\\', 't')
		case c < 0x20:
			b = append(b, '\\', 'x')
			const hex = "0123456789abcdef"
			b = append(b, hex[c>>4], hex[c&0xf])
		default:
			b = append(b, c)
		}
	}
	return b
}

func needsEscape(s string) bool {
	for i := range len(s) {
		if s[i] < 0x20 || s[i] == '"' || s[i] == '\\' {
			return true
		}
	}
	return false
}

// jsonEmitters returns the emitter chain for the fixed-field json preset
func jsonEmitters() []emitter {
	fields := []struct {
		key  string
		emit emitter
	}{
		{"time", func(b []byte, f *Fields) []byte {
			b = append(b, '"')
			b = f.StartTime.UTC().AppendFormat(b, time.RFC3339Nano)
			return append(b, '"')
		}},
		{"client_ip", func(b []byte, f *Fields) []byte {
			return appendJSONString(b, f.ClientIP)
		}},
		{"user", func(b []byte, f *Fields) []byte {
			return appendJSONString(b, f.User)
		}},
		{"method", func(b []byte, f *Fields) []byte {
			return appendJSONString(b, f.Method)
		}},
		{"path", func(b []byte, f *Fields) []byte {
			return appendJSONString(b, f.Path)
		}},
		{"query", func(b []byte, f *Fields) []byte {
			return appendJSONString(b, f.Query)
		}},
		{"proto", func(b []byte, f *Fields) []byte {
			return appendJSONString(b, f.Proto)
		}},
		{"status", func(b []byte, f *Fields) []byte {
			return strconv.AppendInt(b, int64(f.Status), 10)
		}},
		{"bytes", func(b []byte, f *Fields) []byte {
			return strconv.AppendInt(b, f.BytesWritten, 10)
		}},
		{"duration_ms", func(b []byte, f *Fields) []byte {
			return strconv.AppendInt(b, f.Duration.Milliseconds(), 10)
		}},
		{"host", func(b []byte, f *Fields) []byte {
			return appendJSONString(b, f.Host)
		}},
		{"referer", func(b []byte, f *Fields) []byte {
			return appendJSONHeader(b, f.ReqHeader, "Referer")
		}},
		{"user_agent", func(b []byte, f *Fields) []byte {
			return appendJSONHeader(b, f.ReqHeader, "User-Agent")
		}},
		{"backend", func(b []byte, f *Fields) []byte {
			return appendJSONString(b, f.Backend)
		}},
		{"provider", func(b []byte, f *Fields) []byte {
			return appendJSONString(b, f.Provider)
		}},
		{"path_config", func(b []byte, f *Fields) []byte {
			return appendJSONString(b, f.PathConfig)
		}},
		{"cache_status", func(b []byte, f *Fields) []byte {
			return appendJSONString(b, f.CacheStatus)
		}},
		{"engine", func(b []byte, f *Fields) []byte {
			return appendJSONString(b, f.Engine)
		}},
	}
	out := make([]emitter, 0, len(fields)+1)
	for i, fd := range fields {
		prefix := `,"` + fd.key + `":`
		if i == 0 {
			prefix = `{"` + fd.key + `":`
		}
		emit := fd.emit
		out = append(out, func(b []byte, f *Fields) []byte {
			return emit(append(b, prefix...), f)
		})
	}
	out = append(out, func(b []byte, _ *Fields) []byte {
		return append(b, '}')
	})
	return out
}

func appendJSONString(b []byte, s string) []byte {
	return strconv.AppendQuote(b, s)
}

func appendJSONHeader(b []byte, h http.Header, key string) []byte {
	if h == nil {
		return append(b, `""`...)
	}
	return strconv.AppendQuote(b, h.Get(key))
}
