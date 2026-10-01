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

package index

import (
	"bytes"
	"io"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/trickstercache/trickster/v2/pkg/cache/blob/blobtest"
	"github.com/trickstercache/trickster/v2/pkg/observability/metrics"
	"github.com/trickstercache/trickster/v2/pkg/parsing/timeconv"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

func readMeta(t testing.TB, s *blobtest.MemStore, name string) []byte {
	t.Helper()
	r, err := s.OpenMeta(name)
	require.NoError(t, err)
	b, err := io.ReadAll(r)
	require.NoError(t, err)
	return b
}

func metaNames(t testing.TB, s *blobtest.MemStore) []string {
	t.Helper()
	names, err := s.ListMeta()
	require.NoError(t, err)
	return names
}

// decodes up to the first record that is torn or damaged
func records(b []byte) []record {
	var out []record
	r := bytes.NewReader(b)
	buf := make([]byte, recordOverhead+maxRecordKeyLen)
	for {
		rec, err := readRecord(r, buf)
		if err != nil {
			return out
		}
		out = append(out, rec)
	}
}

func TestRecordRoundTrip(t *testing.T) {
	want := []record{
		{op: opAdd, key: "key", size: 1, expiration: 2, lastWrite: 3, lastAccess: 4},
		{op: opAdd, key: strings.Repeat("k", maxRecordKeyLen), size: 1 << 40, expiration: -1},
		{op: opRemove, key: "key"},
		{op: opMark, lastWrite: 1_700_000_000_000_000_000},
		{op: opAdd},
	}
	var b []byte
	for i := range want {
		b = appendRecord(b, &want[i])
	}
	require.Equal(t, want, records(b))
}

func TestReadRecordRejects(t *testing.T) {
	good := appendRecord(nil, &record{op: opAdd, key: "key", size: 1})
	buf := make([]byte, recordOverhead+maxRecordKeyLen)
	_, err := readRecord(bytes.NewReader(nil), buf)
	require.ErrorIs(t, err, io.EOF)

	mutate := func(i int, v byte) []byte {
		b := bytes.Clone(good)
		b[i] = v
		return b
	}
	for name, b := range map[string][]byte{
		"torn head":      good[:recordHeadLen-1],
		"torn key":       good[:recordHeadLen+1],
		"torn checksum":  good[:len(good)-1],
		"no operation":   mutate(0, 0),
		"bad operation":  mutate(0, opMark+1),
		"key too long":   mutate(offRecordKeyLen+1, 0xff),
		"size changed":   mutate(offRecordSize, 9),
		"key changed":    mutate(recordHeadLen, 'x'),
		"bad checksum":   mutate(len(good)-1, good[len(good)-1]^1),
		"longer key len": mutate(offRecordKeyLen, 4),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := readRecord(bytes.NewReader(b), buf)
			require.ErrorIs(t, err, errCorruptRecord)
		})
	}
}

func FuzzReadRecord(f *testing.F) {
	f.Add(appendRecord(nil, &record{op: opAdd, key: "key", size: 1}))
	f.Add(appendMark(nil, time.Unix(1, 0)))
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, b []byte) {
		for _, rec := range records(b) {
			if len(rec.key) > maxRecordKeyLen || rec.op < opAdd || rec.op > opMark {
				t.Fatalf("accepted %+v", rec)
			}
		}
	})
}

func TestGenerationNames(t *testing.T) {
	require.Equal(t, "index.00000000000000000007", generationName(snapshotPrefix, 7))
	require.Equal(t, "journal.18446744073709551615", generationName(journalPrefix, 1<<64-1))
	names := []string{generationName(journalPrefix, 10), generationName(journalPrefix, 9)}
	slices.Sort(names)
	require.Equal(t, generationName(journalPrefix, 9), names[0], "names sort as their generations do")

	g, ok := generationOf("index.00000000000000000007", snapshotPrefix)
	require.True(t, ok)
	require.Equal(t, uint64(7), g)
	for _, name := range []string{
		"journal.00000000000000000007", "index.7", "index.0000000000000000000x", "index.99999999999999999999", cleanMarker,
	} {
		_, ok = generationOf(name, snapshotPrefix)
		require.False(t, ok, name)
	}
	require.Equal(t, uint64(7), generationOfAny("journal.00000000000000000007"))
	require.Zero(t, generationOfAny(cleanMarker))
}

