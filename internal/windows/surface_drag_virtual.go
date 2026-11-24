//go:build windows && (amd64 || arm64)

package windows

import (
	"encoding/binary"
	"io"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"unsafe"

	"github.com/egoist/mygo/internal/schemestream"
	"github.com/egoist/mygo/transfer"
)

// Virtual files (transfer.VirtualFiles) are offered as the shell's
// FileGroupDescriptorW, which names them with their sizes and times, and
// FileContents, an IStream of each file's bytes by its index, which the
// destination reads as it copies.
//
// Explorer copies them on a thread of its own when the data object says
// it can (IDataObjectAsyncCapability): the drop returns at once, and
// Explorer shows its own progress. The streams live in the process's
// multithreaded apartment, so that Explorer's reads come on COM's threads
// and wait for the bytes there, never on the UI thread, which the data
// object's apartment is.

const (
	fdAttributes   = 0x4
	fdWritesTime   = 0x20
	fdFileSize     = 0x40
	fdProgressUI   = 0x4000
	fdUnicode      = 0x80000000
	fileAttrDir    = 0x10
	fileAttrNormal = 0x80
	fileDescSize   = 592 // FILEDESCRIPTORW
	coinitMulti    = 0x0
	// virtualPipeLimit is how far a file's bytes are read ahead of the
	// destination.
	virtualPipeLimit = 4 << 20
)

var (
	cfFileDescriptorW = uint16(registerClipboardFormat("FileGroupDescriptorW"))
	cfFileContents    = uint16(registerClipboardFormat("FileContents"))

	iidIDataObjectAsyncCapability = guid("3d8b0590-f691-11d2-8ea9-006097df5bd4")

	procCoMarshalInterThreadInterfaceInStream = ole32.NewProc("CoMarshalInterThreadInterfaceInStream")
	procCoGetInterfaceAndReleaseStream        = ole32.NewProc("CoGetInterfaceAndReleaseStream")

	dragAsyncVtbl [8]uintptr

	mtaOnce  sync.Once
	mtaCalls chan func()
)

// dragAsync is the state of IDataObjectAsyncCapability.
type dragAsync struct {
	on, inOperation bool
}

// initDragAsync makes the vtbl of IDataObjectAsyncCapability, the second
// interface of a data object that offers virtual files, at its asyncVtbl
// field: its callbacks get that field's address.
func initDragAsync() {
	main := func(this uintptr) uintptr { return this - unsafe.Offsetof(dragDataObject{}.asyncVtbl) }
	dragAsyncVtbl = [8]uintptr{
		syscall.NewCallback(func(this, riid, out uintptr) uintptr { return comCall(main(this), 0, riid, out) }),
		syscall.NewCallback(func(this uintptr) uintptr { return comCall(main(this), 1) }),
		syscall.NewCallback(func(this uintptr) uintptr { return comCall(main(this), 2) }),
		syscall.NewCallback(func(this, do uintptr) uintptr { // SetAsyncMode
			dragObjects[main(this)].async.on = uint32(do) != 0
			return sOK
		}),
		syscall.NewCallback(func(this, out uintptr) uintptr { // GetAsyncMode
			*(*int32)(native(out)) = boolInt(dragObjects[main(this)].async.on)
			return sOK
		}),
		syscall.NewCallback(func(this, ctx uintptr) uintptr { // StartOperation
			dragObjects[main(this)].async.inOperation = true
			return sOK
		}),
		syscall.NewCallback(func(this, out uintptr) uintptr { // InOperation
			*(*int32)(native(out)) = boolInt(dragObjects[main(this)].async.inOperation)
			return sOK
		}),
		syscall.NewCallback(func(this, hr, ctx, effects uintptr) uintptr { // EndOperation
			dragObjects[main(this)].async.inOperation = false
			return sOK
		}),
	}
}

func boolInt(b bool) int32 {
	if b {
		return 1
	}
	return 0
}

