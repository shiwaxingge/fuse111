package fuse

import (
	"bytes"
	"fmt"
	"log"
	"strings"
	"unsafe"

	"github.com/hanwen/go-fuse/raw"
)

func (req *request) Discard() {
	req.pool.FreeBuffer(req.flatData)
	req.pool.FreeBuffer(req.bufferPoolInputBuf)
}

// The largest input without data is 128 (setattr). This also fits small
// requests with short filenames.
const SMALL_BUF_THRESHOLD = 128

type request struct {
	// the input, if obtained through bufferpool
	bufferPoolInputBuf []byte
	pool               BufferPool

	// If we have a small input, we quickly copy it to here,
	// and give back the large buffer to buffer pool.
	smallInputBuf [SMALL_BUF_THRESHOLD]byte
	
	inputBuf []byte

	// These split up inputBuf.
	inHeader  *raw.InHeader      // generic header
	inData    unsafe.Pointer // per op data
	arg       []byte         // flat data.
	
	filenames []string       // filename arguments

	// Unstructured data, a pointer to the relevant XxxxOut struct.
	outData  unsafe.Pointer
	status   Status
	flatData []byte

	// Header + structured data for what we send back to the kernel.
	// May be followed by flatData.
	outHeaderBytes []byte

	// Start timestamp for timing info.
	startNs    int64
	preWriteNs int64

	// All information pertaining to opcode of this request.
	handler *operationHandler
}

func (me *request) InputDebug() string {
	val := " "
	if me.handler.DecodeIn != nil {
		val = fmt.Sprintf(" data: %v ", me.handler.DecodeIn(me.inData))
	}

	names := ""
	if me.filenames != nil {
		names = fmt.Sprintf("names: %v", me.filenames)
	}

	if len(me.arg) > 0 {
		names += fmt.Sprintf(" %d bytes", len(me.arg))
	}

	return fmt.Sprintf("Dispatch: %s, NodeId: %v.%v%v",
		operationName(me.inHeader.Opcode), me.inHeader.NodeId, val, names)
}

func (me *request) OutputDebug() string {
	var val interface{}
	if me.handler.DecodeOut != nil && me.outData != nil {
		val = me.handler.DecodeOut(me.outData)
	}

	dataStr := ""
	if val != nil {
		dataStr = fmt.Sprintf("%v", val)
	}

	max := 1024
	if len(dataStr) > max {
		dataStr = dataStr[:max] + fmt.Sprintf(" ...trimmed (response size %d)", len(me.outHeaderBytes))
	}

	flatStr := ""
	if len(me.flatData) > 0 {
		if me.handler.FileNameOut {
			s := strings.TrimRight(string(me.flatData), "\x00")
			flatStr = fmt.Sprintf(" %q", s)
		} else {
			flatStr = fmt.Sprintf(" %d bytes data\n", len(me.flatData))
		}
	}

	return fmt.Sprintf("Serialize: %s code: %v value: %v%v",
		operationName(me.inHeader.Opcode), me.status, dataStr, flatStr)
}


func (me *request) parse() {
	inHSize := int(unsafe.Sizeof(raw.InHeader{}))
	if len(me.inputBuf) < inHSize {
		log.Printf("Short read for input header: %v", me.inputBuf)
		return
	}

	// We return the input buffer early if possible. Only write
	// requests require a large input buffer, so if we hang onto
	// the large buffer, we create unnecessary memory pressure.
	if len(me.inputBuf) < SMALL_BUF_THRESHOLD {
		copy(me.smallInputBuf[:], me.inputBuf)
		me.inputBuf = me.smallInputBuf[:len(me.inputBuf)]
		me.pool.FreeBuffer(me.bufferPoolInputBuf)
		me.bufferPoolInputBuf = nil
	}
	
	me.inHeader = (*raw.InHeader)(unsafe.Pointer(&me.inputBuf[0]))
	me.arg = me.inputBuf[inHSize:]

	me.handler = getHandler(me.inHeader.Opcode)
	if me.handler == nil {
		log.Printf("Unknown opcode %d", me.inHeader.Opcode)
		me.status = ENOSYS
		return
	}

	if len(me.arg) < int(me.handler.InputSize) {
		log.Printf("Short read for %v: %v", operationName(me.inHeader.Opcode), me.arg)
		me.status = EIO
		return
	}

	if me.handler.InputSize > 0 {
		me.inData = unsafe.Pointer(&me.arg[0])
		me.arg = me.arg[me.handler.InputSize:]
	}

	count := me.handler.FileNames
	if count > 0 {
		if count == 1 {
			me.filenames = []string{string(me.arg[:len(me.arg)-1])}
		} else {
			names := bytes.SplitN(me.arg[:len(me.arg)-1], []byte{0}, count)
			me.filenames = make([]string, len(names))
			for i, n := range names {
				me.filenames[i] = string(n)
			}
			if len(names) != count {
				log.Println("filename argument mismatch", names, count)
				me.status = EIO
			}
		}
	}
}

func (me *request) serialize() {
	dataLength := me.handler.OutputSize
	if me.outData == nil || me.status > OK {
		dataLength = 0
	}

	sizeOfOutHeader := unsafe.Sizeof(raw.OutHeader{})

	me.outHeaderBytes = make([]byte, sizeOfOutHeader+dataLength)
	outHeader := (*raw.OutHeader)(unsafe.Pointer(&me.outHeaderBytes[0]))
	outHeader.Unique = me.inHeader.Unique
	outHeader.Status = int32(-me.status)
	outHeader.Length = uint32(
		int(sizeOfOutHeader) + int(dataLength) + int(len(me.flatData)))

	copy(me.outHeaderBytes[sizeOfOutHeader:], asSlice(me.outData, dataLength))
}
