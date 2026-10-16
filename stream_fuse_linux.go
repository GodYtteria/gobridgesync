//go:build linux

package main

import (
	"context"
	"errors"
	"hash/fnv"
	"io"
	"log"
	"path"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
	"google.golang.org/api/drive/v3"
)

const streamAttrTimeout = time.Second

type streamMount struct {
	snapshot func() *Remote
	srv      *drive.Service
	cache    *streamCache
	writer   *streamWriter
	tree     streamTree
	uid, gid uint32
}

type streamNode struct {
	fs.Inode
	m        *streamMount
	dir      bool
	file     RemoteFile
	servedMu sync.Mutex
	served   string
}

func (n *streamNode) cacheFlags(md5 string) uint32 {
	n.servedMu.Lock()
	defer n.servedMu.Unlock()
	prev := n.served
	n.served = md5
	if prev != "" && prev != md5 {
		return 0
	}
	return fuse.FOPEN_KEEP_CACHE
}

var (
	_ fs.NodeGetattrer = (*streamNode)(nil)
	_ fs.NodeSetattrer = (*streamNode)(nil)
	_ fs.NodeLookuper  = (*streamNode)(nil)
	_ fs.NodeReaddirer = (*streamNode)(nil)
	_ fs.NodeOpener    = (*streamNode)(nil)
	_ fs.NodeReader    = (*streamNode)(nil)
	_ fs.NodeCreater   = (*streamNode)(nil)
	_ fs.NodeMkdirer   = (*streamNode)(nil)
	_ fs.NodeUnlinker  = (*streamNode)(nil)
	_ fs.NodeRmdirer   = (*streamNode)(nil)
	_ fs.NodeRenamer   = (*streamNode)(nil)
)

const streamOpTimeout = 5 * time.Minute

func detached(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), streamOpTimeout)
}

func streamErrno(err error) syscall.Errno {
	switch {
	case err == nil:
		return 0
	case errors.Is(err, errStreamExists):
		return syscall.EEXIST
	case errors.Is(err, errStreamMissing):
		return syscall.ENOENT
	case errors.Is(err, errStreamNotEmpty):
		return syscall.ENOTEMPTY
	case errors.Is(err, errStreamIsDir):
		return syscall.EISDIR
	case errors.Is(err, errStreamNoSpace):
		return syscall.ENOSPC
	case errors.Is(err, errStreamStale):
		return syscall.ESTALE
	case errors.Is(err, errStreamBadName):
		return syscall.EINVAL
	case errors.Is(err, errStreamTooManyDeletes):
		return syscall.EPERM
	}
	log.Printf("streaming folder: %v", err)
	return syscall.EIO
}

var lastIOErrLog atomic.Int64

func ioErrno(action, p string, off int64, err error) syscall.Errno {
	if errors.Is(err, context.Canceled) {
		return syscall.EINTR
	}
	now := time.Now().UnixNano()
	if last := lastIOErrLog.Load(); now-last > int64(5*time.Second) && lastIOErrLog.CompareAndSwap(last, now) {
		log.Printf("streaming: %s %s at offset %d failed: %v", action, p, off, err)
	}
	return syscall.EIO
}

func (n *streamNode) remote() *Remote { return n.m.snapshot() }

func (n *streamNode) here() string { return n.Path(nil) }

func (n *streamNode) child(name string) string { return path.Join(n.here(), name) }

func (n *streamNode) fill(out *fuse.Attr, dir bool, size int64, mtime int64) {
	out.Uid, out.Gid = n.m.uid, n.m.gid
	if dir {
		out.Mode = syscall.S_IFDIR | 0o755
		return
	}
	out.Mode = syscall.S_IFREG | 0o644
	out.Size = uint64(size)
	out.Blocks = uint64((size + 511) / 512)
	t := time.Unix(0, mtime)
	out.SetTimes(&t, &t, &t)
}