func TestNilJournal(t *testing.T) {
	var j *journal
	j.add(&Object{Key: "k"})
	j.remove("k")
}

func TestJournalRecordsChanges(t *testing.T) {
	s := blobtest.NewMemStore()
	idx := openIndex(t, s, idleOpts())
	require.NotNil(t, idx.journal)
	require.True(t, idx.sweepDue, "an index that was never persisted cannot vouch for the cache")
	require.Empty(t, metaNames(t, s))

	require.NoError(t, idx.Store("kept", []byte("123"), time.Hour))
	require.NoError(t, idx.Store("forever", []byte("1"), 0))
	require.NoError(t, idx.Store("removed", []byte("12"), time.Hour))
	require.NoError(t, idx.Remove("removed", "absent"))
	require.NoError(t, idx.Store("brief", []byte("1"), transientTTL-time.Second))
	require.NoError(t, idx.Remove("brief"))
	require.NoError(t, idx.Store("shortened", []byte("1"), time.Hour))
	require.NoError(t, idx.Store("shortened", []byte("1"), time.Second))
	require.NoError(t, idx.Store("lengthened", []byte("1"), time.Second))
	require.NoError(t, idx.Store("lengthened", []byte("1234"), time.Hour))
	require.Empty(t, metaNames(t, s), "nothing is written until the index is flushed")

	before := time.Now().UnixNano()
	idx.flushOnce()
	name := generationName(journalPrefix, 1)
	require.Equal(t, []string{name}, metaNames(t, s))
	got := records(readMeta(t, s, name))
	type change struct {
		op   byte
		key  string
		size int64
	}
	var changes []change
	for _, r := range got[:len(got)-1] {
		changes = append(changes, change{r.op, r.key, r.size})
	}
	require.Equal(t, []change{
		{opAdd, "kept", 3}, {opAdd, "forever", 1}, {opAdd, "removed", 2}, {opRemove, "removed", 0},
		// an object too brief to persist is journaled only to take back what was persisted of it
		{opAdd, "shortened", 1}, {opRemove, "shortened", 0}, {opAdd, "lengthened", 4},
	}, changes)
	mark := got[len(got)-1]
	require.Equal(t, byte(opMark), mark.op)
	require.GreaterOrEqual(t, mark.lastWrite, before)
	require.Equal(t, mark.lastWrite, idx.lastFlush.Load())
	kept, _ := idx.Object("kept")
	require.Equal(t, kept.expiration.Load(), got[0].expiration)
	require.Equal(t, kept.lastWrite.Load(), got[0].lastWrite)

	// a flush with nothing to persist writes nothing
	idx.flushOnce()
	require.Len(t, readMeta(t, s, name), len(got)*recordOverhead+len("keptforeverremovedremovedshortenedshortenedlengthened"))
}

func TestReopen(t *testing.T) {
	s := blobtest.NewMemStore()
	idx := openIndex(t, s, idleOpts())
	for i := range 10 {
		require.NoError(t, idx.Store("key-"+strconv.Itoa(i), make([]byte, i+1), time.Hour))
	}
	require.NoError(t, idx.Remove("key-3"))
	require.NoError(t, idx.Store("brief", []byte("1"), time.Second))
	idx.Retrieve("key-5")
	want := map[string]record{}
	idx.each(func(o *Object) bool {
		want[o.Key] = record{
			size: o.size.Load(), expiration: o.expiration.Load(), lastWrite: o.lastWrite.Load(),
		}
		return true
	})
	delete(want, "brief")
	require.NoError(t, idx.Close())
	require.Contains(t, metaNames(t, s), cleanMarker)

	idx = openIndex(t, s, idleOpts())
	require.False(t, idx.sweepDue, "an index closed in good order vouches for the cache")
	require.NotContains(t, metaNames(t, s), cleanMarker, "and no longer does once it is in use again")
	requireTotals(t, idx, 9, 55-4)
	got := map[string]record{}
	idx.each(func(o *Object) bool {
		got[o.Key] = record{
			size: o.size.Load(), expiration: o.expiration.Load(), lastWrite: o.lastWrite.Load(),
		}
		return true
	})
	require.Equal(t, want, got)
	require.NotZero(t, idx.lastFlush.Load())

	// what was reopened expires as it would have
	idx.reapAt(time.Now().Add(2 * time.Hour).UnixNano())
	requireTotals(t, idx, 0, 0)
}

