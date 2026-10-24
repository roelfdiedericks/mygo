package terminal

import (
	"bytes"
	"compress/zlib"
	"encoding/base64"
	"fmt"

	"github.com/egoist/mygo/plugins/terminal/internal/vt"
)

// imageKey is a placement of an image as a Mirror placed it in the other
// terminal: row counts from the top of the scrollback, so that it stays
// as the screen scrolls.
type imageKey struct {
	img, id                    uint32
	row, col, xoff, yoff       int
	cols, rows                 int
	srcX, srcY, srcW, srcH, gr int
	z                          int32
	virtual                    bool
}

// mirroredImages are the images a Mirror sent of a screen, by id, at
// their generations, and the placements it made, with their ids in the
// other terminal.
type mirroredImages struct {
	gens   map[uint32]uint64
	placed map[imageKey]uint32
}

// images brings the other terminal's images of the screen that shows to
// this one's (Kitty graphics): it sends the pixels of those placed, once
// for each generation, places what shows that it has not, and deletes
// what went. A placement on a row of the scrollback is placed as the row
// goes through the other screen (scrollbackImages, write). Virtual
// placements go as they are, for the cells of Unicode placeholders, which
// frames draw as text. top is the screen's first row from the top of the
// scrollback.
func (m *Mirror) images(out *bytes.Buffer, term *vt.Terminal, alt bool, top, rows int) {
	st := &m.imgs[0]
	if alt {
		st = &m.imgs[1]
	}
	if st.gens == nil {
		st.gens, st.placed = map[uint32]uint64{}, map[imageKey]uint32{}
	}
	seen := map[imageKey]bool{}
	var place []imageKey
	term.Placements(func(pl vt.Placement) {
		if pl.Virtual {
			// Placed by its id, which the placeholder cells name.
			k := imageKey{img: pl.ImageID, id: pl.ID, cols: pl.ParamCols, rows: pl.ParamRows, z: pl.Z, virtual: true}
			seen[k] = true
			if _, ok := st.placed[k]; !ok {
				place = append(place, k)
			}
			return
		}
		if !pl.Visible {
			return
		}
		k := imageKey{
			img: pl.ImageID, id: pl.ID, row: top + pl.Row, col: pl.Col, xoff: pl.XOffset, yoff: pl.YOffset,
			cols: pl.ParamCols, rows: pl.ParamRows,
			srcX: pl.ParamSrcX, srcY: pl.ParamSrcY, srcW: pl.ParamSrcW, srcH: pl.ParamSrcH, gr: max(pl.Rows, 1), z: pl.Z,
		}
		seen[k] = true
		if _, ok := st.placed[k]; !ok && pl.Row >= 0 {
			place = append(place, k)
		}
	})
	// Images gone, or of new pixels, which the other terminal's placements
	// of them may not survive: placed again where they show.
	for id, gen := range st.gens {
		g := term.ImageGeneration(id)
		if g == gen {
			continue
		}
		if g == 0 {
			fmt.Fprintf(out, "\x1b_Ga=d,d=I,i=%d,q=2\x1b\\", id)
			delete(st.gens, id)
		}
		for k := range st.placed {
			if k.img == id {
				delete(st.placed, k)
				if g != 0 && seen[k] && (k.virtual || k.row >= top) {
					place = append(place, k)
				}
			}
		}
	}
	// Placements gone from where they would show; those that scrolled off
	// stay, as they do here.
	for k, id := range st.placed {
		if !seen[k] && (k.virtual || k.row+k.gr > top && k.row < top+rows) {
			fmt.Fprintf(out, "\x1b_Ga=d,d=i,i=%d,p=%d,q=2\x1b\\", k.img, id)
			delete(st.placed, k)
		}
	}
	for _, k := range place {
		gen := term.ImageGeneration(k.img)
		if gen == 0 {
			continue
		}
		if st.gens[k.img] != gen {
			img, ok := term.Image(k.img)
			if !ok {
				continue
			}
			sendImage(out, img)
			st.gens[k.img] = gen
		}
		if k.virtual {
			fmt.Fprintf(out, "\x1b_Ga=p,U=1,i=%d,p=%d,c=%d,r=%d,z=%d,q=2\x1b\\", k.img, k.id, k.cols, k.rows, k.z)
			st.placed[k] = k.id
			continue
		}
		m.placement++
		fmt.Fprintf(out, "\x1b[%d;%dH\x1b_Ga=p,i=%d,p=%d,X=%d,Y=%d,x=%d,y=%d,w=%d,h=%d,c=%d,r=%d,z=%d,C=1,q=2\x1b\\",
			k.row-top+1, k.col+1, k.img, m.placement, k.xoff, k.yoff, k.srcX, k.srcY, k.srcW, k.srcH, k.cols, k.rows, k.z)
		st.placed[k] = m.placement
	}
}