func (n *streamNode) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	out.SetTimeout(streamAttrTimeout)
	if h, ok := f.(*streamHandle); ok && h.sf != nil {
		n.fill(&out.Attr, false, h.sf.size(), h.sf.modTime())
		return 0
	}
	if !n.dir {
		if sf := n.m.writer.lookupPending(n.here()); sf != nil {
			n.fill(&out.Attr, false, sf.size(), sf.modTime())
			return 0
		}
	}
	file := n.file
	if !n.dir {
		if r := n.remote(); r != nil {
			if cur, ok := r.Files[n.here()]; ok {
				file = cur
			}
		}
	}
	n.fill(&out.Attr, n.dir, file.Size, file.MTime)
	return 0
}

func (n *streamNode) Setattr(ctx context.Context, f fs.FileHandle, in *fuse.SetAttrIn, out *fuse.AttrOut) syscall.Errno {
	if size, ok := in.GetSize(); ok && !n.dir {
		if h, ok := f.(*streamHandle); ok && h.sf != nil {
			if err := h.sf.Truncate(int64(size)); err != nil {
				return ioErrno("truncating", n.here(), int64(size), err)
			}
		} else {
			sf, err := n.m.writer.OpenWrite(context.WithoutCancel(ctx), n.here(), size == 0)
			if err != nil {
				return streamErrno(err)
			}
			err = sf.Truncate(int64(size))
			n.m.writer.Release(sf, true)
			if err != nil {
				return ioErrno("truncating", n.here(), int64(size), err)
			}
		}
	}
	if mt, ok := in.GetMTime(); ok && !n.dir {
		if h, ok := f.(*streamHandle); ok && h.sf != nil {
			h.sf.setMTime(mt.UnixNano())
		} else {
			dctx, cancel := detached(ctx)
			err := n.m.writer.SetMTime(dctx, n.here(), mt.UnixNano())
			cancel()
			if err != nil {
				return streamErrno(err)
			}
		}
	}
	return n.Getattr(ctx, f, out)
}

func (n *streamNode) entry(ctx context.Context, name string, e treeEntry, out *fuse.EntryOut) *fs.Inode {
	child := &streamNode{m: n.m, dir: e.Dir, file: e.File}
	mode := uint32(syscall.S_IFREG)
	if e.Dir {
		mode = syscall.S_IFDIR
	}
	var a fuse.AttrOut
	child.Getattr(ctx, nil, &a)
	out.Attr = a.Attr
	out.SetEntryTimeout(streamAttrTimeout)
	out.SetAttrTimeout(streamAttrTimeout)
	return n.NewInode(ctx, child, fs.StableAttr{Mode: mode, Ino: n.inoFor(n.child(name), e)})
}

func (n *streamNode) inoFor(p string, e treeEntry) uint64 {
	key := "path:" + p
	if e.Dir {
		if r := n.remote(); r != nil {
			if id, ok := r.Folders[p]; ok {
				key = "id:" + id
			}
		}
	} else if e.File.ID != "" {
		key = "id:" + e.File.ID
	}
	return stableIno(key)
}

func stableIno(key string) uint64 {
	h := fnv.New64a()
	h.Write([]byte(key))
	ino := h.Sum64()
	if ino <= fuse.FUSE_ROOT_ID || ino == ^uint64(0) {
		ino = fuse.FUSE_ROOT_ID + 1
	}
	return ino
}

func (n *streamNode) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	r := n.remote()
	if r == nil {
		return nil, syscall.EIO
	}
	if sf := n.m.writer.lookupPending(n.child(name)); sf != nil {
		_, baseID, _, _ := sf.snapshot()
		return n.entry(ctx, name, treeEntry{Name: name, File: RemoteFile{ID: baseID, Size: sf.size(), MTime: sf.modTime()}}, out), 0
	}
	e, ok := n.m.tree.Lookup(r, n.here(), name)
	if !ok {
		return nil, syscall.ENOENT
	}
	return n.entry(ctx, name, e, out), 0
}

