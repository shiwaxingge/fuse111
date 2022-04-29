package fuse

import (
	"bytes"
	"fmt"
	"log"
	"path/filepath"
	"strings"
	"sync"
)

type mountData struct {
	// If non-nil the file system mounted here.
	fs PathFilesystem

	// Protects the variables below.
	mutex sync.RWMutex

	// If yes, we are looking to unmount the mounted fs.
	unmountPending bool

	// Count files, dirs and mounts.
	openCount int
}

func (me *mountData) incOpenCount(delta int) {
	me.mutex.Lock()
	defer me.mutex.Unlock()
	me.openCount += delta
}

func newMount(fs PathFilesystem) *mountData {
	return &mountData{fs: fs}
}

// Tests should set to true.
var paranoia = false

// TODO should rename to dentry?
type inodeData struct {
	Parent      *inodeData
	NodeId      uint64
	Name        string
	LookupCount int

	Type uint32

	// Number of inodeData that have this as parent.
	RefCount int

	mount *mountData
}

func (me *inodeData) verify(c *PathFileSystemConnector) {
	if me.Parent != nil {
		k := inodeDataKey(me.Parent.NodeId, me.Name)
		n := c.lookup(k)
		if n != me {
			panic(fmt.Sprintf("parent/child relation corrupted %v %v %v",
				k, n, me))
		}
	} else {
		// may both happen for deleted and root node.
	}
}

// Should implement some hash table method instead?
func inodeDataKey(parentInode uint64, name string) string {
	return string(parentInode) + ":" + name
}

func (me *inodeData) Key() string {
	var p uint64 = 0
	if me.Parent != nil {
		p = me.Parent.NodeId
	}
	return inodeDataKey(p, me.Name)
}

func (me *inodeData) GetPath() (path string, mount *mountData) {
	rev_components := make([]string, 0, 10)
	inode := me

	for ; inode != nil && inode.mount == nil; inode = inode.Parent {
		rev_components = append(rev_components, inode.Name)
	}
	if inode == nil {
		panic("did not find parent with mount")
	}
	mount = inode.mount

	mount.mutex.RLock()
	defer mount.mutex.RUnlock()
	if mount.unmountPending {
		return "", nil
	}
	components := make([]string, len(rev_components))
	for i, v := range rev_components {
		components[len(rev_components) - i -1] = v
	}
	fullPath := strings.Join(components, "/")
	return fullPath, mount
}

type TimeoutOptions struct {
	EntryTimeout    float64
	AttrTimeout     float64
	NegativeTimeout float64
}

func MakeTimeoutOptions() TimeoutOptions {
	return TimeoutOptions{
		NegativeTimeout: 0.0,
		AttrTimeout:     1.0,
		EntryTimeout:    1.0,
	}
}

type PathFileSystemConnectorOptions struct {
	TimeoutOptions
}

type PathFileSystemConnector struct {
	DefaultRawFuseFileSystem
	
	// Protects the hashmap, its contents and the nextFreeInode counter.
	lock sync.RWMutex

	// Invariants
	// - For all values, (RefCount > 0 || LookupCount > 0).
	// - For all values, value = inodePathMap[value.Key()]
	// - For all values, value = inodePathMapByInode[value.NodeId]

	// fuse.c seems to have different lifetimes for the different
	// hashtables, which could lead to the same directory entry
	// existing twice with different generated inode numbers, if
	// we have (FORGET, LOOKUP) on a directory entry with RefCount
	// > 0.
	inodePathMap        map[string]*inodeData
	inodePathMapByInode map[uint64]*inodeData
	nextFreeInode       uint64

	options PathFileSystemConnectorOptions
	Debug   bool
}

func (me *PathFileSystemConnector) verify() {
	if !paranoia {
		return
	}
	for k, v := range me.inodePathMapByInode {
		if v.NodeId != k {
			panic(fmt.Sprintf("Nodeid mismatch", k, v.NodeId, v))
		}
		v.verify(me)
	}
}

