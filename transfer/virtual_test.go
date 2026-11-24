package transfer

import (
	"errors"
	"io"
	"strings"
	"testing"
)

// TestVirtualFiles: paths must be relative and stay inside the drag, a
// file needs Open, and the files survive Snapshot and WithItems without
// being formats of the data.
func TestVirtualFiles(t *testing.T) {
	open := func() (io.ReadCloser, error) { return io.NopCloser(strings.NewReader("x")), nil }
	for _, bad := range []VirtualFile{
		{Path: "", Open: open}, {Path: "/etc/passwd", Open: open}, {Path: `a\b`, Open: open},
		{Path: "C:x", Open: open}, {Path: "a/../../b", Open: open}, {Path: "a//b", Open: open}, {Path: "a.txt"},
	} {
		if _, err := VirtualFiles(bad); !errors.Is(err, ErrVirtualFile) {
			t.Errorf("%+v: %v, want ErrVirtualFile", bad.Path, err)
		}
	}
	d, err := VirtualFiles(VirtualFile{Path: "docs", Dir: true}, VirtualFile{Path: "docs/a.md", Size: 1, Open: open})
	if err != nil {
		t.Fatal(err)
	}
	d = d.WithItems(NewItem(Bytes(Text, []byte("docs")))).Snapshot()
	if files := d.VirtualFiles(); len(files) != 2 || files[1].Path != "docs/a.md" {
		t.Errorf("files %+v", files)
	}
	if f := d.Formats(); len(f) != 1 || f[0] != Text {
		t.Errorf("formats %v, want only text", f)
	}
}
