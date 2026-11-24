//go:build windows && (amd64 || arm64)

package windows

import (
	"bytes"
	"encoding/binary"
	"io"
	"runtime"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"github.com/egoist/mygo/transfer"
)

// TestVirtualFiles: a drag's virtual files as Explorer reads them. The
// FileGroupDescriptorW names a folder and a file in it with its size and
// time; FileContents by the file's index is a stream, through the
// multithreaded apartment, of the bytes Open gives, read in full across
// the pipe's limit; a folder's index has none; and the data object says
// it can be copied asynchronously.
func TestVirtualFiles(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	dropOnce.Do(initDropTarget)

	payload := bytes.Repeat([]byte("0123456789abcdef"), (virtualPipeLimit+12345)/16)
	mod := time.Date(2024, 5, 6, 7, 8, 9, 0, time.UTC)
	opened := 0
	data, err := transfer.VirtualFiles(
		transfer.VirtualFile{Path: "docs", Dir: true},
		transfer.VirtualFile{Path: "docs/notes.md", Size: int64(len(payload)), ModTime: mod, Open: func() (io.ReadCloser, error) {
			opened++
			return io.NopCloser(bytes.NewReader(payload)), nil
		}},
	)
	if err != nil {
		t.Fatal(err)
	}
	object := newTransferData(data, "", false, false)
	defer release(object)

	f := nativeFormat(cfFileDescriptorW)
	var medium stgMedium
	if hr := comCall(object, dataGetData, uintptr(unsafe.Pointer(&f)), uintptr(unsafe.Pointer(&medium))); failed(hr) {
		t.Fatal(hresultError("GetData FileGroupDescriptorW", hr))
	}
	p, _, _ := procGlobalLock.Call(medium.Handle)
	desc := bytes.Clone(unsafe.Slice((*byte)(native(p)), 4+2*fileDescSize))
	procGlobalUnlock.Call(medium.Handle)
	procReleaseStgMedium.Call(uintptr(unsafe.Pointer(&medium)))
	name := func(i int) string {
		u := unsafe.Slice((*uint16)(unsafe.Pointer(&desc[4+i*fileDescSize+72])), 260)
		return syscall.UTF16ToString(u)
	}
	file := desc[4+fileDescSize:]
	ft := syscall.Filetime{LowDateTime: binary.LittleEndian.Uint32(file[56:]), HighDateTime: binary.LittleEndian.Uint32(file[60:])}
	switch {
	case binary.LittleEndian.Uint32(desc) != 2:
		t.Fatalf("%d descriptors, want 2", binary.LittleEndian.Uint32(desc))
	case name(0) != "docs" || binary.LittleEndian.Uint32(desc[4+36:]) != fileAttrDir:
		t.Errorf("the folder: %q, attributes %#x", name(0), binary.LittleEndian.Uint32(desc[4+36:]))
	case name(1) != `docs\notes.md` || binary.LittleEndian.Uint32(file[68:]) != uint32(len(payload)):
		t.Errorf("the file: %q, size %d", name(1), binary.LittleEndian.Uint32(file[68:]))
	case ft.Nanoseconds() != mod.UnixNano():
		t.Errorf("the file's time: %v, want %v", time.Unix(0, ft.Nanoseconds()).UTC(), mod)
	}

	contents := formatEtc{Format: cfFileContents, Aspect: dvaspectContent, Index: 1, Tymed: tymedStream}
	if hr := comCall(object, dataQueryGetData, uintptr(unsafe.Pointer(&contents))); hr != sOK {
		t.Fatal(hresultError("QueryGetData FileContents", hr))
	}
	if hr := comCall(object, dataGetData, uintptr(unsafe.Pointer(&contents)), uintptr(unsafe.Pointer(&medium))); failed(hr) {
		t.Fatal(hresultError("GetData FileContents", hr))
	}
	if lookupBodyStream(medium.Handle) != nil {
		t.Error("FileContents is the stream itself, not a proxy to it in the multithreaded apartment")
	}
	got, err := readDragStream(medium.Handle)
	procReleaseStgMedium.Call(uintptr(unsafe.Pointer(&medium)))
	if err != nil || !bytes.Equal(got, payload) || opened != 1 {
		t.Fatalf("read %d bytes, %v, opened %d times; want %d bytes, once", len(got), err, opened, len(payload))
	}
	folder := formatEtc{Format: cfFileContents, Aspect: dvaspectContent, Index: 0, Tymed: tymedStream}
	if hr := comCall(object, dataQueryGetData, uintptr(unsafe.Pointer(&folder))); hr == sOK {
		t.Error("a folder has contents")
	}

	var async uintptr
	if hr := comCall(object, 0, uintptr(unsafe.Pointer(&iidIDataObjectAsyncCapability)), uintptr(unsafe.Pointer(&async))); failed(hr) || async == 0 {
		t.Fatal(hresultError("QueryInterface IDataObjectAsyncCapability", hr))
	}
	defer release(async)
	var on int32
	comCall(async, 4, uintptr(unsafe.Pointer(&on))) // GetAsyncMode
	if on == 0 {
		t.Error("the data object doesn't offer an asynchronous copy")
	}
}