// Must be called with lock held.
func (me *PathFileSystemConnector) setParent(data *inodeData, newParent *inodeData) {
	if data.Parent == newParent {
		return
	}

	if newParent == nil {
		panic("Unknown parent")
	}

	oldParent := data.Parent
	if oldParent != nil {
		me.unrefNode(oldParent)
	}
	data.Parent = newParent
	if newParent != nil {
		newParent.RefCount++
	}
}

// Must be called with lock held.
func (me *PathFileSystemConnector) unrefNode(data *inodeData) {
	data.RefCount--
	if data.RefCount <= 0 && data.LookupCount <= 0 {
		me.inodePathMapByInode[data.NodeId] = nil, false
	}
}

func (me *PathFileSystemConnector) lookup(key string) *inodeData {
	me.lock.RLock()
	defer me.lock.RUnlock()
	return me.inodePathMap[key]
}

func (me *PathFileSystemConnector) lookupUpdate(parent *inodeData, name string) *inodeData {
	defer me.verify()
	
	key := inodeDataKey(parent.NodeId, name)
	data := me.lookup(key)
	if data != nil {
		return data
	}

	me.lock.Lock()
	defer me.lock.Unlock()

	data, ok := me.inodePathMap[key]
	if !ok {
		data = new(inodeData)
		me.setParent(data, parent)
		data.NodeId = me.nextFreeInode
		data.Name = name
		me.nextFreeInode++

		me.inodePathMapByInode[data.NodeId] = data
		me.inodePathMap[key] = data
	}

	return data
}

func (me *PathFileSystemConnector) getInodeData(nodeid uint64) *inodeData {
	me.lock.RLock()
	defer me.lock.RUnlock()

	val := me.inodePathMapByInode[nodeid]
	if val == nil {
		panic(fmt.Sprintf("inode %v unknown", nodeid))
	}
	return val
}

func (me *PathFileSystemConnector) forgetUpdate(nodeId uint64, forgetCount int) {
	defer me.verify()
	me.lock.Lock()
	defer me.lock.Unlock()

	data, ok := me.inodePathMapByInode[nodeId]
	if ok {
		data.LookupCount -= forgetCount

		if data.mount != nil {
			data.mount.mutex.RLock()
			defer data.mount.mutex.RUnlock()
		}

		if data.LookupCount <= 0 && data.RefCount <= 0 && (data.mount == nil || data.mount.unmountPending) {
			me.inodePathMapByInode[nodeId] = nil, false
			me.inodePathMap[data.Key()] = nil, false
		}
	}
}

func (me *PathFileSystemConnector) renameUpdate(oldParent *inodeData, oldName string, newParent *inodeData, newName string) {
	defer me.verify()
	me.lock.Lock()
	defer me.lock.Unlock()

	oldKey := inodeDataKey(oldParent.NodeId, oldName)
	data := me.inodePathMap[oldKey]
	if data == nil {
		// This can happen if a rename raced with an unlink or
		// another rename.
		//
		// TODO - does the VFS layer allow this?
		//
		// TODO - is this an error we should signal?
		return
	}

	me.inodePathMap[oldKey] = nil, false

	me.setParent(data, newParent)
	data.Name = newName
	newKey := data.Key()

	target := me.inodePathMap[newKey]
	if target != nil {
		panic("file moved into target place.")
	}
	me.inodePathMap[newKey] = data
}

func (me *PathFileSystemConnector) unlinkUpdate(parent *inodeData, name string) {
	defer me.verify()
	me.lock.Lock()
	defer me.lock.Unlock()

	oldKey := inodeDataKey(parent.NodeId, name)
	data := me.inodePathMap[oldKey]

	if data != nil {
		me.inodePathMap[oldKey] = nil, false
		data.Parent = nil
		me.unrefNode(data)
	}
}

