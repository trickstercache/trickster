/**
* Copyright 2018 Comcast Cable Communications Management, LLC
* Licensed under the Apache License, Version 2.0 (the "License");
* you may not use this file except in compliance with the License.
* You may obtain a copy of the License at
* http://www.apache.org/licenses/LICENSE-2.0
* Unless required by applicable law or agreed to in writing, software
* distributed under the License is distributed on an "AS IS" BASIS,
* WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
* See the License for the specific language governing permissions and
* limitations under the License.
 */

package engines

import (
	"errors"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

var ErrReadIndexTooLarge = errors.New("Read index too large")

// NEED TO DEAL WITH TIMEOUT

// IndexReader implements a reader to read data at a specific index into slice b
type IndexReader func(index uint64, b []byte) (int, error)

// ProxyForwardCollapser accepts data written through the io.Writer interface, caches it and
// makes all the data written available to n readers. The readers can request data at index i,
// to which the PFC may block or return the data immediately.
type ProxyForwardCollapser interface {
	AddClient(io.Writer) error
	Write([]byte) (int, error)
	Close()
	IndexRead(uint64, []byte) (int, error)
	WaitServerComplete()
	WaitAllComplete()
	GetBody() ([]byte, error)
	GetResp() *http.Response
}

type proxyForwardCollapser struct {
	resp            *http.Response
	rIndex          uint64
	dataIndex       uint64
	data            [][]byte
	dataLen         uint64
	dataStore       []byte
	dataStoreLen    uint64
	readCond        *sync.Cond
	serverReadDone  int32
	clientWaitgroup *sync.WaitGroup
	serverWaitCond  *sync.Cond
}

// NewPFC returns a new instance of a ProxyForwardCollapser
func NewPFC(clientTimeout time.Duration, resp *http.Response, contentLength int) ProxyForwardCollapser {
	// This contiguous block of memory is just an underlying byte store, references by the slices defined in refs
	// Thread safety is provided through a read index, an atomic, which the writer must exceed and readers may not exceed
	// This effectively limits the readers and writer to separate areas in memory.
	dataStore := make([]byte, contentLength)
	refs := make([][]byte, ((contentLength/HTTPBlockSize)*2)+1)

	var wg sync.WaitGroup
	sd := sync.NewCond(&sync.Mutex{})
	rc := sync.NewCond(&sync.Mutex{})

	pfc := &proxyForwardCollapser{
		resp:            resp,
		rIndex:          0,
		dataIndex:       0,
		data:            refs,
		dataLen:         uint64(len(refs)),
		dataStore:       dataStore,
		dataStoreLen:    uint64(contentLength),
		readCond:        rc,
		serverReadDone:  0,
		clientWaitgroup: &wg,
		serverWaitCond:  sd,
	}

	return pfc
}

// AddClient adds an io.Writer client to the Proxy Forward Collapser
// This client will read all the cached data and read from the live edge if caught up.
func (pfc *proxyForwardCollapser) AddClient(w io.Writer) error {
	pfc.clientWaitgroup.Add(1)
	var readIndex uint64
	var err error
	remaining := 0
	n := 0
	buf := make([]byte, HTTPBlockSize)

	for {
		n, err = pfc.IndexRead(readIndex, buf)
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
			if err != io.EOF {
				return err
			}
			break
		}
	}
	pfc.clientWaitgroup.Done()
	return io.EOF
}

// WaitServerComplete blocks until the object has been retrieved from the origin server
// Need to get payload before can send to actual cache
func (pfc *proxyForwardCollapser) WaitServerComplete() {
	pfc.serverWaitCond.Wait()
	return
}

// WaitAllComplete will wait till all clients have completed or timedout
// Need to no abandon goroutines
func (pfc *proxyForwardCollapser) WaitAllComplete() {
	pfc.clientWaitgroup.Wait()
	return
}

// GetBody returns the underlying body of the data written into a PFC
func (pfc *proxyForwardCollapser) GetBody() ([]byte, error) {
	if atomic.LoadInt32(&pfc.serverReadDone) == 0 {
		return nil, errors.New("Server request not completed")
	}
	return pfc.dataStore[0:pfc.dataIndex], nil
}

func (pfc *proxyForwardCollapser) GetResp() *http.Response {
	return pfc.resp
}

// Write writes the data in b to the Proxy Forward Collapsers data store,
// adds a reference to that data, and increments the read index.
func (pfc *proxyForwardCollapser) Write(b []byte) (int, error) {
	n := atomic.LoadUint64(&pfc.rIndex)
	l := uint64(len(b))
	if pfc.dataIndex+l > pfc.dataStoreLen {
		// Should reallocate and copy?
	} else if n > pfc.dataLen {
		// Should reallocate and copy?
	}
	pfc.data[n] = pfc.dataStore[pfc.dataIndex : pfc.dataIndex+l]
	copy(pfc.data[n], b)
	pfc.dataIndex += l
	atomic.AddUint64(&pfc.rIndex, 1)
	pfc.readCond.Broadcast()
	return len(b), nil
}

// Close signals all things waiting on the server response body to complete.
// This should be triggered by the client io.EOF
func (pfc *proxyForwardCollapser) Close() {
	atomic.AddInt32(&pfc.serverReadDone, 1)
	pfc.serverWaitCond.Signal()
	pfc.readCond.Broadcast()
	return
}

// Read will return the given index data requested by the read is behind the PFC readindex, else blocks and waits for the data
func (pfc *proxyForwardCollapser) IndexRead(index uint64, b []byte) (int, error) {
	i := atomic.LoadUint64(&pfc.rIndex)
	if index >= i {
		// need to check completion and return io.EOF
		if index > pfc.dataLen {
			return 0, ErrReadIndexTooLarge
		} else if atomic.LoadInt32(&pfc.serverReadDone) != 0 {
			return 0, io.EOF
		}
		pfc.readCond.L.Lock()
		pfc.readCond.Wait()
		pfc.readCond.L.Unlock()
	}
	copy(b, pfc.data[index])
	return len(pfc.data[index]), nil
}
