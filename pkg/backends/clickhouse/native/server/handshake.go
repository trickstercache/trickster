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

import "fmt"

// ClientHelloMsg contains the fields sent by the client in the Hello packet.
type ClientHelloMsg struct {
	ClientName    string
	VersionMajor  uint64
	VersionMinor  uint64
	ProtoRevision uint64
	Database      string
	Username      string
	Password      string
}

// readClientHello reads a ClientHello packet from the wire. The leading packet
// type byte must already have been consumed.
func readClientHello(r *protoReader) (*ClientHelloMsg, error) {
	name, err := r.str()
	if err != nil {
		return nil, fmt.Errorf("read client name: %w", err)
	}
	major, err := r.uvarint()
	if err != nil {
		return nil, fmt.Errorf("read major version: %w", err)
	}
	minor, err := r.uvarint()
	if err != nil {
		return nil, fmt.Errorf("read minor version: %w", err)
	}
	revision, err := r.uvarint()
	if err != nil {
		return nil, fmt.Errorf("read protocol revision: %w", err)
	}
	db, err := r.str()
	if err != nil {
		return nil, fmt.Errorf("read database: %w", err)
	}
	user, err := r.str()
	if err != nil {
		return nil, fmt.Errorf("read username: %w", err)
	}
	pass, err := r.str()
	if err != nil {
		return nil, fmt.Errorf("read password: %w", err)
	}
	return &ClientHelloMsg{
		ClientName:    name,
		VersionMajor:  major,
		VersionMinor:  minor,
		ProtoRevision: revision,
		Database:      db,
		Username:      user,
		Password:      pass,
	}, nil
}

// writeServerHello sends a ServerHello response to the client.
func writeServerHello(w *protoWriter) error {
	if err := w.putByte(ServerHello); err != nil {
		return err
	}
	// Server name
	if err := w.putStr("Trickster"); err != nil {
		return err
	}
	// Version major/minor
	if err := w.putUvarint(2); err != nil {
		return err
	}
	if err := w.putUvarint(0); err != nil {
		return err
	}
	// Revision
	if err := w.putUvarint(ServerRevision); err != nil {
		return err
	}
	// Timezone (revision >= 54058)
	if err := w.putStr("UTC"); err != nil {
		return err
	}
	// Display name (revision >= 54372)
	if err := w.putStr("Trickster"); err != nil {
		return err
	}
	// Version patch (revision >= 54401)
	return w.putUvarint(0)
}
