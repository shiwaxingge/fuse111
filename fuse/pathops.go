// Translation of raw operation to path based operations.

package fuse

import (
	"bytes"
	"fmt"
	"log"
	"path/filepath"
	"time"
)

var _ = fmt.Println

func NewFileSystemConnector(fs FileSystem, opts *FileSystemOptions) (me *FileSystemConnector) {
	me = new(FileSystemConnector)
	if opts == nil {
		opts = NewFileSystemOptions()
	}
	me.inodeMap = NewHandleMap(!opts.SkipCheckHandles)
	me.rootNode = me.newInode(true)
	me.rootNode.NodeId = FUSE_ROOT_ID
	me.verify()
	me.mountRoot(fs, opts)
	return me
}

func (me *FileSystemConnector) GetPath(nodeid uint64) (path string, mount *fileSystemMount, node *inode) {
	n := me.getInodeData(nodeid)

	p, m := n.GetPath()
	if me.Debug {
		log.Printf("Node %v = '%s'", nodeid, n.GetFullPath())
	}

	return p, m, n
}

func (me *fileSystemMount) setOwner(attr *Attr) {
	if me.options.Owner != nil {
		attr.Owner = *me.options.Owner
	}
}

func (me *FileSystemConnector) Lookup(header *InHeader, name string) (out *EntryOut, status Status) {
	parent := me.getInodeData(header.NodeId)
	if me.Debug {
		log.Printf("Node %v = '%s'", parent.NodeId, parent.GetFullPath())
	}
	return me.internalLookup(parent, name, 1, &header.Context)
}

func (me *FileSystemConnector) internalLookup(parent *inode, name string, lookupCount int, context *Context) (out *EntryOut, status Status) {
	out, status, _ = me.internalLookupWithNode(parent, name, lookupCount, context)
	return out, status
}

func (me *FileSystemConnector) internalLookupWithNode(parent *inode, name string, lookupCount int, context *Context) (out *EntryOut, status Status, node *inode) {
	fullPath, mount, isMountPoint := me.lookupMount(parent, name, lookupCount)
	if isMountPoint {
		node = mount.mountInode
	} else {
		fullPath, mount = parent.GetPath()
		fullPath = filepath.Join(fullPath, name)
	}

	if mount == nil {
		fmt.Println(me.rootNode)
		fmt.Println(me.rootNode.mountPoint)
		timeout := me.rootNode.mountPoint.options.NegativeTimeout
		if timeout > 0 {
			return NegativeEntry(timeout), OK, nil
		} else {
			return nil, ENOENT, nil
		}
	}
	fi, err := mount.fs.GetAttr(fullPath, context)
	if err == ENOENT && mount.options.NegativeTimeout > 0.0 {
		return NegativeEntry(mount.options.NegativeTimeout), OK, nil
	}

	if err != OK {
		return nil, err, nil
	}
	if !isMountPoint {
		node = me.lookupUpdate(parent, name, fi.IsDirectory(), lookupCount)
	}

	out = &EntryOut{
		NodeId:     node.NodeId,
		Generation: 1, // where to get the generation?
	}
	SplitNs(mount.options.EntryTimeout, &out.EntryValid, &out.EntryValidNsec)
	SplitNs(mount.options.AttrTimeout, &out.AttrValid, &out.AttrValidNsec)
	if !fi.IsDirectory() {
		fi.Nlink = 1
	}

	CopyFileInfo(fi, &out.Attr)
	out.Attr.Ino = node.NodeId
	mount.setOwner(&out.Attr)

	return out, OK, node
}

func (me *FileSystemConnector) Forget(h *InHeader, input *ForgetIn) {
	me.forgetUpdate(h.NodeId, int(input.Nlookup))
}

func (me *FileSystemConnector) GetAttr(header *InHeader, input *GetAttrIn) (out *AttrOut, code Status) {
	fh := uint64(0)
	if input.Flags&FUSE_GETATTR_FH != 0 {
		fh = input.Fh
	}

	f, mount, fullPath, node := me.getOpenFileData(header.NodeId, fh)
	if mount == nil && f == nil {
		return nil, ENOENT
	}

	if f != nil {
		fi, err := f.GetAttr()
		if err != OK && err != ENOSYS {
			return nil, err
		}

		if fi != nil {
			out = &AttrOut{}
			CopyFileInfo(fi, &out.Attr)
			out.Attr.Ino = header.NodeId
			SplitNs(node.mount.options.AttrTimeout, &out.AttrValid, &out.AttrValidNsec)

			return out, OK
		}
	}

	if mount == nil {
		return nil, ENOENT
	}

	fi, err := mount.fs.GetAttr(fullPath, &header.Context)
	if err != OK {
		return nil, err
	}

	out = &AttrOut{}
	CopyFileInfo(fi, &out.Attr)
	out.Attr.Ino = header.NodeId

	if !fi.IsDirectory() {
		out.Nlink = 1
	}
	mount.setOwner(&out.Attr)
	SplitNs(mount.options.AttrTimeout, &out.AttrValid, &out.AttrValidNsec)
	return out, OK
}

