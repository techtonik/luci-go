// Copyright 2015 The Chromium Authors. All rights reserved.
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package archiver

import (
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"os"
	"sync"
	"time"

	"github.com/luci/luci-go/client/internal/common"
	"github.com/luci/luci-go/client/isolatedclient"
	"github.com/luci/luci-go/common/isolated"
)

const (
	groupFound      common.Group   = 0
	groupFoundFound common.Section = 0

	groupHash     common.Group   = 1
	groupHashTodo common.Section = 0
	groupHashDone common.Section = 1

	groupLookup     common.Group   = 2
	groupLookupTodo common.Section = 0
	groupLookupDone common.Section = 1

	groupUpload     common.Group   = 3
	groupUploadTodo common.Section = 0
	groupUploadDone common.Section = 1
)

var headers = [][]string{
	{"found"},
	{"to hash", "hashed"},
	{"to lookup", "looked up"},
	{"to upload", "uploaded"},
}

// Future is the future of a file pushed to be hashed.
type Future interface {
	// DisplayName is the name associated to the content.
	DisplayName() string
	// WaitForHashed hangs until the item hash is known.
	WaitForHashed()
	// Error returns any error that occured for this item if any.
	Error() error
	// Digest returns the calculated digest once calculated, empty otherwise.
	Digest() isolated.HexDigest
}

// Archiver is an high level interface to an isolatedclient.IsolateServer.
type Archiver interface {
	common.Canceler
	Push(displayName string, src io.ReadSeeker) Future
	PushFile(displayName, path string) Future
	Stats() *Stats
}

// UploadStat is the statistic for a single upload.
type UploadStat struct {
	Duration time.Duration
	Size     common.Size
	Name     string
}

// Statistics from the Archiver.
type Stats struct {
	Hits   []common.Size // Bytes; each item is immutable.
	Pushed []*UploadStat // Misses; each item is immutable.
}

func (s *Stats) TotalHits() int {
	return len(s.Hits)
}

func (s *Stats) TotalBytesHits() common.Size {
	out := common.Size(0)
	for _, i := range s.Hits {
		out += i
	}
	return out
}

func (s *Stats) TotalMisses() int {
	return len(s.Pushed)
}

func (s *Stats) TotalBytesPushed() common.Size {
	out := common.Size(0)
	for _, i := range s.Pushed {
		out += i.Size
	}
	return out
}

func (s *Stats) deepCopy() *Stats {
	// Only need to copy the slice, not the items themselves.
	return &Stats{s.Hits, s.Pushed}
}

// New returns a thread-safe Archiver instance.
func New(is isolatedclient.IsolateServer, out io.Writer) Archiver {
	// TODO(maruel): Cache hashes and server cache presence.
	a := &archiver{
		canceler:              common.NewCanceler(),
		progress:              common.NewProgress(headers, out),
		is:                    is,
		maxConcurrentHash:     5,
		maxConcurrentContains: 64,
		maxConcurrentUpload:   8,
		containsBatchingDelay: 100 * time.Millisecond,
		containsBatchSize:     50,
		stage1DedupeChan:      make(chan *archiverItem, 1024),
		stage2HashChan:        make(chan *archiverItem, 10240),
		stage3LookupChan:      make(chan *archiverItem, 2048),
		stage4UploadChan:      make(chan *archiverItem, 2048),
	}

	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		a.stage1DedupeLoop()
	}()

	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		a.stage2HashLoop()
	}()

	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		a.stage3LookupLoop()
	}()

	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		a.stage4UploadLoop()
	}()
	return a
}

// Private details.

// archiverItem is an item to process. Implements Future.
//
// It is caried over from pipeline stage to stage to do processing on it.
type archiverItem struct {
	// Immutable.
	displayName string         // Name to use to qualify this item
	path        string         // Set when source is a file on disk
	wgHashed    sync.WaitGroup // Released once .digestItem.Digest is set

	// Mutable.
	lock       sync.Mutex
	err        error               // Item specific error
	digestItem isolated.DigestItem // Mutated by hashLoop(), used by doContains()
	linked     []*archiverItem     // Deduplicated item.

	// Mutable but not accessible externally.
	src   io.ReadSeeker             // Source of data
	state *isolatedclient.PushState // Server-side push state for cache miss
}

func newArchiverItem(displayName, path string, src io.ReadSeeker) *archiverItem {
	i := &archiverItem{displayName: displayName, path: path, src: src}
	i.wgHashed.Add(1)
	return i
}

func (i *archiverItem) DisplayName() string {
	return i.displayName
}

func (i *archiverItem) WaitForHashed() {
	i.wgHashed.Wait()
}

func (i *archiverItem) Error() error {
	i.lock.Lock()
	defer i.lock.Unlock()
	return i.err
}

func (i *archiverItem) Digest() isolated.HexDigest {
	i.lock.Lock()
	defer i.lock.Unlock()
	return i.digestItem.Digest
}