func TestReopenAfterCrash(t *testing.T) {
	s := blobtest.NewMemStore()
	idx := openIndex(t, s, idleOpts())
	require.NoError(t, idx.Store("flushed", []byte("1"), time.Hour))
	idx.flushOnce()
	require.NoError(t, idx.Store("unflushed", []byte("12"), time.Hour))
	idx.crash()

	idx = openIndex(t, s, idleOpts())
	require.True(t, idx.sweepDue)
	require.Equal(t, []string{"flushed"}, idx.Keys())
	requireTotals(t, idx, 1, 1)
}

func TestReopenSkipsWhatHasExpired(t *testing.T) {
	s := blobtest.NewMemStore()
	idx := openIndex(t, s, idleOpts())
	require.NoError(t, idx.Store("k", []byte("1"), time.Hour))
	o, _ := idx.Object("k")
	o.expiration.Store(time.Now().Add(-time.Second).UnixNano())
	idx.journal.add(o)
	require.NoError(t, idx.Close())
	idx = openIndex(t, s, idleOpts())
	requireTotals(t, idx, 0, 0)
}

func TestReopenTornJournal(t *testing.T) {
	s := blobtest.NewMemStore()
	idx := openIndex(t, s, idleOpts())
	require.NoError(t, idx.Store("first", []byte("1"), time.Hour))
	require.NoError(t, idx.Store("second", []byte("12"), time.Hour))
	require.NoError(t, idx.Close())
	name := generationName(journalPrefix, 1)
	b := readMeta(t, s, name)
	// the crash came partway through the write of the second record
	s.SetMeta(name, b[:recordOverhead+len("first")+10])

	idx = openIndex(t, s, idleOpts())
	require.True(t, idx.sweepDue, "a torn journal is missing changes")
	require.Equal(t, []string{"first"}, idx.Keys())
	// what comes after is kept apart from the torn journal
	require.NoError(t, idx.Store("third", []byte("123"), time.Hour))
	idx.flushOnce()
	require.Equal(t, []string{generationName(snapshotPrefix, 3)}, metaNames(t, s))
	idx.crash()

	idx = openIndex(t, s, idleOpts())
	keys := idx.Keys()
	slices.Sort(keys)
	require.Equal(t, []string{"first", "third"}, keys)
}

func TestReopenDamagedSnapshot(t *testing.T) {
	s := blobtest.NewMemStore()
	idx := openIndex(t, s, idleOpts())
	require.NoError(t, idx.Store("k", []byte("1"), time.Hour))
	require.NoError(t, idx.journal.compact(time.Now(), idx.each))
	require.NoError(t, idx.Store("later", []byte("1"), time.Hour))
	require.NoError(t, idx.Close())
	name := generationName(snapshotPrefix, 2)
	b := readMeta(t, s, name)
	b[len(b)-1] ^= 1
	s.SetMeta(name, b)

	idx = openIndex(t, s, idleOpts())
	require.True(t, idx.sweepDue)
	requireTotals(t, idx, 0, 0)
	// nothing that was persisted is trusted, and the first flush replaces all of it
	idx.flushOnce()
	require.Equal(t, []string{generationName(snapshotPrefix, 4)}, metaNames(t, s))
}

