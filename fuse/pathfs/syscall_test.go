package pathfs

import (
	"reflect"
	"os"
	"testing"
	"syscall"
)

func TestSysUtimensat(t *testing.T) {

	symlink := "/tmp/TestSysUtimensat"
	os.Remove(symlink)
	err := os.Symlink("/nonexisting/file", symlink)
	if err != nil {
		t.Fatal(err)
	}

	var ts [2]syscall.Timespec
	// Atime
	ts[0].Nsec = 1111
	ts[0].Sec = 2222
	// Mtime
	ts[1].Nsec = 3333
	ts[1].Sec = 4444

	err = sysUtimensat(0, symlink, &ts, _AT_SYMLINK_NOFOLLOW)
	if err != nil {
		t.Fatal(err)
	}

	var st syscall.Stat_t
	err = syscall.Lstat(symlink, &st)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(st.Atim, ts[0]) {
		t.Errorf("Wrong atime: %v", st.Atim)
	}
	if !reflect.DeepEqual(st.Mtim, ts[1]) {
		t.Errorf("Wrong mtime: %v", st.Mtim)
	}
}
