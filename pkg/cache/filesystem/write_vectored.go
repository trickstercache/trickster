//go:build linux || darwin

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

package filesystem

import (
	"os"
	"runtime"

	"golang.org/x/sys/unix"
)

const frameSections = 3

// one system call for the three sections, so that they are never joined in memory
func writeFrame(f *os.File, hdr, meta, body []byte) error {
	var sections [frameSections][]byte
	iov := sections[:0]
	for _, section := range [frameSections][]byte{hdr, meta, body} {
		if len(section) > 0 {
			iov = append(iov, section)
		}
	}
	if len(iov) == 0 {
		return nil
	}
	var n int
	var err error
	for {
		// #nosec G115 -- a file descriptor fits an int
		n, err = unix.Writev(int(f.Fd()), iov)
		if err != unix.EINTR {
			break
		}
	}
	runtime.KeepAlive(f)
	if err != nil {
		return err
	}
	if n == len(hdr)+len(meta)+len(body) {
		return nil
	}
	// the system wrote only part, as it may of a very large frame; the rest follows in order
	return writeSections(f, n, hdr, meta, body)
}
