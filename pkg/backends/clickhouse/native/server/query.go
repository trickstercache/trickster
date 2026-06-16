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

package server

import (
	"fmt"
	"io"
)

// ClientQueryMsg contains the relevant fields from a ClientQuery packet.
// We parse enough to extract the SQL but skip over the full client info
// and settings blocks.
type ClientQueryMsg struct {
	QueryID     string
	SQL         string
	Compression bool
}

// readClientQuery reads a ClientQuery packet from the wire. The leading
// packet type byte must already have been consumed. revision is the
// protocol revision the client announced in its Hello.
func readClientQuery(r *protoReader, revision uint64) (*ClientQueryMsg, error) {
	queryID, err := r.str()
	if err != nil {
		return nil, fmt.Errorf("read query id: %w", err)
	}

	// --- client info ---
	if err := skipClientInfo(r, revision); err != nil {
		return nil, fmt.Errorf("skip client info: %w", err)
	}

	// --- settings ---
	if err := skipSettings(r, revision); err != nil {
		return nil, fmt.Errorf("skip settings: %w", err)
	}

	// --- interserver secret (revision >= 54441) ---
	if revision >= RevisionInterserverSecret {
		if _, err := r.str(); err != nil {
			return nil, fmt.Errorf("read interserver secret: %w", err)
		}
	}

	// state + compression
	state, err := r.uvarint()
	if err != nil {
		return nil, fmt.Errorf("read state: %w", err)
	}
	_ = state

	compBool, err := r.boolean()
	if err != nil {
		return nil, fmt.Errorf("read compression flag: %w", err)
	}

	// query body
	sql, err := r.str()
	if err != nil {
		return nil, fmt.Errorf("read query body: %w", err)
	}

	// parameters (revision >= 54459)
	if revision >= RevisionParameters {
		if err := skipKeyValuePairs(r); err != nil {
			return nil, fmt.Errorf("skip parameters: %w", err)
		}
	}

	return &ClientQueryMsg{
		QueryID:     queryID,
		SQL:         sql,
		Compression: compBool,
	}, nil
}

// skipClientInfo reads and discards the client info section.
func skipClientInfo(r *protoReader, revision uint64) error {
	// query kind
	if _, err := r.ReadByte(); err != nil {
		return err
	}
	// initial user, initial query id, initial address
	for range 3 {
		if _, err := r.str(); err != nil {
			return err
		}
	}
	// initial query start time (revision >= 54449)
	if revision >= RevisionInitialQueryStart {
		var b [8]byte
		if _, err := io.ReadFull(r, b[:]); err != nil {
			return err
		}
	}
	// interface type
	if _, err := r.ReadByte(); err != nil {
		return err
	}
	// os user, os hostname, client name
	for range 3 {
		if _, err := r.str(); err != nil {
			return err
		}
	}
	// client version major, minor, protocol revision
	for range 3 {
		if _, err := r.uvarint(); err != nil {
			return err
		}
	}
	// quota key (revision >= 54060)
	if revision >= RevisionQuotaKey {
		if _, err := r.str(); err != nil {
			return err
		}
	}
	// distributed depth (revision >= 54448)
	if revision >= RevisionDistributedDepth {
		if _, err := r.uvarint(); err != nil {
			return err
		}
	}
	// client version patch (revision >= 54401)
	if revision >= RevisionVersionPatch {
		if _, err := r.uvarint(); err != nil {
			return err
		}
	}
	// OpenTelemetry (revision >= 54442)
	if revision >= RevisionOpenTelemetry {
		hasSpan, err := r.ReadByte()
		if err != nil {
			return err
		}
		if hasSpan != 0 {
			// trace id (16) + span id (8) + trace state (string) + flags (1)
			var skip [24]byte
			if _, err := io.ReadFull(r, skip[:]); err != nil {
				return err
			}
			if _, err := r.str(); err != nil {
				return err
			}
			if _, err := r.ReadByte(); err != nil {
				return err
			}
		}
	}
	// parallel replicas (revision >= 54453)
	if revision >= RevisionParallelReplicas {
		for range 3 {
			if _, err := r.uvarint(); err != nil {
				return err
			}
		}
	}
	return nil
}

// skipSettings reads and discards the settings key=value list.
func skipSettings(r *protoReader, revision uint64) error {
	for {
		name, err := r.str()
		if err != nil {
			return err
		}
		if name == "" {
			break
		}
		if revision <= 54429 {
			// old format: uvarint value
			if _, err := r.uvarint(); err != nil {
				return err
			}
		} else {
			// new format: flags (uvarint) + string value
			if _, err := r.uvarint(); err != nil {
				return err
			}
			if _, err := r.str(); err != nil {
				return err
			}
		}
	}
	return nil
}

// skipKeyValuePairs reads string key/value pairs terminated by an empty key.
func skipKeyValuePairs(r *protoReader) error {
	for {
		name, err := r.str()
		if err != nil {
			return err
		}
		if name == "" {
			break
		}
		// flags + value
		if _, err := r.uvarint(); err != nil {
			return err
		}
		if _, err := r.str(); err != nil {
			return err
		}
	}
	return nil
}

// skipClientData reads and discards a ClientData block (the empty data block
// that clients send after a query). The leading packet type byte must already
// have been consumed.
func skipClientData(r *protoReader, revision uint64) error {
	// block name (external table name, typically empty)
	if _, err := r.str(); err != nil {
		return err
	}
	// block info
	if revision > 0 {
		// is_overflows (uvarint), bucket_num (bool)
		if _, err := r.uvarint(); err != nil {
			return err
		}
		if _, err := r.boolean(); err != nil {
			return err
		}
		// bucket_size (uvarint)
		if _, err := r.uvarint(); err != nil {
			return err
		}
		// reserved int32
		if _, err := r.int32(); err != nil {
			return err
		}
		// reserved uvarint
		if _, err := r.uvarint(); err != nil {
			return err
		}
	}
	// num columns, num rows
	numCols, err := r.uvarint()
	if err != nil {
		return err
	}
	numRows, err := r.uvarint()
	if err != nil {
		return err
	}
	_ = numCols
	_ = numRows
	// For the empty data block after query, both should be 0.
	// If not, we'd need to read column data, but we don't support INSERT.
	return nil
}
