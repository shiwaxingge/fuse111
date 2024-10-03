// The fuse package provides APIs to implement filesystems in
// userspace, using libfuse on Linux.
package fuse

import (
	"os"
)

// Types for users to implement.

// A filesystem API that uses paths rather than inodes.  A minimal
// file system should have at least a functional GetAttr method.
// Typically, each call happens in its own goroutine, so take care to
// make the file system thread-safe.
//
// Include DefaultFileSystem to provide a default null implementation of
// required methods.
type FileSystem interface {
	// Used for pretty printing.
	Name() string

	// Attributes
	GetAttr(name string, context *Context) (*os.FileInfo, Status)

	// These should update the file's ctime too.
	Chmod(name string, mode uint32, context *Context) (code Status)
	Chown(name string, uid uint32, gid uint32, context *Context) (code Status)
	Utimens(name string, AtimeNs uint64, MtimeNs uint64, context *Context) (code Status)

	Truncate(name string, offset uint64, context *Context) (code Status)

	Access(name string, mode uint32, context *Context) (code Status)

	// Tree structure
	Link(oldName string, newName string, context *Context) (code Status)
	Mkdir(name string, mode uint32, context *Context) Status
	Mknod(name string, mode uint32, dev uint32, context *Context) Status
	Rename(oldName string, newName string, context *Context) (code Status)
	Rmdir(name string, context *Context) (code Status)
	Unlink(name string, context *Context) (code Status)

	// Extended attributes.
	GetXAttr(name string, attribute string, context *Context) (data []byte, code Status)
	ListXAttr(name string, context *Context) (attributes []string, code Status)
	RemoveXAttr(name string, attr string, context *Context) Status
	SetXAttr(name string, attr string, data []byte, flags int, context *Context) Status

	// Called after mount.
	Mount(connector *FileSystemConnector)
	Unmount()

	// File handling.  If opening for writing, the file's mtime
	// should be updated too.
	Open(name string, flags uint32, context *Context) (file File, code Status)
	Create(name string, flags uint32, mode uint32, context *Context) (file File, code Status)

	// Flush() gets called as a file opened for read/write.
	Flush(name string) Status

	// Directory handling
	OpenDir(name string, context *Context) (stream chan DirEntry, code Status)

	// Symlinks.
	Symlink(value string, linkName string, context *Context) (code Status)
	Readlink(name string, context *Context) (string, Status)

	StatFs() *StatfsOut
}

// A File object should be returned from FileSystem.Open and
// FileSystem.Create.  Include DefaultFile into the struct to inherit
// a default null implementation.
//
// TODO - should File be thread safe?
// TODO - should we pass a *Context argument?
type File interface {
	Read(*ReadIn, BufferPool) ([]byte, Status)
	Write(*WriteIn, []byte) (written uint32, code Status)
	Truncate(size uint64) Status

	GetAttr() (*os.FileInfo, Status)
	Chown(uid uint32, gid uint32) Status
	Chmod(perms uint32) Status
	Utimens(atimeNs uint64, mtimeNs uint64) Status
	Flush() Status
	Release()
	Fsync(*FsyncIn) (code Status)
}

type WithFlags struct {
	File

	// Put FOPEN_* flags here.
	Flags uint32
}

// MountOptions contains time out options for a FileSystem.  The
// default copied from libfuse and set in NewMountOptions() is
// (1s,1s,0s).
type FileSystemOptions struct {
	EntryTimeout    float64
	AttrTimeout     float64
	NegativeTimeout float64

	// If set, replace all uids with given UID.  NewFileSystemOptions() will set
	// this to the daemon's uid/gid.
	*Owner

	// If set, drop extra verification bits to handles.  This will
	// make inode numbers (exported back to callers) stay within
	// int64 (assuming the process uses less than 4G memory.).
	// 64-bit inode numbers makes stat() in 32-bit programs fail.
	SkipCheckHandles bool
}

