package transfer

import (
	"errors"
	"io"
	"slices"
	"strings"
	"time"
)

// VirtualFile is a file or folder a drag offers that is not on this
// machine's disk, as a file of a remote host's or an attachment: the
// destination, a file manager, decides where it goes and reads its bytes
// through Open as it copies, with its own progress.
//
// Path is the file's place in the drag, "/"-separated and relative: a
// file dropped by itself is just its name, and the entries of a folder
// come after the folder's own entry with its name before theirs
// ("notes", "notes/a.md"). Dir marks a folder, which has no bytes. Size
// and ModTime, when known, are what the destination shows and keeps.
//
// Open is called once per copy, on any goroutine, possibly long after the
// drag ended, and possibly not at all; the destination closes what it
// returns when done or cancelled.
type VirtualFile struct {
	Path    string
	Dir     bool
	Size    int64
	ModTime time.Time
	Open    func() (io.ReadCloser, error)
}

// ErrVirtualFile means a virtual file is malformed: an empty, absolute or
// escaping path, or a file without Open.
var ErrVirtualFile = errors.New("transfer: invalid virtual file")

// VirtualFiles offers files and folders that are not on disk, for drags
// out to file managers. Windows offers them as virtual files
// (FileGroupDescriptorW and FileContents, which Explorer copies with its
// own progress, without blocking the source); other platforms offer
// nothing for them yet: offer a file list of local copies there instead.
// Other representations can be added to the result with WithItems.
func VirtualFiles(files ...VirtualFile) (Data, error) {
	for _, f := range files {
		p := f.Path
		if p == "" || strings.HasPrefix(p, "/") || strings.ContainsAny(p, "\\\x00") || (len(p) > 1 && p[1] == ':') {
			return Data{}, ErrVirtualFile
		}
		for _, part := range strings.Split(p, "/") {
			if part == "" || part == "." || part == ".." {
				return Data{}, ErrVirtualFile
			}
		}
		if !f.Dir && f.Open == nil {
			return Data{}, ErrVirtualFile
		}
	}
	return Data{virtual: slices.Clone(files)}, nil
}

// VirtualFiles returns the virtual files the data offers, nil for none.
func (d Data) VirtualFiles() []VirtualFile { return slices.Clone(d.virtual) }

// WithItems returns the data with items added after its own, keeping its
// virtual files.
func (d Data) WithItems(items ...Item) Data {
	return Data{items: append(slices.Clone(d.items), items...), virtual: slices.Clone(d.virtual)}
}