func (me *FileSystemConnector) OpenDir(header *InHeader, input *OpenIn) (flags uint32, handle uint64, status Status) {
	fullPath, mount, node := me.GetPath(header.NodeId)
	if mount == nil {
		return 0, 0, ENOENT
	}
	// TODO - how to handle return flags, the FUSE open flags?
	stream, err := mount.fs.OpenDir(fullPath, &header.Context)
	if err != OK {
		return 0, 0, err
	}

	de := &connectorDir{
		extra:  node.GetMountDirEntries(),
		stream: stream,
	}
	h, opened := mount.registerFileHandle(node, de, nil, input.Flags)

	return opened.FuseFlags, h, OK
}

func (me *FileSystemConnector) ReadDir(header *InHeader, input *ReadIn) (*DirEntryList, Status) {
	opened := me.getOpenedFile(input.Fh)
	de, code := opened.dir.ReadDir(input)
	if code != OK {
		return nil, code
	}
	return de, OK
}

func (me *FileSystemConnector) Open(header *InHeader, input *OpenIn) (flags uint32, handle uint64, status Status) {
	fullPath, mount, node := me.GetPath(header.NodeId)
	if mount == nil {
		return 0, 0, ENOENT
	}

	f, err := mount.fs.Open(fullPath, input.Flags, &header.Context)
	if err != OK {
		return 0, 0, err
	}

	h, opened := mount.registerFileHandle(node, nil, f, input.Flags)

	return opened.FuseFlags, h, OK
}

func (me *FileSystemConnector) SetAttr(header *InHeader, input *SetAttrIn) (out *AttrOut, code Status) {
	var err Status = OK
	var getAttrIn GetAttrIn
	fh := uint64(0)
	if input.Valid&FATTR_FH != 0 {
		fh = input.Fh
		getAttrIn.Fh = fh
		getAttrIn.Flags |= FUSE_GETATTR_FH
	}

	f, mount, fullPath, _ := me.getOpenFileData(header.NodeId, fh)
	if mount == nil {
		return nil, ENOENT
	}

	fileResult := ENOSYS
	if err.Ok() && input.Valid&FATTR_MODE != 0 {
		permissions := uint32(07777) & input.Mode
		if f != nil {
			fileResult = f.Chmod(permissions)
		}
		if fileResult == ENOSYS {
			err = mount.fs.Chmod(fullPath, permissions, &header.Context)
		} else {
			err = fileResult
			fileResult = ENOSYS
		}
	}
	if err.Ok() && (input.Valid&(FATTR_UID|FATTR_GID) != 0) {
		if f != nil {
			fileResult = f.Chown(uint32(input.Uid), uint32(input.Gid))
		}

		if fileResult == ENOSYS {
			// TODO - can we get just FATTR_GID but not FATTR_UID ?
			err = mount.fs.Chown(fullPath, uint32(input.Uid), uint32(input.Gid), &header.Context)
		} else {
			err = fileResult
			fileResult = ENOSYS
		}
	}
	if err.Ok() && input.Valid&FATTR_SIZE != 0 {
		if f != nil {
			fileResult = f.Truncate(input.Size)
		}
		if fileResult == ENOSYS {
			err = mount.fs.Truncate(fullPath, input.Size, &header.Context)
		} else {
			err = fileResult
			fileResult = ENOSYS
		}
	}
	if err.Ok() && (input.Valid&(FATTR_ATIME|FATTR_MTIME|FATTR_ATIME_NOW|FATTR_MTIME_NOW) != 0) {
		atime := uint64(input.Atime*1e9) + uint64(input.Atimensec)
		if input.Valid&FATTR_ATIME_NOW != 0 {
			atime = uint64(time.Nanoseconds())
		}

		mtime := uint64(input.Mtime*1e9) + uint64(input.Mtimensec)
		if input.Valid&FATTR_MTIME_NOW != 0 {
			mtime = uint64(time.Nanoseconds())
		}

		if f != nil {
			fileResult = f.Utimens(atime, mtime)
		}
		if fileResult == ENOSYS {
			err = mount.fs.Utimens(fullPath, atime, mtime, &header.Context)
		} else {
			err = fileResult
			fileResult = ENOSYS
		}
	}
	if err != OK {
		return nil, err
	}

	return me.GetAttr(header, &getAttrIn)
}

