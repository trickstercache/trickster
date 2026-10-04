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

package ipacl

import (
	"bufio"
	"fmt"
	"io"
	"net/netip"
	"os"
	"strings"

	"github.com/trickstercache/trickster/v2/pkg/util/prefixtable"
)

func parseEntry(raw string) ([]netip.Prefix, error) {
	// a CIDR is tried first and masked, then a lone address as a full-length prefix; a mapped entry is stored as
	// IPv4, and all names both families
	if raw == EntryAll {
		return []netip.Prefix{
			netip.PrefixFrom(netip.IPv4Unspecified(), 0),
			netip.PrefixFrom(netip.IPv6Unspecified(), 0),
		}, nil
	}
	if p, err := netip.ParsePrefix(raw); err == nil {
		return []netip.Prefix{prefixtable.Canonical(p)}, nil
	}
	addr, err := netip.ParseAddr(raw)
	if err != nil {
		return nil, fmt.Errorf("%w: %q", ErrInvalidEntry, raw)
	}
	// PrefixFrom strips a zone, as clientip.ParseTrusted does, so fe80::1%eth0
	// is the same entry as fe80::1.
	addr = addr.Unmap()
	return []netip.Prefix{netip.PrefixFrom(addr, addr.BitLen())}, nil
}

type fileLine struct {
	n    int
	text string
}

func loadFile(path string) ([]fileLine, error) {
	// #nosec G304 -- operator-configured ACL file, read when the list is compiled
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("%w: %q: %w", ErrInvalidFile, path, err)
	}
	defer f.Close()
	if st, statErr := f.Stat(); statErr == nil && st.IsDir() {
		return nil, fmt.Errorf("%w: %q is a directory", ErrInvalidFile, path)
	}
	lines, err := parseLines(f)
	if err != nil {
		return nil, fmt.Errorf("%w: %q: %w", ErrInvalidFile, path, err)
	}
	return lines, nil
}

func parseLines(r io.Reader) ([]fileLine, error) {
	// lines are numbered from 1 for errors; a # opens a comment only as a line's first non-space character, since an
	// entry is one address or CIDR
	sc := bufio.NewScanner(r)
	var out []fileLine
	n := 0
	for sc.Scan() {
		n++
		text := strings.TrimSpace(sc.Text())
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		out = append(out, fileLine{n: n, text: text})
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
