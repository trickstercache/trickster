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

package client

import (
	"bufio"
	"io"
	"net"
	"net/netip"
	"os"
	"strings"
)

const (
	// DefaultResolvConfPath is the conventional location of the system
	// resolver configuration on unix hosts
	DefaultResolvConfPath = "/etc/resolv.conf"
	// DefaultPort is the standard DNS service port
	DefaultPort = "53"

	nameserverKeyword = "nameserver"
)

// ResolvConf is the subset of a resolver configuration file this client uses
type ResolvConf struct {
	// Servers are the configured nameservers, as host:port
	Servers []string
}

// LoadResolvConf reads the nameservers from a resolv.conf-formatted file.
// Entries that are not valid IP addresses are skipped.
func LoadResolvConf(path string) (*ResolvConf, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return parseResolvConf(f)
}

func parseResolvConf(r io.Reader) (*ResolvConf, error) {
	out := &ResolvConf{}
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := sc.Text()
		if i := strings.IndexAny(line, "#;"); i >= 0 {
			line = line[:i]
		}
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[0] != nameserverKeyword {
			continue
		}
		if addr, err := netip.ParseAddr(fields[1]); err == nil {
			out.Servers = append(out.Servers,
				net.JoinHostPort(addr.String(), DefaultPort))
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(out.Servers) == 0 {
		return nil, ErrNoServers
	}
	return out, nil
}