// scrollbackImages returns the placements of images on rows of the
// scrollback from from up to to, by row, that the other terminal lacks:
// those a frame never saw on the screen, as when a Mirror starts over,
// or when rows went to the scrollback faster than frames came. The
// viewport is at the bottom, so a placement's row is top + its row from
// the viewport's top.
func (m *Mirror) scrollbackImages(top, from, to int) map[int][]imageKey {
	st := &m.imgs[0]
	if st.gens == nil {
		st.gens, st.placed = map[uint32]uint64{}, map[imageKey]uint32{}
	}
	var out map[int][]imageKey
	m.t.term.Placements(func(pl vt.Placement) {
		row := top + pl.Row
		if pl.Virtual || row < from || row >= to {
			return
		}
		k := imageKey{
			img: pl.ImageID, id: pl.ID, row: row, col: pl.Col, xoff: pl.XOffset, yoff: pl.YOffset,
			cols: pl.ParamCols, rows: pl.ParamRows,
			srcX: pl.ParamSrcX, srcY: pl.ParamSrcY, srcW: pl.ParamSrcW, srcH: pl.ParamSrcH, gr: max(pl.Rows, 1), z: pl.Z,
		}
		if _, ok := st.placed[k]; ok {
			return
		}
		if out == nil {
			out = map[int][]imageKey{}
		}
		out[row] = append(out[row], k)
	})
	return out
}

// placeOnRow places images on the row the cursor is on, as write drew
// it: the cursor goes to each one's column and comes back.
func (m *Mirror) placeOnRow(out *bytes.Buffer, ks []imageKey) {
	st := &m.imgs[0]
	for _, k := range ks {
		if !m.sendPixels(out, m.t.term, st, k.img) {
			continue
		}
		m.placement++
		fmt.Fprintf(out, "\x1b7\x1b[%dG\x1b_Ga=p,i=%d,p=%d,X=%d,Y=%d,x=%d,y=%d,w=%d,h=%d,c=%d,r=%d,z=%d,C=1,q=2\x1b\\\x1b8",
			k.col+1, k.img, m.placement, k.xoff, k.yoff, k.srcX, k.srcY, k.srcW, k.srcH, k.cols, k.rows, k.z)
		st.placed[k] = m.placement
	}
}

// sendPixels sends the pixels of an image the other terminal does not
// have as of its generation, and reports whether it has them.
func (m *Mirror) sendPixels(out *bytes.Buffer, term *vt.Terminal, st *mirroredImages, id uint32) bool {
	gen := term.ImageGeneration(id)
	if gen == 0 {
		return false
	}
	if st.gens[id] != gen {
		img, ok := term.Image(id)
		if !ok {
			return false
		}
		sendImage(out, img)
		st.gens[id] = gen
	}
	return true
}

// sendImage writes the sequences that transmit an image's pixels, as
// RGBA compressed (o=z), in chunks.
func sendImage(out *bytes.Buffer, img vt.Image) {
	var z bytes.Buffer
	w := zlib.NewWriter(&z)
	w.Write(decoded(img).Pix)
	w.Close()
	data := base64.StdEncoding.EncodeToString(z.Bytes())
	const chunk = 4096
	for i := 0; i == 0 || i < len(data); i += chunk {
		part := data[i:min(i+chunk, len(data))]
		more := 0
		if i+chunk < len(data) {
			more = 1
		}
		if i == 0 {
			fmt.Fprintf(out, "\x1b_Ga=t,f=32,o=z,s=%d,v=%d,i=%d,q=2,m=%d;%s\x1b\\", img.Width, img.Height, img.ID, more, part)
		} else {
			fmt.Fprintf(out, "\x1b_Gm=%d;%s\x1b\\", more, part)
		}
	}
}