func TestReopenExpiredIndex(t *testing.T) {
	s := blobtest.NewMemStore()
	idx := openIndex(t, s, idleOpts())
	require.NoError(t, idx.Store("k", []byte("1"), 0))
	require.NoError(t, idx.Close())
	time.Sleep(2 * time.Millisecond)

	o := idleOpts()
	o.IndexExpiry = timeconv.Duration(time.Millisecond)
	idx = openIndex(t, s, o)
	require.True(t, idx.sweepDue)
	requireTotals(t, idx, 0, 0)
	idx.crash()

	o.IndexExpiry = 0
	idx = openIndex(t, s, o)
	requireTotals(t, idx, 1, 1)
}

func TestLoadFailures(t *testing.T) {
	s := blobtest.NewMemStore()
	idx := openIndex(t, s, idleOpts())
	require.NoError(t, idx.Store("k", []byte("1"), time.Hour))
	require.NoError(t, idx.Close())

	s.MetaErr = errFault
	idx = openIndex(t, s, idleOpts())
	require.True(t, idx.sweepDue)
	requireTotals(t, idx, 0, 0)
	s.MetaErr = nil

	for name, j := range map[string]*journal{
		"list":   {store: failingMeta{MemStore: s, list: true}},
		"open":   {store: failingMeta{MemStore: s, open: true}},
		"remove": {store: failingMeta{MemStore: s, remove: true}},
	} {
		t.Run(name, func(t *testing.T) {
			s.SetMeta(cleanMarker, nil)
			if name == "open" {
				require.NoError(t, j.store.AppendMeta(generationName(snapshotPrefix, 1), nil))
			}
			l, err := j.load(func(*record) {})
			require.ErrorIs(t, err, errFault)
			require.False(t, l.whole)
		})
	}
	// a journal that cannot be opened is passed over, as one torn at its start is
	require.NoError(t, s.RemoveMeta(generationName(snapshotPrefix, 1)))
	j := &journal{store: failingMeta{MemStore: s, open: true}}
	l, err := j.load(func(*record) {})
	require.NoError(t, err)
	require.False(t, l.whole)
}

// fails the calls it is told to, and passes the rest to its MemStore
type failingMeta struct {
	*blobtest.MemStore
	list, open, remove, create, appends bool
}

func (f failingMeta) ListMeta() ([]string, error) {
	if f.list {
		return nil, errFault
	}
	return f.MemStore.ListMeta()
}

func (f failingMeta) OpenMeta(name string) (io.ReadCloser, error) {
	if f.open {
		return nil, errFault
	}
	return f.MemStore.OpenMeta(name)
}

func (f failingMeta) RemoveMeta(names ...string) error {
	if f.remove {
		return errFault
	}
	return f.MemStore.RemoveMeta(names...)
}

func (f failingMeta) AppendMeta(name string, p []byte) error {
	if f.appends {
		return errFault
	}
	return f.MemStore.AppendMeta(name, p)
}

type failingWriter struct {
	io.WriteCloser
	write, closes bool
}

func (w failingWriter) Write(p []byte) (int, error) {
	if w.write {
		return 0, errFault
	}
	return w.WriteCloser.Write(p)
}

func (w failingWriter) Close() error {
	if w.closes {
		return errFault
	}
	return w.WriteCloser.Close()
}

func (f failingMeta) CreateMeta(name string) (io.WriteCloser, error) {
	w, err := f.MemStore.CreateMeta(name)
	return failingWriter{WriteCloser: w, write: f.create, closes: f.create}, err
}