// Walk the file system starting from the root.
func (me *PathFileSystemConnector) findInode(fullPath string) *inodeData {
	fullPath = strings.TrimLeft(filepath.Clean(fullPath), "/")
	comps := strings.Split(fullPath, "/", -1)

	me.lock.RLock()
	defer me.lock.RUnlock()

	node := me.inodePathMapByInode[FUSE_ROOT_ID]
	for i, component := range comps {
		if len(component) == 0 {
			continue
		}

		key := inodeDataKey(node.NodeId, component)
		node = me.inodePathMap[key]
		if node == nil {
			panic(fmt.Sprintf("findInode: %v %v", i, fullPath))
		}
	}
	return node
}

////////////////////////////////////////////////////////////////
// Below routines should not access inodePathMap(ByInode) directly,
// and there need no locking.

func NewPathFileSystemConnector(fs PathFilesystem) (out *PathFileSystemConnector) {
	out = new(PathFileSystemConnector)
	out.inodePathMap = make(map[string]*inodeData)
	out.inodePathMapByInode = make(map[uint64]*inodeData)

	rootData := new(inodeData)
	rootData.NodeId = FUSE_ROOT_ID
	rootData.Type = ModeToType(S_IFDIR)

	out.inodePathMap[rootData.Key()] = rootData
	out.inodePathMapByInode[FUSE_ROOT_ID] = rootData
	out.nextFreeInode = FUSE_ROOT_ID + 1

	out.options.NegativeTimeout = 0.0
	out.options.AttrTimeout = 1.0
	out.options.EntryTimeout = 1.0

	if code := out.Mount("/", fs); code != OK {
		panic("root mount failed.")
	}
	return out
}

func (me *PathFileSystemConnector) SetOptions(opts PathFileSystemConnectorOptions) {
	me.options = opts
}


func (me *PathFileSystemConnector) Mount(mountPoint string, fs PathFilesystem) Status {
	var node *inodeData

	if mountPoint != "/" {
		dirParent, base := filepath.Split(mountPoint)
		dirParentNode := me.findInode(dirParent)

		// Make sure we know the mount point.
		_, _ = me.internalLookup(dirParentNode, base, 0)
	}

	node = me.findInode(mountPoint)

	// TODO - check that fs was not mounted elsewhere.
	if node.RefCount > 0 {
		return EBUSY
	}
	if node.Type&ModeToType(S_IFDIR) == 0 {
		return EINVAL
	}

	code := fs.Mount(me)
	if code != OK {
		if me.Debug {
			log.Println("Mount error: ", mountPoint, code)
		}
		return code
	}

	if me.Debug {
		log.Println("Mount: ", fs, "on", mountPoint, node)
	}

	node.mount = newMount(fs)
	if node.Parent != nil {
		_, parentMount := node.Parent.GetPath()
		parentMount.incOpenCount(1)
	}

	return OK
}

func (me *PathFileSystemConnector) Unmount(path string) Status {
	node := me.findInode(path)
	if node == nil {
		panic(path)
	}

	mount := node.mount
	if mount == nil {
		panic(path)
	}

	mount.mutex.Lock()
	defer mount.mutex.Unlock()
	if mount.openCount > 0 {
		log.Println("busy: ", mount)
		return EBUSY
	}

	if me.Debug {
		log.Println("Unmount: ", mount)
	}

	if node.RefCount > 0 {
		mount.fs.Unmount()
		mount.unmountPending = true
	} else {
		node.mount = nil
	}

	if node.Parent != nil {
		_, parentMount := node.Parent.GetPath()
		parentMount.incOpenCount(-1)
	}
	return OK
}

func (me *PathFileSystemConnector) GetPath(nodeid uint64) (path string, mount *mountData, node *inodeData) {
	n := me.getInodeData(nodeid)
	p, m := n.GetPath()
	return p, m, n
}

func (me *PathFileSystemConnector) Init(h *InHeader, input *InitIn) (*InitOut, Status) {
	// TODO ?
	return new(InitOut), OK
}

func (me *PathFileSystemConnector) Destroy(h *InHeader, input *InitIn) {
	// TODO - umount all.
}