func (me *FileSystemConnector) Readlink(header *InHeader) (out []byte, code Status) {
	fullPath, mount, _ := me.GetPath(header.NodeId)
	if mount == nil {
		return nil, ENOENT
	}
	val, err := mount.fs.Readlink(fullPath, &header.Context)
	return bytes.NewBufferString(val).Bytes(), err
}

func (me *FileSystemConnector) Mknod(header *InHeader, input *MknodIn, name string) (out *EntryOut, code Status) {
	fullPath, mount, node := me.GetPath(header.NodeId)
	if mount == nil {
		return nil, ENOENT
	}
	fullPath = filepath.Join(fullPath, name)
	err := mount.fs.Mknod(fullPath, input.Mode, uint32(input.Rdev), &header.Context)
	if err != OK {
		return nil, err
	}
	return me.internalLookup(node, name, 1, &header.Context)
}

func (me *FileSystemConnector) Mkdir(header *InHeader, input *MkdirIn, name string) (out *EntryOut, code Status) {
	fullPath, mount, parent := me.GetPath(header.NodeId)
	if mount == nil {
		return nil, ENOENT
	}
	code = mount.fs.Mkdir(filepath.Join(fullPath, name), input.Mode, &header.Context)
	if code.Ok() {
		out, code = me.internalLookup(parent, name, 1, &header.Context)
	}
	return out, code
}

func (me *FileSystemConnector) Unlink(header *InHeader, name string) (code Status) {
	fullPath, mount, parent := me.GetPath(header.NodeId)
	if mount == nil {
		return ENOENT
	}
	code = mount.fs.Unlink(filepath.Join(fullPath, name), &header.Context)
	if code.Ok() {
		// Like fuse.c, we update our internal tables.
		me.unlinkUpdate(parent, name)
	}
	return code
}

func (me *FileSystemConnector) Rmdir(header *InHeader, name string) (code Status) {
	fullPath, mount, parent := me.GetPath(header.NodeId)
	if mount == nil {
		return ENOENT
	}
	code = mount.fs.Rmdir(filepath.Join(fullPath, name), &header.Context)
	if code.Ok() {
		me.unlinkUpdate(parent, name)
	}
	return code
}

func (me *FileSystemConnector) Symlink(header *InHeader, pointedTo string, linkName string) (out *EntryOut, code Status) {
	fullPath, mount, parent := me.GetPath(header.NodeId)
	if mount == nil {
		return nil, ENOENT
	}
	err := mount.fs.Symlink(pointedTo, filepath.Join(fullPath, linkName), &header.Context)
	if err != OK {
		return nil, err
	}

	out, code = me.internalLookup(parent, linkName, 1, &header.Context)
	return out, code
}

func (me *FileSystemConnector) Rename(header *InHeader, input *RenameIn, oldName string, newName string) (code Status) {
	oldPath, oldMount, oldParent := me.GetPath(header.NodeId)
	newPath, mount, newParent := me.GetPath(input.Newdir)
	if mount == nil || oldMount == nil {
		return ENOENT
	}
	_, _, isMountPoint := me.lookupMount(oldParent, oldName, 0)
	if isMountPoint {
		return EBUSY
	}
	if mount != oldMount {
		return EXDEV
	}

	oldPath = filepath.Join(oldPath, oldName)
	newPath = filepath.Join(newPath, newName)
	code = mount.fs.Rename(oldPath, newPath, &header.Context)
	if code.Ok() {
		me.renameUpdate(oldParent, oldName, newParent, newName)
	}
	return code
}

func (me *FileSystemConnector) Link(header *InHeader, input *LinkIn, filename string) (out *EntryOut, code Status) {
	orig, mount, _ := me.GetPath(input.Oldnodeid)
	newName, newMount, newParent := me.GetPath(header.NodeId)

	if mount == nil || newMount == nil {
		return nil, ENOENT
	}
	if mount != newMount {
		return nil, EXDEV
	}
	newName = filepath.Join(newName, filename)
	err := mount.fs.Link(orig, newName, &header.Context)

	if err != OK {
		return nil, err
	}

	return me.internalLookup(newParent, filename, 1, &header.Context)
}

func (me *FileSystemConnector) Access(header *InHeader, input *AccessIn) (code Status) {
	p, mount, _ := me.GetPath(header.NodeId)
	if mount == nil {
		return ENOENT
	}
	return mount.fs.Access(p, input.Mask, &header.Context)
}

