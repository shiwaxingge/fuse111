package fuse

// Written with a look to http://ptspts.blogspot.com/2009/11/fuse-protocol-tutorial-for-linux-26.html
import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"unsafe"
)

var fusermountBinary string
var umountBinary string

func Socketpair(network string) (l, r *os.File, err error) {
	var domain int
	var typ int
	switch network {
	case "unix":
		domain = syscall.AF_UNIX
		typ = syscall.SOCK_STREAM
	case "unixgram":
		domain = syscall.AF_UNIX
		typ = syscall.SOCK_SEQPACKET
	default:
		log.Panicf("unknown network %q", network)
	}
	fd, errno := syscall.Socketpair(domain, typ, 0)
	if errno != 0 {
		return nil, nil, os.NewSyscallError("socketpair", errno)
	}
	l = os.NewFile(fd[0], "socketpair-half1")
	r = os.NewFile(fd[1], "socketpair-half2")
	return
}

// Create a FUSE FS on the specified mount point.  The returned
// mount point is always absolute.
func mount(mountPoint string, options string) (f *os.File, finalMountPoint string, err error) {
	local, remote, err := Socketpair("unixgram")
	if err != nil {
		return
	}

	defer local.Close()
	defer remote.Close()

	mountPoint = filepath.Clean(mountPoint)
	if !filepath.IsAbs(mountPoint) {
		cwd := ""
		cwd, err = os.Getwd()
		if err != nil {
			return
		}
		mountPoint = filepath.Clean(filepath.Join(cwd, mountPoint))
	}

	cmd := []string{fusermountBinary, mountPoint}
	if options != "" {
		cmd = append(cmd, "-o")
		cmd = append(cmd, options)
	}

	proc, err := os.StartProcess(fusermountBinary,
		cmd,
		&os.ProcAttr{
			Env:   []string{"_FUSE_COMMFD=3"},
			Files: []*os.File{os.Stdin, os.Stdout, os.Stderr, remote}})

	if err != nil {
		return
	}
	w, err := os.Wait(proc.Pid, 0)
	if err != nil {
		return
	}
	if w.ExitStatus() != 0 {
		err = fmt.Errorf("fusermount exited with code %d\n", w.ExitStatus())
		return
	}

	f, err = getConnection(local)
	finalMountPoint = mountPoint
	return
}

func privilegedUnmount(mountPoint string) error {
	dir, _ := filepath.Split(mountPoint)
	proc, err := os.StartProcess(umountBinary,
		[]string{umountBinary, mountPoint},
		&os.ProcAttr{Dir: dir, Files: []*os.File{nil, nil, os.Stderr}})
	if err != nil {
		return err
	}
	w, err := os.Wait(proc.Pid, 0)
	if w.ExitStatus() != 0 {
		return fmt.Errorf("umount exited with code %d\n", w.ExitStatus())
	}
	return err
}

func unmount(mountPoint string) (err error) {
	if os.Geteuid() == 0 {
		return privilegedUnmount(mountPoint)
	}
	dir, _ := filepath.Split(mountPoint)
	proc, err := os.StartProcess(fusermountBinary,
		[]string{fusermountBinary, "-u", mountPoint},
		&os.ProcAttr{Dir: dir, Files: []*os.File{nil, nil, os.Stderr}})
	if err != nil {
		return
	}
	w, err := os.Wait(proc.Pid, 0)
	if err != nil {
		return
	}
	if w.ExitStatus() != 0 {
		return fmt.Errorf("fusermount -u exited with code %d\n", w.ExitStatus())
	}
	return
}

func getConnection(local *os.File) (f *os.File, err error) {
	var data [4]byte
	control := make([]byte, 4*256)

	// n, oobn, recvflags, from, errno  - todo: error checking.
	_, oobn, _, _,
		errno := syscall.Recvmsg(
		local.Fd(), data[:], control[:], 0)
	if errno != 0 {
		return
	}

	message := *(*syscall.Cmsghdr)(unsafe.Pointer(&control[0]))
	fd := *(*int32)(unsafe.Pointer(uintptr(unsafe.Pointer(&control[0])) + syscall.SizeofCmsghdr))

	if message.Type != 1 {
		err = fmt.Errorf("getConnection: recvmsg returned wrong control type: %d", message.Type)
		return
	}
	if oobn <= syscall.SizeofCmsghdr {
		err = fmt.Errorf("getConnection: too short control message. Length: %d", oobn)
		return
	}
	if fd < 0 {
		err = fmt.Errorf("getConnection: fd < 0: %d", fd)
		return
	}
	f = os.NewFile(int(fd), "<fuseConnection>")
	return
}

func init() {
	var err error
	fusermountBinary, err = exec.LookPath("fusermount")
	if err != nil {
		log.Fatal("Could not find fusermount binary: %v", err)
	}
	umountBinary, _ = exec.LookPath("umount")
	if err != nil {
		log.Fatalf("Could not find umount binary: %v", err)
	}
}