func (me *PathFileSystemConnector) Lookup(header *InHeader, name string) (out *EntryOut, status Status) {
	parent := me.getInodeData(header.NodeId)
	return me.internalLookup(parent, name, 1)
}

func (me *PathFileSystemConnector) internalLookup(parent *inodeData, name string, lookupCount int) (out *EntryOut, status Status) {
	// TODO - fuse.c has special case code for name == "." and
	// "..", those lookups happen if FUSE_EXPORT_SUPPORT is set in
	// Init.
	fullPath, mount := parent.GetPath()
	if mount == nil {
		return NegativeEntry(me.options.NegativeTimeout), OK
	}
	fullPath = filepath.Join(fullPath, name)

	attr, err := mount.fs.GetAttr(fullPath)

	if err == ENOENT && me.options.NegativeTimeout > 0.0 {
		return NegativeEntry(me.options.NegativeTimeout), OK
	}

	if err != OK {
		return nil, err
	}

	data := me.lookupUpdate(parent, name)
	data.LookupCount += lookupCount
	data.Type = ModeToType(attr.Mode)

	out = new(EntryOut)
	out.NodeId = data.NodeId
	out.Generation = 1 // where to get the generation?

	SplitNs(me.options.EntryTimeout, &out.EntryValid, &out.EntryValidNsec)
	SplitNs(me.options.AttrTimeout, &out.AttrValid, &out.AttrValidNsec)
	out.Attr = *attr
	out.Attr.Ino = data.NodeId
	return out, OK
}

func (me *PathFileSystemConnector) Forget(h *InHeader, input *ForgetIn) {
	me.forgetUpdate(h.NodeId, int(input.Nlookup))
}

func (me *PathFileSystemConnector) GetAttr(header *InHeader, input *GetAttrIn) (out *AttrOut, code Status) {
	// TODO - do something intelligent with input.Fh.

	// TODO - should we update inodeData.Type?
	fullPath, mount, _ := me.GetPath(header.NodeId)
	if mount == nil {
		return nil, ENOENT
	}
	attr, err := mount.fs.GetAttr(fullPath)
	if err != OK {
		return nil, err
	}

	out = new(AttrOut)
	out.Attr = *attr
	out.Attr.Ino = header.NodeId
	SplitNs(me.options.AttrTimeout, &out.AttrValid, &out.AttrValidNsec)

	return out, OK
}

func (me *PathFileSystemConnector) OpenDir(header *InHeader, input *OpenIn) (flags uint32, fuseFile RawFuseDir, status Status) {
	fullPath, mount, _ := me.GetPath(header.NodeId)
	if mount == nil {
		return 0, nil, ENOENT
	}
	// TODO - how to handle return flags, the FUSE open flags?
	stream, err := mount.fs.OpenDir(fullPath)
	if err != OK {
		return 0, nil, err
	}

	mount.incOpenCount(1)

	de := new(FuseDir)
	de.connector = me
	de.parentIno = header.NodeId
	de.stream = stream
	return 0, de, OK
}

func (me *PathFileSystemConnector) Open(header *InHeader, input *OpenIn) (flags uint32, fuseFile RawFuseFile, status Status) {
	fullPath, mount, _ := me.GetPath(header.NodeId)
	if mount == nil {
		return 0, nil, ENOENT
	}
	// TODO - how to handle return flags, the FUSE open flags?
	f, err := mount.fs.Open(fullPath, input.Flags)
	if err != OK {
		return 0, nil, err
	}

	mount.incOpenCount(1)
	return 0, f, OK
}

