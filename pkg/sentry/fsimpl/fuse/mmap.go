// Copyright 2025 The gVisor Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package fuse

import (
	"io"

	"gvisor.dev/gvisor/pkg/abi/linux"
	"gvisor.dev/gvisor/pkg/context"
	"gvisor.dev/gvisor/pkg/errors/linuxerr"
	"gvisor.dev/gvisor/pkg/hostarch"
	"gvisor.dev/gvisor/pkg/safemem"
	"gvisor.dev/gvisor/pkg/sentry/memmap"
	"gvisor.dev/gvisor/pkg/sentry/pgalloc"
	"gvisor.dev/gvisor/pkg/sentry/usage"
)

// Memory-mapped regular files.
//
// Linux's FUSE supports mmap through the page cache: faults become FUSE_READ
// requests that fill kernel pages. This mirrors that with the sentry's page
// cache: inode.data holds pages allocated from the sentry MemoryFile, filled
// on fault by reads through the connection. The pages are sentry-owned (like
// tmpfs), so they — and the mappings — are checkpointable, which is what the
// S/R story requires.
//
// v1 scope (matching erofs' read-only model): private mappings of any
// permission and read-only shared mappings. Shared writable mappings are
// refused in ConfigureMMap, because writes through the mapping would need
// writeback (FUSE_WRITE on msync) that this doesn't implement. Writes through
// file descriptors bypass the cache and drop the affected pages, so a
// write-then-remap sequence (build a binary, exec it) observes fresh data.

// AddMapping implements memmap.Mappable.AddMapping.
func (i *inode) AddMapping(ctx context.Context, ms memmap.MappingSpace, ar hostarch.AddrRange, offset uint64, writable bool) error {
	i.mapsMu.Lock()
	i.mappings.AddMapping(ms, ar, offset, writable)
	i.mapsMu.Unlock()
	return nil
}

// RemoveMapping implements memmap.Mappable.RemoveMapping.
func (i *inode) RemoveMapping(ctx context.Context, ms memmap.MappingSpace, ar hostarch.AddrRange, offset uint64, writable bool) {
	i.mapsMu.Lock()
	i.mappings.RemoveMapping(ms, ar, offset, writable)
	i.mapsMu.Unlock()
}

// CopyMapping implements memmap.Mappable.CopyMapping.
func (i *inode) CopyMapping(ctx context.Context, ms memmap.MappingSpace, srcAR, dstAR hostarch.AddrRange, offset uint64, writable bool) error {
	i.AddMapping(ctx, ms, dstAR, offset, writable)
	return nil
}

// Translate implements memmap.Mappable.Translate.
func (i *inode) Translate(ctx context.Context, required, optional memmap.MappableRange, at hostarch.AccessType) ([]memmap.Translation, error) {
	if at.Write {
		// Shouldn't be possible: ConfigureMMap refuses shared writable
		// mappings, and private ones are COW'd by the mm.
		return nil, &memmap.BusError{Err: linuxerr.EROFS}
	}

	size := i.size.Load()
	pgend, _ := hostarch.PageRoundUp(size)
	var beyondEOF bool
	if required.End > pgend {
		if required.Start >= pgend {
			return nil, &memmap.BusError{Err: io.EOF}
		}
		beyondEOF = true
		required.End = pgend
	}
	if optional.End > pgend {
		optional.End = pgend
	}

	i.dataMu.Lock()
	defer i.dataMu.Unlock()

	// The reads that fill the pages go through a file handle the FUSE server
	// returned for this inode (Linux's page cache I/O similarly uses a
	// fuse_file's handle).
	if err := i.initMmapFhLocked(ctx); err != nil {
		return nil, &memmap.BusError{Err: err}
	}

	_, cerr := i.data.Fill(ctx, required, optional, size, i.fs.mf, pgalloc.AllocOpts{
		Kind:    usage.Anonymous,
		MemCgID: pgalloc.MemoryCgroupIDFromContext(ctx),
	}, i.readAtLocked)

	var ts []memmap.Translation
	var translatedEnd uint64
	for seg := i.data.FindSegment(required.Start); seg.Ok() && seg.Start() < required.End; seg, _ = seg.NextNonEmpty() {
		segMR := seg.Range().Intersect(optional)
		if segMR.Length() == 0 {
			segMR = seg.Range().Intersect(required)
		}
		ts = append(ts, memmap.Translation{
			Source: segMR,
			File:   i.fs.mf,
			Offset: seg.FileRangeOf(segMR).Start,
			Perms:  hostarch.ReadExecute,
		})
		translatedEnd = segMR.End
	}
	if translatedEnd < required.End && cerr != nil {
		return ts, &memmap.BusError{Err: cerr}
	}
	if beyondEOF {
		return ts, &memmap.BusError{Err: io.EOF}
	}
	return ts, nil
}