func (n *streamNode) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	r := n.remote()
	if r == nil {
		return nil, syscall.EIO
	}
	list, ok := n.m.tree.List(r, n.here())
	if !ok {
		return nil, syscall.ENOENT
	}
	seen := map[string]bool{}
	out := make([]fuse.DirEntry, 0, len(list))
	for _, e := range n.m.writer.PendingIn(n.here()) {
		seen[e.Name] = true
		out = append(out, fuse.DirEntry{Name: e.Name, Mode: syscall.S_IFREG})
	}
	for _, e := range list {
		if seen[e.Name] {
			continue
		}
		mode := uint32(syscall.S_IFREG)
		if e.Dir {
			mode = syscall.S_IFDIR
		}
		out = append(out, fuse.DirEntry{Name: e.Name, Mode: mode})
	}
	return fs.NewListDirStream(out), 0
}

func (n *streamNode) Open(ctx context.Context, flags uint32) (fs.FileHandle, uint32, syscall.Errno) {
	if n.dir {
		return nil, 0, syscall.EISDIR
	}
	if flags&(syscall.O_WRONLY|syscall.O_RDWR|syscall.O_APPEND|syscall.O_TRUNC) != 0 {
		sf, err := n.m.writer.OpenWrite(context.WithoutCancel(ctx), n.here(), flags&syscall.O_TRUNC != 0)
		if err != nil {
			return nil, 0, streamErrno(err)
		}
		return &streamHandle{m: n.m, sf: sf, write: true}, fuse.FOPEN_DIRECT_IO, 0
	}
	if sf := n.m.writer.acquire(n.here(), false); sf != nil {
		return &streamHandle{m: n.m, sf: sf}, fuse.FOPEN_DIRECT_IO, 0
	}
	r := n.remote()
	if r == nil {
		return nil, 0, syscall.EIO
	}
	cur, ok := r.Files[n.here()]
	if !ok {
		return nil, 0, syscall.ENOENT
	}
	return &streamHandle{m: n.m, path: n.here(), rf: cur}, n.cacheFlags(cur.MD5), 0
}

func (n *streamNode) Read(ctx context.Context, f fs.FileHandle, dest []byte, off int64) (fuse.ReadResult, syscall.Errno) {
	if h, ok := f.(*streamHandle); ok {
		return h.Read(ctx, dest, off)
	}
	r := n.remote()
	if r == nil {
		return nil, syscall.EIO
	}
	cur, ok := r.Files[n.here()]
	if !ok || cur.ID != n.file.ID || cur.MD5 != n.file.MD5 || cur.Size != n.file.Size {
		return nil, syscall.ESTALE
	}
	got, err := n.m.cache.ReadAt(ctx, cur.ID, cur.MD5, cur.Size, dest, off, driveRangeFetcher(n.m.srv, cur.ID))
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, ioErrno("reading", n.here(), off, err)
	}
	return fuse.ReadResultData(dest[:got]), 0
}

func (n *streamNode) Create(ctx context.Context, name string, flags uint32, mode uint32, out *fuse.EntryOut) (*fs.Inode, fs.FileHandle, uint32, syscall.Errno) {
	sf, err := n.m.writer.Create(n.child(name))
	if err != nil {
		return nil, nil, 0, streamErrno(err)
	}
	return n.entry(ctx, name, treeEntry{Name: name, File: RemoteFile{MTime: time.Now().UnixNano()}}, out), &streamHandle{m: n.m, sf: sf, write: true}, fuse.FOPEN_DIRECT_IO, 0
}

func (n *streamNode) Mkdir(ctx context.Context, name string, mode uint32, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	ctx, cancel := detached(ctx)
	defer cancel()
	if err := n.m.writer.Mkdir(ctx, n.child(name)); err != nil {
		return nil, streamErrno(err)
	}
	return n.entry(ctx, name, treeEntry{Name: name, Dir: true}, out), 0
}

func (n *streamNode) Unlink(ctx context.Context, name string) syscall.Errno {
	ctx, cancel := detached(ctx)
	defer cancel()
	return streamErrno(n.m.writer.Remove(ctx, n.child(name)))
}

func (n *streamNode) Rmdir(ctx context.Context, name string) syscall.Errno {
	ctx, cancel := detached(ctx)
	defer cancel()
	return streamErrno(n.m.writer.Rmdir(ctx, n.child(name)))
}