// fileGroupDescriptor is the FileGroupDescriptorW of files.
func fileGroupDescriptor(files []transfer.VirtualFile) []byte {
	b := make([]byte, 4+len(files)*fileDescSize)
	binary.LittleEndian.PutUint32(b, uint32(len(files)))
	for i, f := range files {
		d := b[4+i*fileDescSize:][:fileDescSize]
		flags := uint32(fdAttributes | fdProgressUI | fdUnicode)
		attrs := uint32(fileAttrNormal)
		if f.Dir {
			attrs = fileAttrDir
		} else {
			flags |= fdFileSize
			binary.LittleEndian.PutUint32(d[64:], uint32(uint64(f.Size)>>32))
			binary.LittleEndian.PutUint32(d[68:], uint32(f.Size))
		}
		if !f.ModTime.IsZero() {
			flags |= fdWritesTime
			ft := syscall.NsecToFiletime(f.ModTime.UnixNano())
			binary.LittleEndian.PutUint32(d[56:], ft.LowDateTime)
			binary.LittleEndian.PutUint32(d[60:], ft.HighDateTime)
		}
		binary.LittleEndian.PutUint32(d[0:], flags)
		binary.LittleEndian.PutUint32(d[36:], attrs)
		name := syscall.StringToUTF16(strings.ReplaceAll(f.Path, "/", `\`))
		if len(name) > 260 {
			name = append(name[:259], 0)
		}
		for k, c := range name {
			binary.LittleEndian.PutUint16(d[72+2*k:], c)
		}
	}
	return b
}

// fileContentsFormat reports whether f asks for the bytes of one of the
// data object's virtual files, as a stream.
func (d *dragDataObject) fileContentsFormat(f formatEtc) (transfer.VirtualFile, bool) {
	if f.Format != cfFileContents || f.Aspect != dvaspectContent || f.Tymed&tymedStream == 0 {
		return transfer.VirtualFile{}, false
	}
	if f.Index < 0 || int(f.Index) >= len(d.virtual) || d.virtual[f.Index].Dir {
		return transfer.VirtualFile{}, false
	}
	return d.virtual[f.Index], true
}

// fileContents is the stream of a virtual file's bytes, for the medium
// GetData answers: a proxy, on the calling UI thread, to a stream in the
// multithreaded apartment, which a goroutine fills from the file's Open.
func fileContents(f transfer.VirtualFile) (uintptr, bool) {
	pipe := schemestream.NewPipe(virtualPipeLimit, nil)
	go func() {
		rc, err := f.Open()
		if err != nil {
			pipe.Fail(err)
			return
		}
		defer rc.Close()
		buf := make([]byte, 256<<10)
		for {
			n, err := rc.Read(buf)
			if n > 0 {
				if pipe.Write(buf[:n]) != nil {
					return // the destination is done with it
				}
			}
			if err == io.EOF {
				pipe.Close()
				return
			}
			if err != nil {
				pipe.Fail(err)
				return
			}
		}
	}()
	stream := newFileStream(pipe, f.Size)
	var marshaled uintptr
	var hr uintptr
	onMTA(func() {
		hr, _, _ = procCoMarshalInterThreadInterfaceInStream.Call(uintptr(unsafe.Pointer(&iidIStream)), stream, uintptr(unsafe.Pointer(&marshaled)))
	})
	if failed(hr) {
		// Explorer reads the stream through the UI thread then, which
		// waits for each of its reads.
		return stream, true
	}
	var proxy uintptr
	hr, _, _ = procCoGetInterfaceAndReleaseStream.Call(marshaled, uintptr(unsafe.Pointer(&iidIStream)), uintptr(unsafe.Pointer(&proxy)))
	release(stream) // the apartment's stub holds it now
	if failed(hr) {
		return 0, false
	}
	return proxy, true
}

// onMTA runs fn on a thread of the process's multithreaded apartment,
// which that thread keeps for as long as the process runs.
func onMTA(fn func()) {
	mtaOnce.Do(func() {
		mtaCalls = make(chan func())
		go func() {
			runtime.LockOSThread()
			procCoInitializeEx.Call(0, coinitMulti)
			for call := range mtaCalls {
				call()
			}
		}()
	})
	done := make(chan struct{})
	mtaCalls <- func() { fn(); close(done) }
	<-done
}
