// Copyright 2019 the Go-FUSE Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package nodefs

import (
	"bytes"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sync"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/hanwen/go-fuse/fuse"
	"github.com/hanwen/go-fuse/internal/testutil"
)

var _ = log.Println

type testCase struct {
	*testing.T

	dir     string
	origDir string
	mntDir  string

	loopback Operations
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

func newTestCase(t *testing.T) *testCase {
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

	tc.loopback = NewLoopback(tc.origDir)
	_ = time.Second
	oneSec := time.Second
	tc.rawFS = NewNodeFS(tc.loopback, &Options{
		Debug: testutil.VerboseTest(),

		// NOSUBMIT - should run all tests without cache too
		EntryTimeout: &oneSec,
		AttrTimeout:  &oneSec,
	})

	var err error
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
	tc := newTestCase(t)
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
	if got, want := stat.Mode, uint32(fuse.S_IFREG|0644); got != want {
		t.Errorf("got mode %o, want %o", got, want)
	}

	if err := os.Remove(fn); err != nil {
		t.Errorf("Remove: %v", err)
	}

	if fi, err := os.Lstat(fn); err == nil {
		t.Errorf("Lstat after remove: got file %v", fi)
	}
}

func TestFile(t *testing.T) {
	tc := newTestCase(t)
	defer tc.Clean()

	content := []byte("hello world")
	fn := tc.mntDir + "/file"

	if err := ioutil.WriteFile(fn, content, 0755); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if got, err := ioutil.ReadFile(fn); err != nil {
		t.Fatalf("ReadFile: %v", err)
	} else if bytes.Compare(got, content) != 0 {
		t.Errorf("got %q, want %q", got, content)
	}

	f, err := os.Open(fn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	defer f.Close()

	fi, err := f.Stat()

	if err != nil {
		t.Fatalf("Fstat: %v", err)
	} else if int(fi.Size()) != len(content) {
		t.Errorf("got size %d want 5", fi.Size())
	}

	stat := fuse.ToStatT(fi)
	if got, want := stat.Mode, uint32(fuse.S_IFREG|0755); got != want {
		t.Errorf("Fstat: got mode %o, want %o", got, want)
	}

	if err := f.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}

func TestFileTruncate(t *testing.T) {
	tc := newTestCase(t)
	defer tc.Clean()

	content := []byte("hello world")

	if err := ioutil.WriteFile(tc.origDir+"/file", content, 0755); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	f, err := os.OpenFile(tc.mntDir+"/file", os.O_RDWR, 0644)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer f.Close()

	const trunc = 5
	if err := f.Truncate(5); err != nil {
		t.Errorf("Truncate: %v", err)
	}

	if err := f.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}

	if got, err := ioutil.ReadFile(tc.origDir + "/file"); err != nil {
		t.Fatalf("ReadFile: %v", err)
	} else if want := content[:trunc]; bytes.Compare(got, want) != 0 {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestFileFdLeak(t *testing.T) {
	tc := newTestCase(t)
	defer func() {
		if tc != nil {
			tc.Clean()
		}
	}()

	content := []byte("hello world")

	if err := ioutil.WriteFile(tc.origDir+"/file", content, 0755); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	for i := 0; i < 100; i++ {
		if _, err := ioutil.ReadFile(tc.mntDir + "/file"); err != nil {
			t.Fatalf("ReadFile: %v", err)
		}
	}

	if runtime.GOOS == "linux" {
		infos, err := ioutil.ReadDir("/proc/self/fd")
		if err != nil {
			t.Errorf("ReadDir %v", err)
		}

		if len(infos) > 15 {
			t.Errorf("found %d open file descriptors for 100x ReadFile", len(infos))
		}
	}

	tc.Clean()
	bridge := tc.rawFS.(*rawBridge)
	tc = nil

	if got := len(bridge.files); got > 3 {
		t.Errorf("found %d used file handles, should be <= 3", got)
	}
}

func TestMkdir(t *testing.T) {
	tc := newTestCase(t)
	defer tc.Clean()

	if err := os.Mkdir(tc.mntDir+"/dir", 0755); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}

	if fi, err := os.Lstat(tc.mntDir + "/dir"); err != nil {
		t.Fatalf("Lstat %v", err)
	} else if !fi.IsDir() {
		t.Fatalf("is not a directory")
	}

	if err := os.Remove(tc.mntDir + "/dir"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
}

func testRenameOverwrite(t *testing.T, destExists bool) {
	tc := newTestCase(t)
	defer tc.Clean()

	if err := os.Mkdir(tc.origDir+"/dir", 0755); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	if err := ioutil.WriteFile(tc.origDir+"/file", []byte("hello"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if destExists {
		if err := ioutil.WriteFile(tc.origDir+"/dir/renamed", []byte("xx"), 0644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
	}

	st := syscall.Stat_t{}
	if err := syscall.Lstat(tc.mntDir+"/file", &st); err != nil {
		t.Fatalf("Lstat before: %v", err)
	}
	beforeIno := st.Ino
	if err := os.Rename(tc.mntDir+"/file", tc.mntDir+"/dir/renamed"); err != nil {
		t.Errorf("Rename: %v", err)
	}

	if fi, err := os.Lstat(tc.mntDir + "/file"); err == nil {
		t.Fatalf("Lstat old: %v", fi)
	}

	if err := syscall.Lstat(tc.mntDir+"/dir/renamed", &st); err != nil {
		t.Fatalf("Lstat after: %v", err)
	}

	if got := st.Ino; got != beforeIno {
		t.Errorf("got ino %d, want %d", got, beforeIno)
	}
}

func TestRenameDestExist(t *testing.T) {
	testRenameOverwrite(t, true)
}

func TestRenameDestNoExist(t *testing.T) {
	testRenameOverwrite(t, false)
}

func TestRenameNoOverwrite(t *testing.T) {
	tc := newTestCase(t)
	defer tc.Clean()

	if err := os.Mkdir(tc.origDir+"/dir", 0755); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	if err := ioutil.WriteFile(tc.origDir+"/file", []byte("hello"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := ioutil.WriteFile(tc.origDir+"/dir/file", []byte("x"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	f1, err := syscall.Open(tc.mntDir+"/", syscall.O_DIRECTORY, 0)
	if err != nil {
		t.Fatalf("open 1: %v", err)
	}
	defer syscall.Close(f1)
	f2, err := syscall.Open(tc.mntDir+"/dir", syscall.O_DIRECTORY, 0)
	if err != nil {
		t.Fatalf("open 2: %v", err)
	}
	defer syscall.Close(f2)

	if err := unix.Renameat2(f1, "file", f2, "file", unix.RENAME_NOREPLACE); err == nil {
		t.Errorf("rename NOREPLACE succeeded")
	} else if err != syscall.EEXIST {
		t.Errorf("got %v (%T) want EEXIST", err, err)
	}

	if err := unix.Renameat2(f1, "file", f2, "file", unix.RENAME_EXCHANGE); err == nil {
		t.Errorf("rename EXCHANGE succeeded")
	} else if err != syscall.EINVAL {
		t.Errorf("got %v (%T) want %v (%T)", err, err, syscall.EINVAL, syscall.EINVAL)
	}
}

func TestNlinkZero(t *testing.T) {
	// xfstest generic/035.
	tc := newTestCase(t)
	defer tc.Clean()

	src := tc.mntDir + "/src"
	dst := tc.mntDir + "/dst"
	if err := ioutil.WriteFile(src, []byte("source"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if err := ioutil.WriteFile(dst, []byte("dst"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	f, err := os.Open(dst)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer f.Close()

	var st syscall.Stat_t
	if err := syscall.Fstat(int(f.Fd()), &st); err != nil {
		t.Errorf("Fstat before: %v", err)
	} else if st.Nlink != 1 {
		t.Errorf("Nlink of file: got %d, want 1", st.Nlink)
	}

	if err := os.Rename(src, dst); err != nil {
		t.Fatalf("Rename: %v", err)
	}

	if err := syscall.Fstat(int(f.Fd()), &st); err != nil {
		t.Errorf("Fstat after: %v", err)
	} else if st.Nlink != 0 {
		t.Errorf("Nlink of overwritten file: got %d, want 0", st.Nlink)
	}
}

func TestParallelFileOpen(t *testing.T) {
	tc := newTestCase(t)
	defer tc.Clean()

	fn := tc.mntDir + "/file"
	if err := ioutil.WriteFile(fn, []byte("content"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	var wg sync.WaitGroup
	one := func(b byte) {
		f, err := os.OpenFile(fn, os.O_RDWR, 0644)
		if err != nil {
			t.Fatalf("OpenFile: %v", err)
		}
		var buf [10]byte
		f.Read(buf[:])
		buf[0] = b
		f.WriteAt(buf[0:1], 2)
		f.Close()
		wg.Done()
	}
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go one(byte(i))
	}
	wg.Wait()
}

func TestSymlink(t *testing.T) {
	tc := newTestCase(t)
	defer tc.Clean()

	fn := tc.mntDir + "/link"
	target := "target"
	if err := os.Symlink(target, fn); err != nil {
		t.Fatalf("Symlink: %v", err)
	}

	if got, err := os.Readlink(fn); err != nil {
		t.Fatalf("Readlink: %v", err)
	} else if got != target {
		t.Errorf("Readlink: got %q, want %q", got, target)
	}
}

func TestLink(t *testing.T) {
	tc := newTestCase(t)
	defer tc.Clean()

	link := tc.mntDir + "/link"
	target := tc.mntDir + "/target"

	if err := ioutil.WriteFile(target, []byte("hello"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	st := syscall.Stat_t{}
	if err := syscall.Lstat(target, &st); err != nil {
		t.Fatalf("Lstat before: %v", err)
	}

	beforeIno := st.Ino
	if err := os.Link(target, link); err != nil {
		t.Errorf("Link: %v", err)
	}

	if err := syscall.Lstat(link, &st); err != nil {
		t.Fatalf("Lstat after: %v", err)
	}

	if st.Ino != beforeIno {
		t.Errorf("Lstat after: got %d, want %d", st.Ino, beforeIno)
	}
}

func TestNotifyEntry(t *testing.T) {
	tc := newTestCase(t)
	defer tc.Clean()

	orig := tc.origDir + "/file"
	fn := tc.mntDir + "/file"
	if err := ioutil.WriteFile(orig, []byte("hello"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

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

	if code := InodeOf(tc.loopback).NotifyEntry("file"); !code.Ok() {
		t.Errorf("notify failed: %v", code)
	}

	if err := syscall.Lstat(fn, &after); err != syscall.ENOENT {
		t.Fatalf("Lstat after: got %v, want ENOENT", err)
	}
}

// XXX Test Notify() , but requires KEEP_CACHE ? or could use mmap?
// XXX Test NotifyDelete?

func TestReadDir(t *testing.T) {
	tc := newTestCase(t)
	defer tc.Clean()

	// XXX what about ".." and "." ?
	want := map[string]bool{}
	for i := 0; i < 2; i++ {
		// 40 bytes of filename, so 110 entries overflows a
		// 4096 page.
		nm := fmt.Sprintf("file%036x", i)
		want[nm] = true
		if err := ioutil.WriteFile(tc.origDir+"/"+nm, []byte("hello"), 0644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
	}

	entries, err := ioutil.ReadDir(tc.mntDir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	got := map[string]bool{}
	for _, e := range entries {
		got[e.Name()] = true
	}
	if len(got) != len(want) {
		t.Errorf("got %d entries, want %d", len(got), len(want))
	}
	for k := range got {
		if !want[k] {
			t.Errorf("got unknown name %q", k)
		}
	}
}