// InvalidateUnsavable implements memmap.Mappable.InvalidateUnsavable.
func (i *inode) InvalidateUnsavable(ctx context.Context) error {
	i.mapsMu.Lock()
	i.mappings.InvalidateAll(memmap.InvalidateOpts{})
	i.mapsMu.Unlock()
	return nil
}

// initMmapFhLocked opens (read-only) an internal file handle for page-cache
// reads if there isn't one yet. The handle is kept for the inode's lifetime;
// the FUSE server sees it like any other open file.
//
// +checklocks:i.dataMu
func (i *inode) initMmapFhLocked(ctx context.Context) error {
	if i.mmapFh != 0 || i.fs.conn.noOpen {
		return nil
	}
	in := linux.FUSEOpenIn{Flags: linux.O_RDONLY}
	out := linux.FUSEOpenOut{}
	if err := i.call(ctx, linux.FUSE_OPEN, &in, &out); err != nil {
		if linuxerr.Equals(linuxerr.ENOSYS, err) {
			// The server doesn't implement open; reads proceed without a
			// handle (Fh=0), as the kernel does when no_open is negotiated.
			i.fs.conn.noOpen = true
			return nil
		}
		return err
	}
	i.mmapFh = out.Fh
	return nil
}

// readAtLocked fills dsts with file contents starting at offset, issuing
// FUSE_READ requests capped at the connection's negotiated read size.
//
// +checklocks:i.dataMu
func (i *inode) readAtLocked(ctx context.Context, dsts safemem.BlockSeq, offset uint64) (uint64, error) {
	var total uint64
	for !dsts.IsEmpty() {
		size := dsts.NumBytes()
		if size > uint64(i.fs.conn.maxRead) {
			size = uint64(i.fs.conn.maxRead)
		}
		in := linux.FUSEReadIn{
			Fh:     i.mmapFh,
			Offset: offset + total,
			Size:   uint32(size),
		}
		res, err := i.callRaw(ctx, linux.FUSE_READ, &in)
		if err != nil {
			return total, err
		}
		if err := res.Error(); err != nil {
			return total, err
		}
		buf := res.data[res.hdr.SizeBytes():]
		if len(buf) == 0 {
			// EOF.
			return total, io.EOF
		}
		n, err := safemem.CopySeq(dsts, safemem.BlockSeqOf(safemem.BlockFromSafeSlice(buf)))
		total += n
		if err != nil {
			return total, err
		}
		if n < uint64(len(buf)) {
			// Short copy: dsts exhausted mid-reply.
			return total, nil
		}
		dsts = dsts.DropFirst64(n)
	}
	return total, nil
}

// dropCachedPages drops cached pages for [offset, offset+size), e.g. after a
// write through a file descriptor bypassed them.
func (i *inode) dropCachedPages(offset, size uint64) {
	if size == 0 {
		return
	}
	i.dataMu.Lock()
	i.data.Drop(memmap.MappableRange{Start: offset, End: offset + size}, i.fs.mf)
	i.dataMu.Unlock()
}