func (me *PathFileSystemConnector) SetAttr(header *InHeader, input *SetAttrIn) (out *AttrOut, code Status) {
	var err Status = OK

	// TODO - support Fh.   (FSetAttr/FGetAttr/FTruncate.)
	fullPath, mount, _ := me.GetPath(header.NodeId)
	if mount == nil {
		return nil, ENOENT
	}

	if input.Valid&FATTR_MODE != 0 {
		err = mount.fs.Chmod(fullPath, input.Mode)
	}
	if err != OK && (input.Valid&FATTR_UID != 0 || input.Valid&FATTR_GID != 0) {
		// TODO - can we get just FATTR_GID but not FATTR_UID ?
		err = mount.fs.Chown(fullPath, uint32(input.Uid), uint32(input.Gid))
	}
	if input.Valid&FATTR_SIZE != 0 {
		mount.fs.Truncate(fullPath, input.Size)
	}
	if err != OK && (input.Valid&FATTR_ATIME != 0 || input.Valid&FATTR_MTIME != 0) {
		err = mount.fs.Utimens(fullPath,
			uint64(input.Atime*1e9)+uint64(input.Atimensec),
			uint64(input.Mtime*1e9)+uint64(input.Mtimensec))
	}
	if err != OK && (input.Valid&FATTR_ATIME_NOW != 0 || input.Valid&FATTR_MTIME_NOW != 0) {
		// TODO - should set time to now. Maybe just reuse
		// Utimens() ?  Go has no UTIME_NOW unfortunately.
	}
	if err != OK {
		return nil, err
	}

	// TODO - where to get GetAttrIn.Flags / Fh ?
	return me.GetAttr(header, new(GetAttrIn))
}

func (me *PathFileSystemConnector) Readlink(header *InHeader) (out []byte, code Status) {
	fullPath, mount, _ := me.GetPath(header.NodeId)
	if mount == nil {
		return nil, ENOENT
	}
	val, err := mount.fs.Readlink(fullPath)
	return bytes.NewBufferString(val).Bytes(), err
}

func (me *PathFileSystemConnector) Mknod(header *InHeader, input *MknodIn, name string) (out *EntryOut, code Status) {
	fullPath, mount, node := me.GetPath(header.NodeId)
	if mount == nil {
		return nil, ENOENT
	}
	fullPath = filepath.Join(fullPath, name)
	err := mount.fs.Mknod(fullPath, input.Mode, uint32(input.Rdev))
	if err != OK {
		return nil, err
	}
	return me.internalLookup(node, name, 1)
}

func (me *PathFileSystemConnector) Mkdir(header *InHeader, input *MkdirIn, name string) (out *EntryOut, code Status) {
	fullPath, mount, parent := me.GetPath(header.NodeId)
	if mount == nil {
		return nil, ENOENT
	}
	code = mount.fs.Mkdir(filepath.Join(fullPath, name), input.Mode)
	if code == OK {
		out, code = me.internalLookup(parent, name, 1)
	}
	return out, code
}

func (me *PathFileSystemConnector) Unlink(header *InHeader, name string) (code Status) {
	fullPath, mount, parent := me.GetPath(header.NodeId)
	if mount == nil {
		return ENOENT
	}
	code = mount.fs.Unlink(filepath.Join(fullPath, name))
	if code == OK {
		// Like fuse.c, we update our internal tables.
		me.unlinkUpdate(parent, name)
	}
	return code
}

func (me *PathFileSystemConnector) Rmdir(header *InHeader, name string) (code Status) {
	fullPath, mount, parent := me.GetPath(header.NodeId)
	if mount == nil {
		return ENOENT
	}
	code = mount.fs.Rmdir(filepath.Join(fullPath, name))
	if code == OK {
		me.unlinkUpdate(parent, name)
	}
	return code
}

func (me *PathFileSystemConnector) Symlink(header *InHeader, pointedTo string, linkName string) (out *EntryOut, code Status) {
	fullPath, mount, parent := me.GetPath(header.NodeId)
	if mount == nil {
		return nil, ENOENT
	}
	err := mount.fs.Symlink(pointedTo, filepath.Join(fullPath, linkName))
	if err != OK {
		return nil, err
	}

	out, code = me.internalLookup(parent, linkName, 1)
	return out, code
}

