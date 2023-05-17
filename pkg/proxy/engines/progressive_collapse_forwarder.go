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

package engines

import (
	"io"
	"net/http"
	"sync"
	"sync/atomic"

	"github.com/trickstercache/trickster/v2/pkg/proxy/errors"
)

// NEED TO DEAL WITH TIMEOUT

// IndexReader implements a reader to read data at a specific index into slice b
type IndexReader func(index uint64, b []byte) (int, error)

// ProgressiveCollapseForwarder accepts data written through the io.Writer interface, caches it and
// makes all the data written available to n readers. The readers can request data at index i,
// to which the PCF may block or return the data immediately.
type ProgressiveCollapseForwarder interface {
	AddClient(io.Writer) error
	Write([]byte) (int, error)
	Close()
	IndexRead(uint64, []byte) (int, error)
	WaitServerComplete()
	WaitAllComplete()
	GetBody() ([]byte, error)
	GetResp() *http.Response
}

type progressiveCollapseForwarder struct {
	resp            *http.Response
	rIndex          atomic.Uint64
	dataIndex       uint64
	data            [][]byte
	dataLen         uint64
	dataStore       []byte
	dataStoreLen    uint64
	readCond        *sync.Cond
	serverReadDone  atomic.Int32
	clientWaitgroup *sync.WaitGroup
	serverWaitCond  *sync.Cond
}

// NewPCF returns a new instance of a ProgressiveCollapseForwarder
func NewPCF(resp *http.Response, contentLength int64) ProgressiveCollapseForwarder {
	// This contiguous block of memory is just an underlying byte store, references by the slices defined in refs
	// Thread safety is provided through a read index, an atomic, which the writer must exceed and readers may not exceed
	// This effectively limits the readers and writer to separate areas in memory.
	if contentLength < 0 {
		return nil
	}
	dataStore := make([]byte, contentLength)
	refs := make([][]byte, ((contentLength/HTTPBlockSize)*2)+1)

	var wg sync.WaitGroup
	sd := sync.NewCond(&sync.Mutex{})
	rc := sync.NewCond(&sync.Mutex{})

	pcf := &progressiveCollapseForwarder{
		resp:            resp,
		dataIndex:       0,
		data:            refs,
		dataLen:         uint64(len(refs)),
		dataStore:       dataStore,
		dataStoreLen:    uint64(contentLength),
		readCond:        rc,
		clientWaitgroup: &wg,
		serverWaitCond:  sd,
	}
	pcf.rIndex.Store(0)
	pcf.serverReadDone.Store(0)

	return pcf
}

// AddClient adds an io.Writer client to the ProgressiveCollapseForwarder
// This client will read all the cached data and read from the live edge if caught up.
func (pcf *progressiveCollapseForwarder) AddClient(w io.Writer) error {
	pcf.clientWaitgroup.Add(1)
	var readIndex uint64
	var err error
	remaining := 0
	n := 0
	buf := make([]byte, HTTPBlockSize)

	for {
		n, err = pcf.IndexRead(readIndex, buf)
		if n > 0 {
			// Handle the data returned by the read index > HTTPBlockSize
			if n > HTTPBlockSize {
				remaining = n
				for {
					if remaining > HTTPBlockSize {
						w.Write(buf[0:HTTPBlockSize])
						remaining -= HTTPBlockSize
					} else {
						w.Write(buf[0:remaining])
						break
					}
				}
			} else {
				w.Write(buf[0:n])
			}
			readIndex++
		}
		if err != nil {
			// return error at end of function
			// Nominal case should be io.EOF
			break
		}
	}
	pcf.clientWaitgroup.Done()
	return err
}

// WaitServerComplete blocks until the object has been retrieved from the origin server
// Need to get payload before can send to actual cache
func (pcf *progressiveCollapseForwarder) WaitServerComplete() {
	if pcf.serverReadDone.Load() != 0 {
		return
	}
	pcf.serverWaitCond.L.Lock()
	pcf.serverWaitCond.Wait()
	pcf.serverWaitCond.L.Unlock()
}

// WaitAllComplete will wait till all clients have completed or timedout
// Need to no abandon goroutines
func (pcf *progressiveCollapseForwarder) WaitAllComplete() {
	pcf.clientWaitgroup.Wait()
}

// GetBody returns the underlying body of the data written into a PCF
func (pcf *progressiveCollapseForwarder) GetBody() ([]byte, error) {
	if pcf.serverReadDone.Load() == 0 {
		return nil, errors.ErrServerRequestNotCompleted
	}
	return pcf.dataStore[0:pcf.dataIndex], nil
}

// GetResp returns the response from the original request
func (pcf *progressiveCollapseForwarder) GetResp() *http.Response {
	return pcf.resp
}

// Write writes the data in b to the ProgressiveCollapseForwarders data store,
// adds a reference to that data, and increments the read index.
func (pcf *progressiveCollapseForwarder) Write(b []byte) (int, error) {
	n := pcf.rIndex.Load()
	if pcf.dataIndex+uint64(len(b)) > pcf.dataStoreLen || n > pcf.dataLen {
		return 0, io.ErrShortWrite
	}
	pcf.data[n] = pcf.dataStore[pcf.dataIndex : pcf.dataIndex+uint64(len(b))]
	copy(pcf.data[n], b)
	pcf.dataIndex += uint64(len(b))
	pcf.rIndex.Add(1)
	pcf.readCond.Broadcast()
	return len(b), nil
}

// Close signals all things waiting on the server response body to complete.
// This should be triggered by the client io.EOF
func (pcf *progressiveCollapseForwarder) Close() {
	pcf.serverReadDone.Add(1)
	pcf.serverWaitCond.Broadcast()
	pcf.readCond.Broadcast()
}

// Read will return the given index data requested by the read is behind the PCF readindex,
// else blocks and waits for the data
func (pcf *progressiveCollapseForwarder) IndexRead(index uint64, b []byte) (int, error) {
	i := pcf.rIndex.Load()
	if index >= i {
		// need to check completion and return io.EOF
		if index > pcf.dataLen {
			return 0, errors.ErrReadIndexTooLarge
		} else if pcf.serverReadDone.Load() != 0 {
			return 0, io.EOF
		}
		pcf.readCond.L.Lock()
		pcf.readCond.Wait()
		pcf.readCond.L.Unlock()
	}
	copy(b, pcf.data[index])
	return len(pcf.data[index]), nil
}