func (i *archiverItem) setErr(err error) {
	if err == nil {
		panic("internal error")
	}
	i.lock.Lock()
	defer i.lock.Unlock()
	if i.err == nil {
		i.err = err
		for _, child := range i.linked {
			child.lock.Lock()
			child.err = err
			child.lock.Unlock()
		}
	}
	// TODO(maruel): Support Close().
	i.src = nil
}

func (i *archiverItem) calcDigest() error {
	defer i.wgHashed.Done()
	var d isolated.DigestItem
	if i.path != "" {
		// Open and hash the file.
		var err error
		if d, err = isolated.HashFile(i.path); err != nil {
			i.setErr(err)
			return fmt.Errorf("hash(%s) failed: %s\n", i.DisplayName(), err)
		}
	} else {
		// Use src instead.
		h := isolated.GetHash()
		size, err := io.Copy(h, i.src)
		if err != nil {
			i.setErr(err)
			return fmt.Errorf("read(%s) failed: %s\n", i.DisplayName(), err)
		}
		if pos, err := i.src.Seek(0, os.SEEK_SET); err != nil || pos != 0 {
			err = fmt.Errorf("seek(%s) failed: %s\n", i.DisplayName(), err)
			i.setErr(err)
			return err
		}
		digest := isolated.HexDigest(hex.EncodeToString(h.Sum(nil)))
		d = isolated.DigestItem{digest, true, size}
	}
	i.lock.Lock()
	defer i.lock.Unlock()
	i.digestItem = d

	for _, child := range i.linked {
		child.lock.Lock()
		child.digestItem = d
		child.lock.Unlock()
		child.wgHashed.Done()
	}
	return nil
}

func (i *archiverItem) link(child *archiverItem) {
	child.lock.Lock()
	defer child.lock.Unlock()
	if child.src != nil || child.state != nil || child.linked != nil || child.err != nil {
		panic("internal error")
	}
	i.lock.Lock()
	defer i.lock.Unlock()
	i.linked = append(i.linked, child)
	if i.digestItem.Digest != "" {
		child.digestItem = i.digestItem
		child.err = i.err
		child.wgHashed.Done()
	} else if i.err != nil {
		child.err = i.err
		child.wgHashed.Done()
	}
}

// archiver archives content to an Isolate server.
//
// Uses a 4 stages pipeline, each doing work concurrently:
// - Deduplicating similar requests or known server hot cache hits.
// - Hashing files.
// - Batched cache hit lookups on the server.
// - Uploading cache misses.
type archiver struct {
	// Immutable.
	is                    isolatedclient.IsolateServer
	maxConcurrentHash     int           // Stage 2; Disk I/O bound.
	maxConcurrentContains int           // Stage 3; Server overload due to parallelism (DDoS).
	maxConcurrentUpload   int           // Stage 4; Network I/O bound.
	containsBatchingDelay time.Duration // Used by stage 3
	containsBatchSize     int           // Used by stage 3
	stage1DedupeChan      chan *archiverItem
	stage2HashChan        chan *archiverItem
	stage3LookupChan      chan *archiverItem
	stage4UploadChan      chan *archiverItem
	wg                    sync.WaitGroup
	canceler              common.Canceler
	progress              common.Progress

	// Mutable.
	lock  sync.Mutex
	stats Stats
}

// Close waits for all pending files to be done.
func (a *archiver) Close() error {
	// This is done so asynchronously calling push() won't crash.
	a.lock.Lock()
	close(a.stage1DedupeChan)
	a.stage1DedupeChan = nil
	a.lock.Unlock()

	a.wg.Wait()
	_ = a.progress.Close()
	_ = a.canceler.Close()
	return a.CancelationReason()
}

func (a *archiver) Cancel(reason error) {
	a.canceler.Cancel(reason)
}

func (a *archiver) CancelationReason() error {
	return a.canceler.CancelationReason()
}

func (a *archiver) Channel() <-chan error {
	return a.canceler.Channel()
}

func (a *archiver) Push(displayName string, src io.ReadSeeker) Future {
	i := newArchiverItem(displayName, "", src)
	if pos, err := i.src.Seek(0, os.SEEK_SET); pos != 0 || err != nil {
		i.setErr(fmt.Errorf("seek(%s) failed: %s\n", i.DisplayName(), err))
		i.wgHashed.Done()
		return i
	}
	return a.push(i)
}

func (a *archiver) PushFile(displayName, path string) Future {
	return a.push(newArchiverItem(displayName, path, nil))
}

func (a *archiver) Stats() *Stats {
	a.lock.Lock()
	defer a.lock.Unlock()
	return a.stats.deepCopy()
}

func (a *archiver) push(item *archiverItem) Future {
	// The close(a.stage1DedupeChan) call is always occuring with the lock held.
	a.lock.Lock()
	defer a.lock.Unlock()
	if a.stage1DedupeChan == nil {
		// Archiver was closed.
		return nil
	}
	a.stage1DedupeChan <- item
	a.progress.Update(groupFound, groupFoundFound, 1)
	return item
}