func (me *PathFileSystemConnector) Rename(header *InHeader, input *RenameIn, oldName string, newName string) (code Status) {
	oldPath, oldMount, oldParent := me.GetPath(header.NodeId)
	newPath, mount, newParent := me.GetPath(input.Newdir)
	if mount == nil || oldMount == nil {
		return ENOENT
	}
	if mount != oldMount {
		return EXDEV
	}

	oldPath = filepath.Join(oldPath, oldName)
	newPath = filepath.Join(newPath, newName)
	code = mount.fs.Rename(oldPath, newPath)
	if code == OK {
		// It is conceivable that the kernel module will issue a
		// forget for the old entry, and a lookup request for the new
		// one, but the fuse.c updates its client-side tables on its
		// own, so we do this as well.
		//
		// It should not hurt for us to do it here as well, although
		// it remains unclear how we should update Count.
		me.renameUpdate(oldParent, oldName, newParent, newName)
	}
	return code
}

func (me *PathFileSystemConnector) Link(header *InHeader, input *LinkIn, filename string) (out *EntryOut, code Status) {
	orig, mount, _ := me.GetPath(input.Oldnodeid)
	newName, newMount, newParent := me.GetPath(header.NodeId)

	if mount == nil || newMount == nil {
		return nil, ENOENT
	}
	if mount != newMount {
		return nil, EXDEV
	}
	newName = filepath.Join(newName, filename)
	err := mount.fs.Link(orig, newName)

	if err != OK {
		return nil, err
	}

	return me.internalLookup(newParent, filename, 1)
}

func (me *PathFileSystemConnector) Access(header *InHeader, input *AccessIn) (code Status) {
	p, mount, _ := me.GetPath(header.NodeId)
	if mount == nil {
		return ENOENT
	}
	return mount.fs.Access(p, input.Mask)
}

func (me *PathFileSystemConnector) Create(header *InHeader, input *CreateIn, name string) (flags uint32, fuseFile RawFuseFile, out *EntryOut, code Status) {
	directory, mount, parent := me.GetPath(header.NodeId)
	if mount == nil {
		return 0, nil, nil, ENOENT
	}
	fullPath := filepath.Join(directory, name)

	f, err := mount.fs.Create(fullPath, uint32(input.Flags), input.Mode)
	if err != OK {
		return 0, nil, nil, err
	}

	mount.incOpenCount(1)

	out, code = me.internalLookup(parent, name, 1)
	return 0, f, out, code
}

func (me *PathFileSystemConnector) Release(header *InHeader, f RawFuseFile) {
	_, mount, _ := me.GetPath(header.NodeId)
	if mount != nil {
		mount.incOpenCount(-1)
	}
}

func (me *PathFileSystemConnector) ReleaseDir(header *InHeader, f RawFuseDir) {
	_, mount, _ := me.GetPath(header.NodeId)
	if mount != nil {
		mount.incOpenCount(-1)
	}
}

func (me *PathFileSystemConnector) GetXAttr(header *InHeader, attribute string) (data []byte, code Status) {
	path, mount, _ := me.GetPath(header.NodeId)
	if mount == nil {
		return nil, ENOENT
	}

	data, code = mount.fs.GetXAttr(path, attribute)
	return data, code
}

func (me *PathFileSystemConnector) RemoveXAttr(header *InHeader, attr string) Status {
	path, mount, _ := me.GetPath(header.NodeId)
	if mount == nil {
		return ENOENT
	}

	return mount.fs.RemoveXAttr(path, attr)
}

func (me *PathFileSystemConnector) SetXAttr(header *InHeader, input *SetXAttrIn, attr string, data []byte) Status {
	path, mount, _ := me.GetPath(header.NodeId)
	if mount == nil {
		return ENOENT
	}

	return mount.fs.SetXAttr(path, attr, data, int(input.Flags))
}

func (me *PathFileSystemConnector) ListXAttr(header *InHeader) (data []byte, code Status) {
	path, mount, _ := me.GetPath(header.NodeId)
	if mount == nil {
		return nil, ENOENT
	}

	attrs, code := mount.fs.ListXAttr(path)
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
