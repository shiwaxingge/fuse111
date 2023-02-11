package unionfs

import (
	"fmt"
	"github.com/hanwen/go-fuse/fuse"
	"log"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"sync"
	"time"
)

// Creates unions for all files under a given directory,
// walking the tree and looking for directories D which have a
// D/READONLY symlink.
//
// A union for A/B/C will placed under directory A-B-C.
type AutoUnionFs struct {
	fuse.DefaultFileSystem

	lock             sync.RWMutex
	knownFileSystems map[string]*UnionFs
	root             string

	connector *fuse.FileSystemConnector

	options *AutoUnionFsOptions
}

type AutoUnionFsOptions struct {
	UnionFsOptions
	fuse.MountOptions
}

const (
	_READONLY = "READONLY"
	_STATUS   = "status"
	_CONFIG   = "config"
	_ROOT     = "root"
	_VERSION  = "gounionfs_version"
)

func NewAutoUnionFs(directory string, options AutoUnionFsOptions) *AutoUnionFs {
	a := new(AutoUnionFs)
	a.knownFileSystems = make(map[string]*UnionFs)
	a.options = &options
	a.root = directory
	return a
}

func (me *AutoUnionFs) Mount(connector *fuse.FileSystemConnector) fuse.Status {
	me.connector = connector
	time.AfterFunc(0.1e9, func() { me.updateKnownFses() })
	return fuse.OK
}

func (me *AutoUnionFs) addAutomaticFs(roots []string) {
	relative := strings.TrimLeft(strings.Replace(roots[0], me.root, "", -1), "/")
	name := strings.Replace(relative, "/", "-", -1)
	me.addFs(name, roots)
}

func (me *AutoUnionFs) addFs(name string, roots []string) bool {
	if name == _CONFIG || name == _STATUS {
		log.Println("Illegal name for overlay", roots)
		return false
	}

	me.lock.Lock()
	var gofs *UnionFs
	if me.knownFileSystems[name] == nil {
		log.Println("Adding UnionFs for roots", roots)
		gofs = NewUnionFs(roots, me.options.UnionFsOptions)
		me.knownFileSystems[name] = gofs
	}
	me.lock.Unlock()

	if gofs != nil {
		me.connector.Mount("/"+name, gofs, &me.options.MountOptions)
	}
	return true
}

// TODO - should hide these methods.
func (me *AutoUnionFs) VisitDir(path string, f *os.FileInfo) bool {
	roots := me.getRoots(path)
	if roots != nil {
		me.addAutomaticFs(roots)
	}
	return true
}

func (me *AutoUnionFs) getRoots(path string) []string {
	ro := filepath.Join(path, _READONLY)
	fi, err := os.Lstat(ro)
	fiDir, errDir := os.Stat(ro)
	if err == nil && errDir == nil && fi.IsSymlink() && fiDir.IsDirectory() {
		// TODO - should recurse and chain all READONLYs
		// together.
		return []string{path, ro}
	}
	return nil
}

func (me *AutoUnionFs) VisitFile(path string, f *os.FileInfo) {

}

func (me *AutoUnionFs) updateKnownFses() {
	log.Println("Looking for new filesystems")
	filepath.Walk(me.root, me, nil)
	log.Println("Done looking")
}

func (me *AutoUnionFs) Readlink(path string) (out string, code fuse.Status) {
	comps := strings.Split(path, filepath.SeparatorString, -1)
	if comps[0] == _STATUS && comps[1] == _ROOT {
		return me.root, fuse.OK
	}

	if comps[0] != _CONFIG {
		return "", fuse.ENOENT
	}
	name := comps[1]
	me.lock.RLock()
	defer me.lock.RUnlock()
	fs := me.knownFileSystems[name]
	if fs == nil {
		return "", fuse.ENOENT
	}
	return fs.Roots()[0], fuse.OK
}

func (me *AutoUnionFs) getUnionFs(name string) *UnionFs {
	me.lock.RLock()
	defer me.lock.RUnlock()
	return me.knownFileSystems[name]
}

func (me *AutoUnionFs) Symlink(pointedTo string, linkName string) (code fuse.Status) {
	comps := strings.Split(linkName, "/", -1)
	if len(comps) != 2 {
		return fuse.EPERM
	}

	if comps[0] == _CONFIG {
		roots := me.getRoots(pointedTo)
		if roots == nil {
			return syscall.ENOTDIR
		}

		name := comps[1]
		if !me.addFs(name, roots) {
			return fuse.EPERM
		}
		return fuse.OK
	}
	return fuse.EPERM
}


// Must define this, because ENOSYS will suspend all GetXAttr calls.
func (me *AutoUnionFs) GetXAttr(name string, attr string) ([]byte, fuse.Status) {
	return nil, syscall.ENODATA
}

func (me *AutoUnionFs) GetAttr(path string) (*fuse.Attr, fuse.Status) {
	if path == "" || path == _CONFIG || path == _STATUS {
		a := &fuse.Attr{
			Mode: fuse.S_IFDIR | 0755,
		}
		return a, fuse.OK
	}

	if path == filepath.Join(_STATUS, _VERSION) {
		a := &fuse.Attr{
			Mode: fuse.S_IFREG | 0644,
		}
		return a, fuse.OK
	}

	if path == filepath.Join(_STATUS, _ROOT) {
		a := &fuse.Attr{
			Mode: syscall.S_IFLNK | 0644,
		}
		return a, fuse.OK
	}

	comps := strings.Split(path, filepath.SeparatorString, -1)

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

	if me.getUnionFs(path) != nil {
		return &fuse.Attr{
			Mode: fuse.S_IFDIR | 0755,
		},fuse.OK
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
		Name: _ROOT,
		Mode: syscall.S_IFLNK | 0644,
	}

	close(stream)
	return stream, fuse.OK
}

func (me *AutoUnionFs) OpenDir(name string) (stream chan fuse.DirEntry, status fuse.Status) {
	switch name {
	case _STATUS:
		return me.StatusDir()
	case _CONFIG:
		me.updateKnownFses()
	case "/":
		name = ""
	case "":
	default:
		panic(fmt.Sprintf("Don't know how to list dir %v", name))
	}

	me.lock.RLock()
	defer me.lock.RUnlock()

	stream = make(chan fuse.DirEntry, len(me.knownFileSystems)+5)
	for k, _ := range me.knownFileSystems {
		mode := fuse.S_IFDIR | 0755
		if name == _CONFIG {
			mode = syscall.S_IFLNK | 0644
		}

		stream <- fuse.DirEntry{
			Name: k,
			Mode: uint32(mode),
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
