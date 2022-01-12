// Mounts another directory as loopback for testing and benchmarking
// purposes.

package main

import (
	"github.com/hanwen/go-fuse/fuse"
	"fmt"
	"os"
	"flag"
	"runtime"
)

func main() {
	// Scans the arg list and sets up flags
	debug := flag.Bool("debug", false, "print debugging messages.")
	threaded := flag.Bool("threaded", true, "switch off threading; print debugging messages.")
	flag.Parse()
	if flag.NArg() < 2 {
		// TODO - where to get program name?
		fmt.Println("usage: main ORIGINAL MOUNTPOINT")
		os.Exit(2)
	}

	orig := flag.Arg(0)
	fs := fuse.NewLoopbackFileSystem(orig)
	timing := fuse.NewTimingPathFilesystem(fs)

	var opts fuse.PathFileSystemConnectorOptions

	opts.AttrTimeout = 1.0
	opts.EntryTimeout = 1.0
	opts.NegativeTimeout = 1.0

	fs.SetOptions(&opts)

	conn := fuse.NewPathFileSystemConnector(timing)
	state := fuse.NewMountState(conn)
	state.Debug = *debug

	mountPoint := flag.Arg(1)
	err := state.Mount(mountPoint)
	if err != nil {
		fmt.Printf("MountFuse fail: %v\n", err)
		os.Exit(1)
	}
	// TODO - figure out what a good value is.
	cpus := 1
	// 	cpus := fuse.CountCpus()
	if cpus > 1 {
		runtime.GOMAXPROCS(cpus)
	}

	fmt.Printf("Mounted %s on %s (threaded=%v, debug=%v, cpus=%v)\n", orig, mountPoint, *threaded, *debug, cpus)
	state.Loop(*threaded)
	fmt.Println("Finished", state.Stats())

	counts := state.OperationCounts()

	fmt.Println("Counts: ", counts)

	latency := state.Latencies()
	fmt.Println("Latency (ms):", latency)

	latency = timing.Latencies()
	fmt.Println("Path ops (ms):", latency)
}
