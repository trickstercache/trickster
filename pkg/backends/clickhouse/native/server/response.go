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
	"bytes"
	"fmt"
	"io"

	"github.com/ClickHouse/ch-go/compress"
)

// writeException sends a ServerException packet.
func writeException(w *protoWriter, code int32, name, message string) error {
	if err := w.putByte(ServerException); err != nil {
		return err
	}
	if err := w.putInt32(code); err != nil {
		return err
	}
	if err := w.putStr(name); err != nil {
		return err
	}
	if err := w.putStr(message); err != nil {
		return err
	}
	if err := w.putStr(""); err != nil { // stack trace
		return err
	}
	return w.putBool(false) // has nested
}

// writeEndOfStream sends a ServerEndOfStream packet.
func writeEndOfStream(w *protoWriter) error {
	return w.putByte(ServerEndOfStream)
}

// writePong sends a ServerPong packet.
func writePong(w *protoWriter) error {
	return w.putByte(ServerPong)
}

// Column represents a named and typed column for writing data blocks.
type Column struct {
	Name string
	Type string
}

// writeDataBlock writes a ServerData packet containing a result block.
// values is a column-major 2D slice: values[col][row].
func writeDataBlock(w *protoWriter, columns []Column, values [][]any, numRows uint64) error {
	var buf bytes.Buffer
	if err := writeBlockContent(newProtoWriter(&buf), columns, values, numRows); err != nil {
		return err
	}
	return writeDataPacket(w, buf.Bytes())
}

func writeDataPacket(w *protoWriter, data []byte) error {
	if err := w.putByte(ServerData); err != nil {
		return err
	}
	if err := w.putStr(""); err != nil {
		return err
	}
	_, err := w.Write(data)
	return err
}

// writeEmptyBlock sends an empty ServerData block (0 columns, 0 rows).
func writeEmptyBlock(w *protoWriter) error {
	return writeDataBlock(w, nil, nil, 0)
}

// writeCompressedDataBlock writes a ServerData packet with LZ4-compressed
// block content.
func writeCompressedDataBlock(w *protoWriter, columns []Column, values [][]any, numRows uint64) error {
	var buf bytes.Buffer
	bw := newProtoWriter(&buf)
	if err := writeBlockContent(bw, columns, values, numRows); err != nil {
		return err
	}

	cw := compress.NewWriter(compress.LevelZero, compress.LZ4)
	if err := cw.Compress(buf.Bytes()); err != nil {
		return fmt.Errorf("compress block: %w", err)
	}
	return writeDataPacket(w, cw.Data)
}

// writeBlockContent writes block info + columns + data (no packet header).
func writeBlockContent(w *protoWriter, columns []Column, values [][]any, numRows uint64) error {
	return writeFormatBlock(w, columns, values, numRows, ServerRevision)
}

func writeFormatBlock(w *protoWriter, columns []Column, values [][]any, numRows uint64, revision uint64) error {
	if revision > 0 {
		if err := w.putUvarint(1); err != nil {
			return err
		}
		if err := w.putBool(false); err != nil {
			return err
		}
		if err := w.putUvarint(2); err != nil {
			return err
		}
		if err := w.putInt32(-1); err != nil {
			return err
		}
		if err := w.putUvarint(0); err != nil {
			return err
		}
	}

	numCols := uint64(len(columns))
	if err := w.putUvarint(numCols); err != nil {
		return err
	}
	if err := w.putUvarint(numRows); err != nil {
		return err
	}

	for i, col := range columns {
		if err := w.putStr(col.Name); err != nil {
			return err
		}
		if err := w.putStr(col.Type); err != nil {
			return err
		}
		if revision >= RevisionCustomSerialization {
			if err := w.putBool(false); err != nil {
				return err
			}
		}
		if numRows > 0 {
			if err := encodeColumn(w, col.Type, values[i]); err != nil {
				return fmt.Errorf("write column %q data: %w", col.Name, err)
			}
		}
	}
	return nil
}

// EncodeNativeBlock writes a binary result block without the native packet envelope.
func EncodeNativeBlock(w io.Writer, columns []Column, values [][]any, rows uint64) error {
	return writeBlockContent(newProtoWriter(w), columns, values, rows)
}

// EncodeNativeFormat writes an HTTP Native-format response for the requested revision.
func EncodeNativeFormat(w io.Writer, columns []Column, values [][]any, rows, revision uint64) error {
	return writeFormatBlock(newProtoWriter(w), columns, values, rows, revision)
}