func (a *archiver) stage1DedupeLoop() {
	defer close(a.stage2HashChan)
	a.lock.Lock()
	stage1DedupeChan := a.stage1DedupeChan
	a.lock.Unlock()
	if stage1DedupeChan == nil {
		// This means Close() was called really too quickly.
		return
	}
	seen := map[string]*archiverItem{}
	for item := range stage1DedupeChan {
		if a.CancelationReason() != nil {
			item.setErr(a.CancelationReason())
			item.wgHashed.Done()
			continue
		}
		// TODO(todd): Create on-disk cache. This should be a separate interface
		// with its own implementation that 'archiver' keeps a reference to as
		// member.
		// TODO(maruel): Resolve symlinks for further deduplication? Depends on the
		// use case, not sure the trade off is worth.
		if item.path != "" {
			if previous, ok := seen[item.path]; ok {
				previous.link(item)
				continue
			}
		}
		a.progress.Update(groupHash, groupHashTodo, 1)
		a.stage2HashChan <- item
		seen[item.path] = item
	}
}

func (a *archiver) stage2HashLoop() {
	defer close(a.stage3LookupChan)
	pool := common.NewGoroutinePool(a.maxConcurrentContains, a.canceler)
	defer func() {
		_ = pool.Wait()
	}()
	for file := range a.stage2HashChan {
		item := file
		pool.Schedule(func() {
			if err := item.calcDigest(); err != nil {
				a.Cancel(err)
				return
			}
			a.progress.Update(groupHash, groupHashDone, 1)
			a.progress.Update(groupLookup, groupLookupTodo, 1)
			a.stage3LookupChan <- item
		}, func() {
			item.setErr(a.CancelationReason())
			item.wgHashed.Done()
		})
	}

}

func (a *archiver) stage3LookupLoop() {
	defer close(a.stage4UploadChan)
	pool := common.NewGoroutinePool(a.maxConcurrentContains, a.canceler)
	defer func() {
		_ = pool.Wait()
	}()
	items := []*archiverItem{}
	never := make(<-chan time.Time)
	timer := never
	loop := true
	for loop {
		select {
		case <-timer:
			batch := items
			pool.Schedule(func() {
				a.doContains(batch)
			}, nil)
			items = []*archiverItem{}
			timer = never

		case item, ok := <-a.stage3LookupChan:
			if !ok {
				loop = false
				break
			}
			items = append(items, item)
			if len(items) == a.containsBatchSize {
				batch := items
				pool.Schedule(func() {
					a.doContains(batch)
				}, nil)
				items = []*archiverItem{}
				timer = never
			} else if timer == never {
				timer = time.After(a.containsBatchingDelay)
			}
		}
	}

	if len(items) != 0 {
		batch := items
		pool.Schedule(func() {
			a.doContains(batch)
		}, nil)
	}
}

func (a *archiver) stage4UploadLoop() {
	pool := common.NewGoroutinePool(a.maxConcurrentUpload, a.canceler)
	defer func() {
		_ = pool.Wait()
	}()
	for state := range a.stage4UploadChan {
		item := state
		pool.Schedule(func() {
			a.doUpload(item)
		}, nil)
	}
}

// doContains is called by stage 3.
func (a *archiver) doContains(items []*archiverItem) {
	tmp := make([]*isolated.DigestItem, len(items))
	// No need to lock each item at that point, no mutation occurs on
	// archiverItem.digestItem after stage 2.
	for i, item := range items {
		tmp[i] = &item.digestItem
	}
	states, err := a.is.Contains(tmp)
	if err != nil {
		err = fmt.Errorf("contains(%d) failed: %s", len(items), err)
		a.Cancel(err)
		for _, item := range items {
			item.setErr(err)
		}
		return
	}
	a.progress.Update(groupLookup, groupLookupDone, len(items))
	for index, state := range states {
		size := items[index].digestItem.Size
		if state == nil {
			a.lock.Lock()
			a.stats.Hits = append(a.stats.Hits, common.Size(size))
			a.lock.Unlock()
		} else {
			items[index].state = state
			a.progress.Update(groupUpload, groupUploadTodo, 1)
			a.stage4UploadChan <- items[index]
		}
	}
	log.Printf("Looked up %d items\n", len(items))
}

// doUpload is called by stage 4.
func (a *archiver) doUpload(item *archiverItem) {
	var src io.Reader
	if item.src == nil {
		f, err := os.Open(item.path)
		if err != nil {
			a.Cancel(err)
			item.setErr(err)
			return
		}
		defer f.Close()
		src = f
	} else {
		src = item.src
		item.src = nil
	}
	start := time.Now()
	if err := a.is.Push(item.state, src); err != nil {
		err = fmt.Errorf("push(%s) failed: %s\n", item.path, err)
		a.Cancel(err)
		item.setErr(err)
	} else {
		a.progress.Update(groupUpload, groupUploadDone, 1)
	}
	size := common.Size(item.digestItem.Size)
	u := &UploadStat{time.Since(start), size, item.DisplayName()}
	a.lock.Lock()
	a.stats.Pushed = append(a.stats.Pushed, u)
	a.lock.Unlock()
	log.Printf("Uploaded %7s: %s\n", size, item.DisplayName())
}