type MountOptions struct {
	AllowOther bool

	// Options are passed as -o string to fusermount.
	Options []string

	// Default is _DEFAULT_BACKGROUND_TASKS, 12.
	MaxBackground int
}

// DefaultFileSystem implements a FileSystem that returns ENOSYS for every operation.
type DefaultFileSystem struct{}

// DefaultFile returns ENOSYS for every operation.
type DefaultFile struct{}

// RawFileSystem is an interface closer to the FUSE wire protocol.
//
// Unless you really know what you are doing, you should not implement
// this, but rather the FileSystem interface; the details of getting
// interactions with open files, renames, and threading right etc. are
// somewhat tricky and not very interesting.
//
// Include DefaultRawFileSystem to inherit a null implementation.
type RawFileSystem interface {
	Lookup(header *InHeader, name string) (out *EntryOut, status Status)
	Forget(header *InHeader, input *ForgetIn)

	// Attributes.
	GetAttr(header *InHeader, input *GetAttrIn) (out *AttrOut, code Status)
	SetAttr(header *InHeader, input *SetAttrIn) (out *AttrOut, code Status)

	// Modifying structure.
	Mknod(header *InHeader, input *MknodIn, name string) (out *EntryOut, code Status)
	Mkdir(header *InHeader, input *MkdirIn, name string) (out *EntryOut, code Status)
	Unlink(header *InHeader, name string) (code Status)
	Rmdir(header *InHeader, name string) (code Status)
	Rename(header *InHeader, input *RenameIn, oldName string, newName string) (code Status)
	Link(header *InHeader, input *LinkIn, filename string) (out *EntryOut, code Status)

	Symlink(header *InHeader, pointedTo string, linkName string) (out *EntryOut, code Status)
	Readlink(header *InHeader) (out []byte, code Status)
	Access(header *InHeader, input *AccessIn) (code Status)

	// Extended attributes.
	GetXAttr(header *InHeader, attr string) (data []byte, code Status)
	ListXAttr(header *InHeader) (attributes []byte, code Status)
	SetXAttr(header *InHeader, input *SetXAttrIn, attr string, data []byte) Status
	RemoveXAttr(header *InHeader, attr string) (code Status)

	// File handling.
	Create(header *InHeader, input *CreateIn, name string) (flags uint32, handle uint64, out *EntryOut, code Status)
	Open(header *InHeader, input *OpenIn) (flags uint32, handle uint64, status Status)
	Read(*ReadIn, BufferPool) ([]byte, Status)

	Release(header *InHeader, input *ReleaseIn)
	Write(*WriteIn, []byte) (written uint32, code Status)
	Flush(header *InHeader, input *FlushIn) Status
	Fsync(*FsyncIn) (code Status)

	// Directory handling
	OpenDir(header *InHeader, input *OpenIn) (flags uint32, handle uint64, status Status)
	ReadDir(header *InHeader, input *ReadIn) (*DirEntryList, Status)
	ReleaseDir(header *InHeader, input *ReleaseIn)
	FsyncDir(header *InHeader, input *FsyncIn) (code Status)

	//
	Ioctl(header *InHeader, input *IoctlIn) (output *IoctlOut, data []byte, code Status)
	StatFs() *StatfsOut

	// Provide callbacks for pushing notifications to the kernel.
	Init(params *RawFsInit)
}

// DefaultRawFileSystem returns ENOSYS for every operation.
type DefaultRawFileSystem struct{}

// Talk back to FUSE.
//
// InodeNotify invalidates the information associated with the inode
// (ie. data cache, attributes, etc.)
//
// EntryNotify should be used if the existence status of an entry changes,
// (ie. to notify of creation or deletion of the file).
//
// Somewhat confusingly, InodeNotify for a file that stopped to exist
// will give the correct result for Lstat (ENOENT), but the kernel
// will still issue file Open() on the inode.
type RawFsInit struct {
	InodeNotify func(*NotifyInvalInodeOut) Status
	EntryNotify func(parent uint64, name string) Status
}
