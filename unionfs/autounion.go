package unionfs

import (
	"fmt"
	"github.com/hanwen/go-fuse/fuse"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

type knownFs struct {
	*UnionFs
	*fuse.PathNodeFs
}

// Creates unions for all files under a given directory,
// walking the tree and looking for directories D which have a
// D/READONLY symlink.
//
// A union for A/B/C will placed under directory A-B-C.
type AutoUnionFs struct {
	fuse.DefaultFileSystem

	lock             sync.RWMutex
	knownFileSystems map[string]knownFs
	nameRootMap      map[string]string
	root             string

	nodeFs  *fuse.PathNodeFs
	options *AutoUnionFsOptions

	mountState *fuse.MountState
	connector  *fuse.FileSystemConnector
}

type AutoUnionFsOptions struct {
	UnionFsOptions
	fuse.FileSystemOptions
	fuse.PathNodeFsOptions

	// If set, run updateKnownFses() after mounting.
	UpdateOnMount bool

	// If set hides the _READONLY file.
	HideReadonly bool
}

const (
	_READONLY    = "READONLY"
	_STATUS      = "status"
	_CONFIG      = "config"
	_DEBUG       = "debug"
	_ROOT        = "root"
	_VERSION     = "gounionfs_version"
	_SCAN_CONFIG = ".scan_config"
)

func NewAutoUnionFs(directory string, options AutoUnionFsOptions) *AutoUnionFs {
	if options.HideReadonly {
		options.HiddenFiles = append(options.HiddenFiles, _READONLY)
	}
	a := new(AutoUnionFs)
	a.knownFileSystems = make(map[string]knownFs)
	a.nameRootMap = make(map[string]string)
	a.options = &options
	directory, err := filepath.Abs(directory)
	if err != nil {
		panic("filepath.Abs returned err")
	}
	a.root = directory
	return a
}

func (me *AutoUnionFs) String() string {
	return fmt.Sprintf("AutoUnionFs(%s)", me.root)
}

func (me *AutoUnionFs) OnMount(nodeFs *fuse.PathNodeFs) {
	me.nodeFs = nodeFs
	if me.options.UpdateOnMount {
		time.AfterFunc(100*time.Millisecond, func() { me.updateKnownFses() })
	}
}

func (me *AutoUnionFs) addAutomaticFs(roots []string) {
	relative := strings.TrimLeft(strings.Replace(roots[0], me.root, "", -1), "/")
	name := strings.Replace(relative, "/", "-", -1)

	if me.getUnionFs(name) == nil {
		me.addFs(name, roots)
	}
}

func (me *AutoUnionFs) createFs(name string, roots []string) fuse.Status {
	me.lock.Lock()
	defer me.lock.Unlock()

	for workspace, root := range me.nameRootMap {
		if root == roots[0] && workspace != name {
			log.Printf("Already have a union FS for directory %s in workspace %s",
				roots[0], workspace)
			return fuse.EBUSY
		}
	}

	known := me.knownFileSystems[name]
	if known.UnionFs != nil {
		log.Println("Already have a workspace:", name)
		return fuse.EBUSY
	}

	ufs, err := NewUnionFsFromRoots(roots, &me.options.UnionFsOptions, true)
	if err != nil {
		log.Println("Could not create UnionFs:", err)
		return fuse.EPERM
	}

	log.Printf("Adding workspace %v for roots %v", name, ufs.String())
	nfs := fuse.NewPathNodeFs(ufs, &me.options.PathNodeFsOptions)
	code := me.nodeFs.Mount(name, nfs, &me.options.FileSystemOptions)
	if code.Ok() {
		me.knownFileSystems[name] = knownFs{
			ufs,
			nfs,
		}
		me.nameRootMap[name] = roots[0]
	}
	return code
}

func (me *AutoUnionFs) rmFs(name string) (code fuse.Status) {
	me.lock.Lock()
	defer me.lock.Unlock()

	known := me.knownFileSystems[name]
	if known.UnionFs == nil {
		return fuse.ENOENT
	}

	code = me.nodeFs.Unmount(name)
	if code.Ok() {
		delete(me.knownFileSystems, name)
		delete(me.nameRootMap, name)
	} else {
		log.Printf("Unmount failed for %s.  Code %v", name, code)
	}

	return code
}

func (me *AutoUnionFs) addFs(name string, roots []string) (code fuse.Status) {
	if name == _CONFIG || name == _STATUS || name == _SCAN_CONFIG {
		log.Println("Illegal name for overlay", roots)
		return fuse.EINVAL
	}
	return me.createFs(name, roots)
}

func (me *AutoUnionFs) getRoots(path string) []string {
	ro := filepath.Join(path, _READONLY)
	fi, err := os.Lstat(ro)
	fiDir, errDir := os.Stat(ro)
	if err != nil || errDir != nil {
		return nil
	}

	if fi.Mode()&os.ModeSymlink != 0 && fiDir.IsDir() {
		// TODO - should recurse and chain all READONLYs
		// together.
		return []string{path, ro}
	}
	return nil
}

func (me *AutoUnionFs) visit(path string, fi os.FileInfo, err error) error {
	if fi != nil && fi.IsDir() {
		roots := me.getRoots(path)
		if roots != nil {
			me.addAutomaticFs(roots)
		}
	}
	return nil
}

func (me *AutoUnionFs) updateKnownFses() {
	log.Println("Looking for new filesystems")
	// We unroll the first level of entries in the root manually in order
	// to allow symbolic links on that level.
	directoryEntries, err := ioutil.ReadDir(me.root)
	if err == nil {
		for _, dir := range directoryEntries {
			if dir.IsDir() || dir.Mode()&os.ModeSymlink != 0 {
				path := filepath.Join(me.root, dir.Name())
				dir, _ = os.Stat(path)
				me.visit(path, dir, nil)
				filepath.Walk(path,
					func(path string, fi os.FileInfo, err error) error {
						return me.visit(path, fi, err)
					})
			}
		}
	}
	log.Println("Done looking")
}

func (me *AutoUnionFs) Readlink(path string, context *fuse.Context) (out string, code fuse.Status) {
	comps := strings.Split(path, string(filepath.Separator))
	if comps[0] == _STATUS && comps[1] == _ROOT {
		return me.root, fuse.OK
	}

	if comps[0] != _CONFIG {
		return "", fuse.ENOENT
	}
	name := comps[1]
	me.lock.RLock()
	defer me.lock.RUnlock()

	root, ok := me.nameRootMap[name]
	if ok {
		return root, fuse.OK
	}
	return "", fuse.ENOENT
}

func (me *AutoUnionFs) getUnionFs(name string) *UnionFs {
	me.lock.RLock()
	defer me.lock.RUnlock()
	return me.knownFileSystems[name].UnionFs
}

func (me *AutoUnionFs) Symlink(pointedTo string, linkName string, context *fuse.Context) (code fuse.Status) {
	comps := strings.Split(linkName, "/")
	if len(comps) != 2 {
		return fuse.EPERM
	}

	if comps[0] == _CONFIG {
		roots := me.getRoots(pointedTo)
		if roots == nil {
			return fuse.Status(syscall.ENOTDIR)
		}

		name := comps[1]
		return me.addFs(name, roots)
	}
	return fuse.EPERM
}

func (me *AutoUnionFs) Unlink(path string, context *fuse.Context) (code fuse.Status) {
	comps := strings.Split(path, "/")
	if len(comps) != 2 {
		return fuse.EPERM
	}

	if comps[0] == _CONFIG && comps[1] != _SCAN_CONFIG {
		code = me.rmFs(comps[1])
	} else {
		code = fuse.ENOENT
	}
	return code
}

// Must define this, because ENOSYS will suspend all GetXAttr calls.
func (me *AutoUnionFs) GetXAttr(name string, attr string, context *fuse.Context) ([]byte, fuse.Status) {
	return nil, fuse.ENODATA
}

func (me *AutoUnionFs) GetAttr(path string, context *fuse.Context) (*fuse.Attr, fuse.Status) {
	if path == "" || path == _CONFIG || path == _STATUS {
		a := &fuse.Attr{
			Mode: fuse.S_IFDIR | 0755,
		}
		return a, fuse.OK
	}

	if path == filepath.Join(_STATUS, _VERSION) {
		a := &fuse.Attr{
			Mode: fuse.S_IFREG | 0644,
			Size: uint64(len(fuse.Version())),
		}
		return a, fuse.OK
	}

	if path == filepath.Join(_STATUS, _DEBUG) {
		a := &fuse.Attr{
			Mode: fuse.S_IFREG | 0644,
			Size: uint64(len(me.DebugData())),
		}
		return a, fuse.OK
	}

	if path == filepath.Join(_STATUS, _ROOT) {
		a := &fuse.Attr{
			Mode: syscall.S_IFLNK | 0644,
		}
		return a, fuse.OK
	}

	if path == filepath.Join(_CONFIG, _SCAN_CONFIG) {
		a := &fuse.Attr{
			Mode: fuse.S_IFREG | 0644,
		}
		return a, fuse.OK
	}
	comps := strings.Split(path, string(filepath.Separator))

	if len(comps) > 1 && comps[0] == _CONFIG {
		fs := me.getUnionFs(comps[1])

		if fs == nil {
			return nil, fuse.ENOENT
		}

		a := &fuse.Attr{
			Mode: syscall.S_IFLNK | 0644,
		}
		return a, fuse.OK
	}

	return nil, fuse.ENOENT
}

func (me *AutoUnionFs) StatusDir() (stream chan fuse.DirEntry, status fuse.Status) {
	stream = make(chan fuse.DirEntry, 10)
	stream <- fuse.DirEntry{
		Name: _VERSION,
		Mode: fuse.S_IFREG | 0644,
	}
	stream <- fuse.DirEntry{
		Name: _DEBUG,
		Mode: fuse.S_IFREG | 0644,
	}
	stream <- fuse.DirEntry{
		Name: _ROOT,
		Mode: syscall.S_IFLNK | 0644,
	}

	close(stream)
	return stream, fuse.OK
}

// SetMountState stores the MountState, which is necessary for
// retrieving debug data.
func (me *AutoUnionFs) SetMountState(state *fuse.MountState) {
	me.mountState = state
}

func (me *AutoUnionFs) SetFileSystemConnector(conn *fuse.FileSystemConnector) {
	me.connector = conn
}

func (me *AutoUnionFs) DebugData() string {
	if me.mountState == nil {
		return "AutoUnionFs.mountState not set"
	}
	setting := me.mountState.KernelSettings()
	msg := fmt.Sprintf(
		"Version: %v\n"+
			"Bufferpool: %v\n"+
			"Kernel: %v\n",
		fuse.Version(),
		me.mountState.BufferPoolStats(),
		&setting)

	lat := me.mountState.Latencies()
	if len(lat) > 0 {
		msg += fmt.Sprintf("Latencies: %v\n", lat)
	}

	counts := me.mountState.OperationCounts()
	if len(counts) > 0 {
		msg += fmt.Sprintf("Op counts: %v\n", counts)
	}

	if me.connector != nil {
		msg += fmt.Sprintf("Live inodes: %d\n", me.connector.InodeHandleCount())
	}
	
	return msg
}

func (me *AutoUnionFs) Open(path string, flags uint32, context *fuse.Context) (fuse.File, fuse.Status) {
	if path == filepath.Join(_STATUS, _DEBUG) {
		if flags&fuse.O_ANYWRITE != 0 {
			return nil, fuse.EPERM
		}

		return fuse.NewDataFile([]byte(me.DebugData())), fuse.OK
	}
	if path == filepath.Join(_STATUS, _VERSION) {
		if flags&fuse.O_ANYWRITE != 0 {
			return nil, fuse.EPERM
		}
		return fuse.NewDataFile([]byte(fuse.Version())), fuse.OK
	}
	if path == filepath.Join(_CONFIG, _SCAN_CONFIG) {
		if flags&fuse.O_ANYWRITE != 0 {
			me.updateKnownFses()
		}
		return fuse.NewDevNullFile(), fuse.OK
	}
	return nil, fuse.ENOENT
}

func (me *AutoUnionFs) Truncate(name string, offset uint64, context *fuse.Context) (code fuse.Status) {
	if name != filepath.Join(_CONFIG, _SCAN_CONFIG) {
		log.Println("Huh? Truncating unsupported write file", name)
		return fuse.EPERM
	}
	return fuse.OK
}

func (me *AutoUnionFs) OpenDir(name string, context *fuse.Context) (stream chan fuse.DirEntry, status fuse.Status) {
	switch name {
	case _STATUS:
		return me.StatusDir()
	case _CONFIG:
	case "/":
		name = ""
	case "":
	default:
		log.Printf("Argh! Don't know how to list dir %v", name)
		return nil, fuse.ENOSYS
	}

	me.lock.RLock()
	defer me.lock.RUnlock()

	stream = make(chan fuse.DirEntry, len(me.knownFileSystems)+5)
	if name == _CONFIG {
		for k := range me.knownFileSystems {
			stream <- fuse.DirEntry{
				Name: k,
				Mode: syscall.S_IFLNK | 0644,
			}
		}
	}

	if name == "" {
		stream <- fuse.DirEntry{
			Name: _CONFIG,
			Mode: uint32(fuse.S_IFDIR | 0755),
		}
		stream <- fuse.DirEntry{
			Name: _STATUS,
			Mode: uint32(fuse.S_IFDIR | 0755),
		}
	}
	close(stream)
	return stream, status
}

func (me *AutoUnionFs) StatFs(name string) *fuse.StatfsOut {
	return &fuse.StatfsOut{}
}
