// Random odds and ends.

package fuse

import (
	"flag"
	"fmt"
	"log"
	"os"
	"reflect"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

func (code Status) String() string {
	if code <= 0 {
		return []string{
			"OK",
			"NOTIFY_POLL",
			"NOTIFY_INVAL_INODE",
			"NOTIFY_INVAL_ENTRY",
		}[-code]
	}
	return fmt.Sprintf("%d=%v", int(code), syscall.Errno(code))
}

func (code Status) Ok() bool {
	return code == OK
}

// Convert error back to Errno based errors.
func ToStatus(err error) Status {
	if err != nil {
		switch t := err.(type) {
		case syscall.Errno:
			return Status(t)
		case *os.SyscallError:
			return Status(t.Errno.(syscall.Errno))
		case *os.PathError:
			return ToStatus(t.Err)
		case *os.LinkError:
			return ToStatus(t.Err)
		default:
			log.Println("can't convert error type:", err)
			return ENOSYS
		}
	}
	return OK
}

func splitDuration(dt time.Duration, secs *uint64, nsecs *uint32) {
	ns := int64(dt)
	*nsecs = uint32(ns % 1e9)
	*secs = uint64(ns / 1e9)
}

func ModeToType(mode uint32) uint32 {
	return (mode & 0170000) >> 12
}

func CheckSuccess(e error) {
	if e != nil {
		log.Panicf("Unexpected error: %v", e)
	}
}

// Thanks to Andrew Gerrand for this hack.
func asSlice(ptr unsafe.Pointer, byteCount uintptr) []byte {
	h := &reflect.SliceHeader{uintptr(ptr), int(byteCount), int(byteCount)}
	return *(*[]byte)(unsafe.Pointer(h))
}


func Version() string {
	if version != nil {
		return *version
	}
	return "unknown"
}

func ReverseJoin(rev_components []string, sep string) string {
	components := make([]string, len(rev_components))
	for i, v := range rev_components {
		components[len(rev_components)-i-1] = v
	}
	return strings.Join(components, sep)
}

func CurrentOwner() *Owner {
	return &Owner{
		Uid: uint32(os.Getuid()),
		Gid: uint32(os.Getgid()),
	}
}

func VerboseTest() bool {
	flag := flag.Lookup("test.v")
	return flag != nil && flag.Value.String() == "true"
}

func init() {
	p := syscall.Getpagesize()
	if p != PAGESIZE {
		log.Panicf("page size incorrect: %d", p)
	}
}
