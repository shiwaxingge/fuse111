package main

import (
	"flag"
	"fmt"
	"github.com/hanwen/go-fuse/fuse"
	"github.com/hanwen/go-fuse/zipfs"
	"io"
	"log"
	"os"
	"os/exec"
	"runtime"
	"runtime/pprof"
	"strings"
	"time"
)

var _ = log.Printf

func main() {
	// Scans the arg list and sets up flags
	debug := flag.Bool("debug", false, "print debugging messages.")
	profile := flag.String("profile", "", "record cpu profile.")
	mem_profile := flag.String("mem-profile", "", "record memory profile.")
	command := flag.String("run", "", "run this command after mounting.")
	ttl := flag.Float64("ttl", 1.0, "attribute/entry cache TTL.")
	flag.Parse()
	if flag.NArg() < 2 {
		fmt.Fprintf(os.Stderr, "usage: %s MOUNTPOINT ZIP-FILE\n", os.Args[0])
		os.Exit(2)
	}

	var profFile, memProfFile io.Writer
	var err error
	if *profile != "" {
		profFile, err = os.Create(*profile)
		if err != nil {
			log.Fatalf("os.Create: %v", err)
		}
	}
	if *mem_profile != "" {
		memProfFile, err = os.Create(*mem_profile)
		if err != nil {
			log.Fatalf("os.Create: %v", err)
		}
	}

	var fs fuse.NodeFileSystem
	fs, err = zipfs.NewArchiveFileSystem(flag.Arg(1))
	if err != nil {
		fmt.Fprintf(os.Stderr, "NewArchiveFileSystem failed: %v\n", err)
		os.Exit(1)
	}

	opts := &fuse.FileSystemOptions{
		AttrTimeout:  time.Duration(*ttl * float64(time.Second)),
		EntryTimeout: time.Duration(*ttl * float64(time.Second)),
	}
	state, _, err := fuse.MountNodeFileSystem(flag.Arg(0), fs, opts)
	if err != nil {
		fmt.Printf("Mount fail: %v\n", err)
		os.Exit(1)
	}

	state.SetDebug(*debug)
	runtime.GC()
	if profFile != nil {
		pprof.StartCPUProfile(profFile)
		defer pprof.StopCPUProfile()
	}

	if *command != "" {
		args := strings.Split(*command, " ")
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Stdout = os.Stdout
		cmd.Start()
	}

	state.Loop()
	if memProfFile != nil {
		pprof.WriteHeapProfile(memProfFile)
	}
}
