//go:build linux || darwin || freebsd

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

import "golang.org/x/sys/unix"

// FreeBytes returns the space left to an unprivileged user on the cache path's filesystem
func (s *Store) FreeBytes() (int64, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(s.root, &st); err != nil {
		return 0, err
	}
	// #nosec G115 -- block counts and sizes are far below the int64 range, and the types vary by platform
	return int64(st.Bavail) * int64(st.Bsize), nil
}