func (me *FileSystemConnector) Create(header *InHeader, input *CreateIn, name string) (flags uint32, h uint64, out *EntryOut, code Status) {
	directory, mount, parent := me.GetPath(header.NodeId)
	if mount == nil {
		return 0, 0, nil, ENOENT
	}
	fullPath := filepath.Join(directory, name)

	f, err := mount.fs.Create(fullPath, uint32(input.Flags), input.Mode, &header.Context)
	if err != OK {
		return 0, 0, nil, err
	}

	out, code, inode := me.internalLookupWithNode(parent, name, 1, &header.Context)
	if inode == nil {
		msg := fmt.Sprintf("Create succeded, but GetAttr returned no entry %v. code %v", fullPath, code)
		panic(msg)
	}
	handle, opened := mount.registerFileHandle(inode, nil, f, input.Flags)
	return opened.FuseFlags, handle, out, code
}

func (me *FileSystemConnector) Release(header *InHeader, input *ReleaseIn) {
	node := me.getInodeData(header.NodeId)
	opened := node.mount.unregisterFileHandle(node, input.Fh)
	opened.file.Release()
}

func (me *FileSystemConnector) Flush(input *FlushIn) Status {
	opened := me.getOpenedFile(input.Fh)

	code := opened.file.Flush()
	if code.Ok() && opened.OpenFlags&O_ANYWRITE != 0 {
		// We only signal releases to the FS if the
		// open could have changed things.
		var path string
		var mount *fileSystemMount
		path, mount = opened.inode.GetPath()

		if mount != nil {
			code = mount.fs.Flush(path)
		}
	}
	return code
}

func (me *FileSystemConnector) ReleaseDir(header *InHeader, input *ReleaseIn) {
	node := me.getInodeData(header.NodeId)
	opened := node.mount.unregisterFileHandle(node, input.Fh)
	opened.dir.Release()
	me.considerDropInode(node)
}

func (me *FileSystemConnector) FsyncDir(header *InHeader, input *FsyncIn) (code Status) {
	// What the heck is FsyncDir supposed to do?
	return OK
}

func (me *FileSystemConnector) GetXAttr(header *InHeader, attribute string) (data []byte, code Status) {
	path, mount, _ := me.GetPath(header.NodeId)
	if mount == nil {
		return nil, ENOENT
	}

	data, code = mount.fs.GetXAttr(path, attribute, &header.Context)
	return data, code
}

func (me *FileSystemConnector) RemoveXAttr(header *InHeader, attr string) Status {
	path, mount, _ := me.GetPath(header.NodeId)
	if mount == nil {
		return ENOENT
	}

	return mount.fs.RemoveXAttr(path, attr, &header.Context)
}

func (me *FileSystemConnector) SetXAttr(header *InHeader, input *SetXAttrIn, attr string, data []byte) Status {
	path, mount, _ := me.GetPath(header.NodeId)
	if mount == nil {
		return ENOENT
	}

	return mount.fs.SetXAttr(path, attr, data, int(input.Flags), &header.Context)
}

func (me *FileSystemConnector) ListXAttr(header *InHeader) (data []byte, code Status) {
	path, mount, _ := me.GetPath(header.NodeId)
	if mount == nil {
		return nil, ENOENT
	}

	attrs, code := mount.fs.ListXAttr(path, &header.Context)
	if code != OK {
		return nil, code
	}

	b := bytes.NewBuffer([]byte{})
	for _, v := range attrs {
		b.Write([]byte(v))
		b.WriteByte(0)
	}

	return b.Bytes(), code
}

func (me *FileSystemConnector) fileDebug(fh uint64, n *inode) {
	p, _, _ := me.GetPath(n.NodeId)
	log.Printf("Fh %d = %s", fh, p)
}

func (me *FileSystemConnector) Write(input *WriteIn, data []byte) (written uint32, code Status) {
	opened := me.getOpenedFile(input.Fh)
	if me.Debug {
		me.fileDebug(input.Fh, opened.inode)
	}
	return opened.file.Write(input, data)
}

func (me *FileSystemConnector) Read(input *ReadIn, bp BufferPool) ([]byte, Status) {
	opened := me.getOpenedFile(input.Fh)
	if me.Debug {
		me.fileDebug(input.Fh, opened.inode)
	}
	return opened.file.Read(input, bp)
}

func (me *FileSystemConnector) Ioctl(header *InHeader, input *IoctlIn) (out *IoctlOut, data []byte, code Status) {
	opened := me.getOpenedFile(input.Fh)
	if me.Debug {
		me.fileDebug(input.Fh, opened.inode)
	}
	return opened.file.Ioctl(input)
}

func (me *FileSystemConnector) StatFs() *StatfsOut {
	return me.rootNode.mountPoint.fs.StatFs()
}
