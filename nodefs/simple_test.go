// Copyright 2019 the Go-FUSE Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package nodefs

import (
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/hanwen/go-fuse/fuse"
	"github.com/hanwen/go-fuse/internal/testutil"
	"github.com/hanwen/go-fuse/posixtest"
)

type testCase struct {
	*testing.T

	dir     string
	origDir string
	mntDir  string

	loopback InodeEmbedder
	rawFS    fuse.RawFileSystem
	server   *fuse.Server
}

func (tc *testCase) writeOrig(path, content string, mode os.FileMode) {
	if err := ioutil.WriteFile(filepath.Join(tc.origDir, path), []byte(content), mode); err != nil {
		tc.Fatal(err)
	}
}

func (tc *testCase) Clean() {
	if err := tc.server.Unmount(); err != nil {
		tc.Fatal(err)
	}
	if err := os.RemoveAll(tc.dir); err != nil {
		tc.Fatal(err)
	}
}

func newTestCase(t *testing.T, entryCache bool, attrCache bool) *testCase {
	tc := &testCase{
		dir: testutil.TempDir(),
		T:   t,
	}
	tc.origDir = tc.dir + "/orig"
	tc.mntDir = tc.dir + "/mnt"
	if err := os.Mkdir(tc.origDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(tc.mntDir, 0755); err != nil {
		t.Fatal(err)
	}

	var err error
	tc.loopback, err = NewLoopbackRoot(tc.origDir)
	if err != nil {
		t.Fatalf("NewLoopback: %v", err)
	}

	oneSec := time.Second

	attrDT := &oneSec
	if !attrCache {
		attrDT = nil
	}
	entryDT := &oneSec
	if !entryCache {
		entryDT = nil
	}
	tc.rawFS = NewNodeFS(tc.loopback, &Options{
		EntryTimeout: entryDT,
		AttrTimeout:  attrDT,
	})

	tc.server, err = fuse.NewServer(tc.rawFS, tc.mntDir,
		&fuse.MountOptions{
			Debug: testutil.VerboseTest(),
		})
	if err != nil {
		t.Fatal(err)
	}

	go tc.server.Serve()
	if err := tc.server.WaitMount(); err != nil {
		t.Fatal(err)
	}
	return tc
}

func TestBasic(t *testing.T) {
	tc := newTestCase(t, true, true)
	defer tc.Clean()

	tc.writeOrig("file", "hello", 0644)

	fn := tc.mntDir + "/file"
	fi, err := os.Lstat(fn)
	if err != nil {
		t.Fatalf("Lstat: %v", err)
	}

	if fi.Size() != 5 {
		t.Errorf("got size %d want 5", fi.Size())
	}

	stat := fuse.ToStatT(fi)
	if got, want := uint32(stat.Mode), uint32(fuse.S_IFREG|0644); got != want {
		t.Errorf("got mode %o, want %o", got, want)
	}

	if err := os.Remove(fn); err != nil {
		t.Errorf("Remove: %v", err)
	}

	if fi, err := os.Lstat(fn); err == nil {
		t.Errorf("Lstat after remove: got file %v", fi)
	}
}

func TestFileBasic(t *testing.T) {
	tc := newTestCase(t, true, true)
	defer tc.Clean()

	posixtest.FileBasic(t, tc.mntDir)
}

func TestFileTruncate(t *testing.T) {
	tc := newTestCase(t, true, true)
	defer tc.Clean()

	posixtest.TruncateFile(t, tc.mntDir)
}

func TestFileFdLeak(t *testing.T) {
	tc := newTestCase(t, true, true)
	defer func() {
		if tc != nil {
			tc.Clean()
		}
	}()

	posixtest.FdLeak(t, tc.mntDir)

	tc.Clean()
	bridge := tc.rawFS.(*rawBridge)
	tc = nil

	if got := len(bridge.files); got > 3 {
		t.Errorf("found %d used file handles, should be <= 3", got)
	}
}

func TestMkdir(t *testing.T) {
	tc := newTestCase(t, true, true)
	defer tc.Clean()

	posixtest.MkdirRmdir(t, tc.mntDir)
}

func testRenameOverwrite(t *testing.T, destExists bool) {
	tc := newTestCase(t, true, true)
	defer tc.Clean()
	posixtest.RenameOverwrite(t, tc.mntDir, destExists)
}

func TestRenameDestExist(t *testing.T) {
	testRenameOverwrite(t, true)
}

func TestRenameDestNoExist(t *testing.T) {
	testRenameOverwrite(t, false)
}

func TestNlinkZero(t *testing.T) {
	// xfstest generic/035.
	tc := newTestCase(t, true, true)
	defer tc.Clean()

	posixtest.NlinkZero(t, tc.mntDir)
}

func TestParallelFileOpen(t *testing.T) {
	tc := newTestCase(t, true, true)
	defer tc.Clean()

	posixtest.ParallelFileOpen(t, tc.mntDir)
}

func TestSymlink(t *testing.T) {
	tc := newTestCase(t, true, true)
	defer tc.Clean()

	posixtest.SymlinkReadlink(t, tc.mntDir)
}

func TestLink(t *testing.T) {
	tc := newTestCase(t, true, true)
	defer tc.Clean()

	posixtest.Link(t, tc.mntDir)
}

func TestNotifyEntry(t *testing.T) {
	tc := newTestCase(t, true, true)
	defer tc.Clean()

	orig := tc.origDir + "/file"
	fn := tc.mntDir + "/file"
	tc.writeOrig("file", "hello", 0644)

	st := syscall.Stat_t{}
	if err := syscall.Lstat(fn, &st); err != nil {
		t.Fatalf("Lstat before: %v", err)
	}

	if err := os.Remove(orig); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	after := syscall.Stat_t{}
	if err := syscall.Lstat(fn, &after); err != nil {
		t.Fatalf("Lstat after: %v", err)
	} else if !reflect.DeepEqual(st, after) {
		t.Fatalf("got after %#v, want %#v", after, st)
	}

	if errno := tc.loopback.EmbeddedInode().NotifyEntry("file"); errno != 0 {
		t.Errorf("notify failed: %v", errno)
	}

	if err := syscall.Lstat(fn, &after); err != syscall.ENOENT {
		t.Fatalf("Lstat after: got %v, want ENOENT", err)
	}
}

func TestReadDir(t *testing.T) {
	tc := newTestCase(t, true, true)
	defer tc.Clean()

	posixtest.ReadDir(t, tc.mntDir)
}

// This test is racy. If an external process consumes space while this
// runs, we may see spurious differences between the two statfs() calls.
func TestStatFs(t *testing.T) {
	tc := newTestCase(t, true, true)
	defer tc.Clean()

	empty := syscall.Statfs_t{}
	orig := empty
	if err := syscall.Statfs(tc.origDir, &orig); err != nil {
		t.Fatal("statfs orig", err)
	}

	mnt := syscall.Statfs_t{}
	if err := syscall.Statfs(tc.mntDir, &mnt); err != nil {
		t.Fatal("statfs mnt", err)
	}

	var mntFuse, origFuse fuse.StatfsOut
	mntFuse.FromStatfsT(&mnt)
	origFuse.FromStatfsT(&orig)

	if !reflect.DeepEqual(mntFuse, origFuse) {
		t.Errorf("Got %#v, want %#v", mntFuse, origFuse)
	}
}

func TestGetAttrParallel(t *testing.T) {
	// We grab a file-handle to provide to the API so rename+fstat
	// can be handled correctly. Here, test that closing and
	// (f)stat in parallel don't lead to fstat on closed files.
	// We can only test that if we switch off caching
	tc := newTestCase(t, false, false)
	defer tc.Clean()

	N := 100

	var fds []int
	var fns []string
	for i := 0; i < N; i++ {
		fn := fmt.Sprintf("file%d", i)
		tc.writeOrig(fn, "ello", 0644)
		fn = filepath.Join(tc.mntDir, fn)
		fns = append(fns, fn)
		fd, err := syscall.Open(fn, syscall.O_RDONLY, 0)
		if err != nil {
			t.Fatalf("Open %d: %v", i, err)
		}

		fds = append(fds, fd)
	}

	var wg sync.WaitGroup
	wg.Add(2 * N)
	for i := 0; i < N; i++ {
		go func(i int) {
			if err := syscall.Close(fds[i]); err != nil {
				t.Errorf("close %d: %v", i, err)
			}
			wg.Done()
		}(i)
		go func(i int) {
			var st syscall.Stat_t
			if err := syscall.Lstat(fns[i], &st); err != nil {
				t.Errorf("lstat %d: %v", i, err)
			}
			wg.Done()
		}(i)
	}
	wg.Wait()
}

func TestMknod(t *testing.T) {
	tc := newTestCase(t, false, false)
	defer tc.Clean()

	modes := map[string]uint32{
		"regular": syscall.S_IFREG,
		"socket":  syscall.S_IFSOCK,
		"fifo":    syscall.S_IFIFO,
	}

	for nm, mode := range modes {
		t.Run(nm, func(t *testing.T) {
			p := filepath.Join(tc.mntDir, nm)
			err := syscall.Mknod(p, mode|0755, (8<<8)|0)
			if err != nil {
				t.Fatalf("mknod(%s): %v", nm, err)
			}

			var st syscall.Stat_t
			if err := syscall.Stat(p, &st); err != nil {
				got := st.Mode &^ 07777
				if want := mode; got != want {
					t.Fatalf("stat(%s): got %o want %o", nm, got, want)
				}
			}

			// We could test if the files can be
			// read/written but: The kernel handles FIFOs
			// without talking to FUSE at all. Presumably,
			// this also holds for sockets.  Regular files
			// are tested extensively elsewhere.
		})
	}
}

func TestTruncate(t *testing.T) {
	tc := newTestCase(t, false, false)
	defer tc.Clean()

	posixtest.TruncateNoFile(t, tc.mntDir)
}
