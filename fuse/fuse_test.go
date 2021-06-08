package fuse

import (
	"fmt"
	"log"
	"os"
	"path"
	"rand"
	"strings"
	"testing"
	"time"
)

var (
	testFileNames = []string{"one", "two", "three.txt"}
)

type testFuse struct{}

func (fs *testFuse) GetAttr(path string) (out Attr, code Status) {
	if strings.HasSuffix(path, ".txt") {
		out.Mode = S_IFREG + 0644
		out.Size = 13
	} else {
		out.Mode = S_IFDIR + 0755
	}
	out.Mtime = uint64(time.Seconds())
	return
}

func (fs *testFuse) List(dir string) (names []string, code Status) {
	names = testFileNames
	return
}

func (fs *testFuse) Open(path string) (file File, code Status) {
	file = &testFile{}
	return
}

type testFile struct{}

func (f *testFile) ReadAt(data []byte, offset int64) (n int, err os.Error) {
	if offset < 13 {
		our := []byte("Hello world!\n")[offset:]
		for i, b := range our {
			data[i] = b
		}
		n = len(our)
		return
	}
	return 0, os.EOF
}

func (f *testFile) Close() (status Status) {
	return OK
}

func errorHandler(errors chan os.Error) {
	for err := range errors {
		log.Println("MountPoint.errorHandler: ", err)
		if err == os.EOF {
			break
		}
	}
}

func TestMount(t *testing.T) {
	fs := new(testFuse)

	tempMountDir := MakeTempDir()

	fmt.Println("Tmpdir is: ", tempMountDir)
	defer os.Remove(tempMountDir)
	m, err, errors := Mount(tempMountDir, fs)
	if err != nil {
		t.Fatalf("Can't mount a dir, err: %v", err)
	}
	defer func() {
		err := m.Unmount()
		if err != nil {
			t.Fatalf("Can't unmount a dir, err: %v", err)
		}
	}()

	// Question: how to neatly do error handling?
	go errorHandler(errors)
	f, err := os.Open(tempMountDir, os.O_RDONLY, 0)
	if err != nil {
		t.Fatalf("Can't open a dir: %s, err: %v", tempMountDir, err)
	}
	defer f.Close()
	names, err := f.Readdirnames(10)
	if err != nil {
		t.Fatalf("Can't ls a dir: %s, err: %v", tempMountDir, err)
	}
	has := strings.Join(names, ", ")
	wanted := strings.Join(testFileNames, ", ")
	if has != wanted {
		t.Errorf("Ls returned wrong results, has: [%s], wanted: [%s]", has, wanted)
		return
	}
}

// Make a temporary directory securely.
func MakeTempDir() string {
	source := rand.NewSource(time.Nanoseconds())
	number := source.Int63() & 0xffff
	name := fmt.Sprintf("tmp%d", number)

	fullName := path.Join(os.TempDir(), name)
	err := os.Mkdir(fullName, 0700)
	if err != nil {
		panic("Mkdir() should always succeed: " + fullName)
	}
	return fullName
}