func TestCompaction(t *testing.T) {
	s := blobtest.NewMemStore()
	idx := openIndex(t, s, idleOpts())
	compactions := metrics.CacheEvents.WithLabelValues(testName, testProvider, eventIndex, reasonCompaction)
	before := testutil.ToFloat64(compactions)
	value := make([]byte, 1)
	key := strings.Repeat("k", 1000)
	// the journal grows by the same few objects, written over and over
	for i := range 2 * compactionMinBytes / (recordOverhead + len(key) + 1) {
		require.NoError(t, idx.Store(key+strconv.Itoa(i%10), value, time.Hour))
	}
	require.NoError(t, idx.Store("brief", value, time.Second))
	require.NoError(t, idx.Store("expired", value, time.Hour))
	expired, _ := idx.Object("expired")
	expired.expiration.Store(1)

	idx.flushOnce()
	require.Equal(t, before+1, testutil.ToFloat64(compactions))
	name := generationName(snapshotPrefix, 2)
	require.Equal(t, []string{name}, metaNames(t, s), "the snapshot takes the place of the journal")
	got := records(readMeta(t, s, name))
	require.Len(t, got, 11, "a mark, and an object for each that is worth persisting")
	require.Equal(t, byte(opMark), got[0].op)
	require.Equal(t, idx.journal.snapshotBytes, int64(len(readMeta(t, s, name))))
	require.Zero(t, idx.journal.journalBytes)

	// the journal that follows the snapshot is of its generation
	require.NoError(t, idx.Store("after", value, time.Hour))
	idx.flushOnce()
	require.Equal(t, []string{name, generationName(journalPrefix, 2)}, metaNames(t, s))
	require.Equal(t, before+1, testutil.ToFloat64(compactions))
	idx.crash()

	idx = openIndex(t, s, idleOpts())
	requireTotals(t, idx, 11, 11)
}

// changes made while a snapshot is being written are in the journal that follows it
func TestCompactionKeepsConcurrentChanges(t *testing.T) {
	s := blobtest.NewMemStore()
	idx := openIndex(t, s, idleOpts())
	require.NoError(t, idx.Store("before", []byte("1"), time.Hour))
	require.NoError(t, idx.Store("removed", []byte("1"), time.Hour))
	idx.flushOnce()
	var during bool
	require.NoError(t, idx.journal.compact(time.Now(), func(yield func(o *Object) bool) {
		idx.each(func(o *Object) bool {
			if !during {
				during = true
				require.NoError(t, idx.Store("during", []byte("12"), time.Hour))
				require.NoError(t, idx.Remove("removed"))
			}
			return yield(o)
		})
	}))
	idx.flushOnce()
	idx.crash()

	idx = openIndex(t, s, idleOpts())
	keys := idx.Keys()
	slices.Sort(keys)
	require.Equal(t, []string{"before", "during"}, keys)
	requireTotals(t, idx, 2, 3)
}

func TestCompactionFailures(t *testing.T) {
	for name, fail := range map[string]failingMeta{
		"create": {}, "write": {create: true}, "list": {list: true}, "remove": {remove: true},
	} {
		t.Run(name, func(t *testing.T) {
			s := blobtest.NewMemStore()
			idx := openIndex(t, s, idleOpts())
			require.NoError(t, idx.Store("k", []byte("1"), time.Hour))
			idx.flushOnce()
			fail.MemStore = s
			if name == "create" {
				s.MetaErr = errFault
			} else {
				idx.journal.store = fail
			}
			idx.journal.markLossy()
			idx.flushOnce()
			s.MetaErr = nil
			idx.journal.store = s
			require.Contains(t, metaNames(t, s), generationName(journalPrefix, 1), "what stood before still stands")

			// the index is as it was for one that reopens it
			require.NoError(t, idx.Store("later", []byte("1"), time.Hour))
			idx.flushOnce()
			idx.crash()
			idx = openIndex(t, s, idleOpts())
			requireTotals(t, idx, 2, 2)
		})
	}
}

