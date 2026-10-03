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
)

// parseEntry parses one address, CIDR or the keyword all.
//
// Parsing follows clientip.ParseTrusted: a CIDR is tried first and masked,
// otherwise a single address is a full-length prefix. IPv4-mapped IPv6 is
// stored as IPv4, so an entry written either way matches the same addresses
// Check sees after Unmap. all is both families. A v6 prefix that merely
// contains mapped addresses, such as ::/0, stays v6: all is the way to name
// both families, and Check has already turned mapped addresses into v4.
func parseEntry(raw string) ([]netip.Prefix, error) {
	if raw == entryAll {
		return []netip.Prefix{
			netip.PrefixFrom(netip.IPv4Unspecified(), 0),
			netip.PrefixFrom(netip.IPv6Unspecified(), 0),
		}, nil
	}
	if p, err := netip.ParsePrefix(raw); err == nil {
		return normalizePrefix(p), nil
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

// normalizePrefix masks p and rewrites an IPv4-mapped prefix of at least
// /96 as the IPv4 prefix those bits name. ::ffff:10.0.0.0/104 is 10.0.0.0/8,
// and ::ffff:0:0/96 is every IPv4 address. A shorter prefix does not include
// the mapped marker in its network, so masking leaves an ordinary v6 prefix.
func normalizePrefix(p netip.Prefix) []netip.Prefix {
	p = p.Masked()
	addr := p.Addr()
	if addr.Is4() {
		return []netip.Prefix{p}
	}
	const mappedBits = 96
	if addr.Is4In6() && p.Bits() >= mappedBits {
		v4 := netip.PrefixFrom(addr.Unmap(), p.Bits()-mappedBits).Masked()
		return []netip.Prefix{v4}
	}
	return []netip.Prefix{p}
}

type fileLine struct {
	n    int
	text string
}

// loadFile reads an allow or deny file. Blank lines and comment lines are
// skipped. A missing file, a directory or an unreadable file is an error.
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

// parseLines returns one entry per line, numbered from 1 so an error can
// name the line. A blank line, or a line whose first non-space character
// is #, is skipped. A # later on the line is part of the entry: the file
// format is one address or CIDR per line, not an inline comment.
func parseLines(r io.Reader) ([]fileLine, error) {
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