func (n *streamNode) Rename(ctx context.Context, name string, newParent fs.InodeEmbedder, newName string, flags uint32) syscall.Errno {
	np, ok := newParent.(*streamNode)
	if !ok || flags != 0 {
		return syscall.EINVAL
	}
	ctx, cancel := detached(ctx)
	defer cancel()
	from, to := n.child(name), np.child(newName)
	err := n.m.writer.Rename(ctx, from, to)
	if err == nil {
		log.Printf("streaming: renamed %s to %s", from, to)
	}
	return streamErrno(err)
}

type streamHandle struct {
	m     *streamMount
	write bool
	sf    *stageFile
	path  string
	rf    RemoteFile
}

var (
	_ fs.FileReader    = (*streamHandle)(nil)
	_ fs.FileWriter    = (*streamHandle)(nil)
	_ fs.FileReleaser  = (*streamHandle)(nil)
	_ fs.FileFlusher   = (*streamHandle)(nil)
	_ fs.FileFsyncer   = (*streamHandle)(nil)
	_ fs.FileGetattrer = (*streamHandle)(nil)
)

func (h *streamHandle) Read(ctx context.Context, dest []byte, off int64) (fuse.ReadResult, syscall.Errno) {
	if h.sf == nil {
		r := h.m.snapshot()
		if r == nil {
			return nil, syscall.EIO
		}
		cur, ok := r.Files[h.path]
		if !ok || cur.ID != h.rf.ID || cur.MD5 != h.rf.MD5 || cur.Size != h.rf.Size {
			return nil, syscall.ESTALE
		}
		got, err := h.m.cache.ReadAt(ctx, cur.ID, cur.MD5, cur.Size, dest, off, driveRangeFetcher(h.m.srv, cur.ID))
		if err != nil && !errors.Is(err, io.EOF) {
			return nil, ioErrno("reading", h.path, off, err)
		}
		return fuse.ReadResultData(dest[:got]), 0
	}
	n, err := h.sf.ReadAt(dest, off)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, ioErrno("reading the staged copy of", h.sf.path, off, err)
	}
	return fuse.ReadResultData(dest[:n]), 0
}

func (h *streamHandle) Write(ctx context.Context, data []byte, off int64) (uint32, syscall.Errno) {
	if h.sf == nil {
		return 0, syscall.EBADF
	}
	n, err := h.sf.WriteAt(data, off)
	if err != nil {
		if errors.Is(err, syscall.ENOSPC) {
			log.Printf("streaming: no space left to stage %s", h.sf.path)
			return uint32(n), syscall.ENOSPC
		}
		return uint32(n), ioErrno("writing", h.sf.path, off, err)
	}
	return uint32(n), 0
}

func (h *streamHandle) Flush(ctx context.Context) syscall.Errno { return 0 }

func (h *streamHandle) Fsync(ctx context.Context, flags uint32) syscall.Errno {
	if h.sf == nil {
		return 0
	}
	if err := h.sf.f.Sync(); err != nil {
		return ioErrno("syncing", h.sf.path, 0, err)
	}
	return 0
}

func (h *streamHandle) Release(ctx context.Context) syscall.Errno {
	if h.sf != nil {
		h.m.writer.Release(h.sf, h.write)
	}
	return 0
}

func (h *streamHandle) Getattr(ctx context.Context, out *fuse.AttrOut) syscall.Errno {
	out.Uid, out.Gid = h.m.uid, h.m.gid
	out.Mode = syscall.S_IFREG | 0o644
	if h.sf != nil {
		out.Size = uint64(h.sf.size())
	} else {
		out.Size = uint64(h.rf.Size)
	}
	return 0
}

func mountStream(dir string, m *streamMount) (*fuse.Server, error) {
	root := &streamNode{m: m, dir: true}
	return fs.Mount(dir, root, &fs.Options{MountOptions: fuse.MountOptions{
		FsName:       "gobridgesync",
		Name:         "gobridgesync",
		MaxWrite:     1 << 20,
		MaxReadAhead: 1 << 20,
	}})
}