func TestFlushFailure(t *testing.T) {
	s := blobtest.NewMemStore()
	idx := openIndex(t, s, idleOpts())
	require.NoError(t, idx.Store("lost", []byte("1"), time.Hour))
	idx.journal.store = failingMeta{MemStore: s, appends: true}
	_, err := idx.journal.flush(time.Now())
	require.ErrorIs(t, err, errFault)
	require.True(t, idx.journal.lossy)
	require.Empty(t, metaNames(t, s))

	// the journal is missing the change, so a snapshot is written in its place
	idx.flushOnce()
	require.Equal(t, []string{generationName(snapshotPrefix, 2)}, metaNames(t, s))
	require.NotZero(t, idx.lastFlush.Load())
	idx.crash()
	idx = openIndex(t, s, idleOpts())
	requireTotals(t, idx, 1, 1)
}

func TestJournalBufferIsBounded(t *testing.T) {
	s := blobtest.NewMemStore()
	idx := openIndex(t, s, idleOpts())
	idx.journal.pending = make([]byte, maxJournalBuffer)
	require.NoError(t, idx.Store("k", []byte("1"), time.Hour))
	require.NoError(t, idx.Remove("k"))
	require.Len(t, idx.journal.pending, maxJournalBuffer)
	require.True(t, idx.journal.lossy)

	idx.journal.pending = nil
	idx.journal.recycle(make([]byte, 0, maxJournalBuffer+1))
	require.Nil(t, idx.journal.spare, "a buffer that grew too large is let go")
}

func TestCloseFailure(t *testing.T) {
	for name, fail := range map[string]failingMeta{"flush": {appends: true}, "mark": {create: true}} {
		t.Run(name, func(t *testing.T) {
			s := blobtest.NewMemStore()
			idx := openIndex(t, s, idleOpts())
			require.NoError(t, idx.Store("k", []byte("1"), time.Hour))
			fail.MemStore = s
			idx.journal.store = fail
			require.NoError(t, idx.Close(), "the cache is closed all the same")
			require.NotContains(t, metaNames(t, s), cleanMarker)
			require.Zero(t, idx.lastFlush.Load())
		})
	}
	s := blobtest.NewMemStore()
	s.MetaErr = errFault
	idx := openIndex(t, s, idleOpts())
	require.NoError(t, idx.Close())
}

func TestFlusherWorker(t *testing.T) {
	s := blobtest.NewMemStore()
	o := idleOpts()
	o.FlushInterval = timeconv.Duration(time.Millisecond)
	idx := openIndex(t, s, o)
	require.NoError(t, idx.Store("k", []byte("1"), time.Hour))
	require.Eventually(t, func() bool { return len(metaNames(t, s)) == 1 }, testTimeout, time.Millisecond)
	require.Eventually(t, func() bool { return len(idx.hasFlushed) == 1 }, testTimeout, time.Millisecond)
	idx.UpdateOptions(idleOpts())

	require.NoError(t, idx.Store("forced", []byte("1"), time.Hour))
	idx.forceFlush <- true
	require.Eventually(t, func() bool {
		return len(records(readMeta(t, s, generationName(journalPrefix, 1)))) == 4
	}, testTimeout, time.Millisecond)
	require.NoError(t, idx.Close())
	require.True(t, idx.flusherExited.Load())
}

// an index that lost changes is not marked clean at close until a snapshot has replaced its journal
func TestCloseCompactsALossyJournal(t *testing.T) {
	s := blobtest.NewMemStore()
	idx := openIndex(t, s, idleOpts())
	require.NoError(t, idx.Store("k", []byte("1"), time.Hour))
	idx.journal.markLossy()
	require.NoError(t, idx.Close())
	names := metaNames(t, s)
	require.Contains(t, names, cleanMarker)
	require.Contains(t, names, generationName(snapshotPrefix, 2))
	idx = openIndex(t, s, idleOpts())
	require.False(t, idx.sweepDue)
	requireTotals(t, idx, 1, 1)
	idx.crash()

	// a snapshot that cannot be written leaves no mark
	idx = openIndex(t, s, idleOpts())
	idx.journal.markLossy()
	idx.journal.store = failingMeta{MemStore: s, create: true}
	require.NoError(t, idx.Close())
	require.NotContains(t, metaNames(t, s), cleanMarker)
	idx = openIndex(t, s, idleOpts())
	require.True(t, idx.sweepDue)
}
