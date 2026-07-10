// Package nzb parses NZB files and downloads and reassembles their binary
// content over NNTP, decoding yEnc article bodies. It builds with
// CGO_ENABLED=0 and depends only on the sibling go-newsgroups/nntp and
// go-newsgroups/yenc modules.
package nzb

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"sort"
	"strconv"

	"github.com/go-newsgroups/nntp"
	"github.com/go-newsgroups/yenc"
)

// Segment is one article segment of a file.
type Segment struct {
	Number    int    // segment order (1-based)
	Bytes     int    // declared byte size
	MessageID string // without angle brackets
}

// File is one file described by an NZB (a set of segments across groups).
type File struct {
	Subject  string
	Poster   string
	Date     int64 // unix seconds (from the date attribute)
	Groups   []string
	Segments []Segment
}

// NZB is a parsed .nzb document.
type NZB struct {
	Files []File
}

// xmlNZB, xmlFile and xmlSegment mirror the newzbin DTD for encoding/xml.
type xmlNZB struct {
	XMLName xml.Name  `xml:"nzb"`
	Files   []xmlFile `xml:"file"`
}

type xmlFile struct {
	Subject  string       `xml:"subject,attr"`
	Poster   string       `xml:"poster,attr"`
	Date     string       `xml:"date,attr"`
	Groups   []string     `xml:"groups>group"`
	Segments []xmlSegment `xml:"segments>segment"`
}

type xmlSegment struct {
	Bytes     int    `xml:"bytes,attr"`
	Number    int    `xml:"number,attr"`
	MessageID string `xml:",chardata"`
}

// latin1Reader transcodes an ISO-8859-1 (Latin-1) byte stream to UTF-8 so that
// encoding/xml can decode NZB documents that declare a non-UTF-8 charset.
type latin1Reader struct {
	r    io.Reader
	buf  [1]byte
	tail []byte // encoded bytes not yet delivered to the caller
}

func (l *latin1Reader) Read(p []byte) (int, error) {
	n := 0
	for n < len(p) {
		if len(l.tail) > 0 {
			c := copy(p[n:], l.tail)
			l.tail = l.tail[c:]
			n += c
			continue
		}
		m, err := l.r.Read(l.buf[:])
		if m > 0 {
			b := l.buf[0]
			if b < 0x80 {
				p[n] = b
				n++
			} else {
				enc := []byte{0xc0 | b>>6, 0x80 | b&0x3f}
				c := copy(p[n:], enc)
				n += c
				l.tail = enc[c:]
			}
		}
		if err != nil {
			return n, err
		}
	}
	return n, nil
}

// stripAngles removes a single pair of surrounding angle brackets from a
// message id if present.
func stripAngles(id string) string {
	if len(id) >= 2 && id[0] == '<' && id[len(id)-1] == '>' {
		return id[1 : len(id)-1]
	}
	return id
}

// bracketID wraps a bare message id in the angle brackets NNTP commands expect.
func bracketID(id string) string {
	return "<" + id + ">"
}

// Parse parses NZB XML (the newzbin DTD: <nzb><file subject poster date>
// <groups><group>…</group></groups><segments><segment bytes number>MSGID
// </segment></segments></file></nzb>). Angle brackets around segment message
// IDs are stripped. Malformed XML returns an error.
func Parse(data []byte) (*NZB, error) {
	var doc xmlNZB
	dec := xml.NewDecoder(bytes.NewReader(data))
	// NZB files commonly declare a non-UTF-8 charset (e.g. iso-8859-1); accept
	// it by transcoding Latin-1 bytes to UTF-8.
	dec.CharsetReader = func(_ string, r io.Reader) (io.Reader, error) {
		return &latin1Reader{r: r}, nil
	}
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("nzb: parse: %w", err)
	}

	out := &NZB{}
	for _, xf := range doc.Files {
		f := File{
			Subject: xf.Subject,
			Poster:  xf.Poster,
			Groups:  xf.Groups,
		}
		// date is unix seconds; 0 on parse failure.
		if n, err := strconv.ParseInt(xf.Date, 10, 64); err == nil {
			f.Date = n
		}
		for _, xs := range xf.Segments {
			f.Segments = append(f.Segments, Segment{
				Number:    xs.Number,
				Bytes:     xs.Bytes,
				MessageID: stripAngles(xs.MessageID),
			})
		}
		out.Files = append(out.Files, f)
	}
	return out, nil
}

// ArticleFetcher is the slice of *nntp.Conn needed to download segments (so
// tests inject a fake).
type ArticleFetcher interface {
	Article(msgIDorNum string) (*nntp.Article, error)
}

// DownloadFile fetches every segment of f (in segment order) via af,
// yEnc-decodes each article body, reassembles the parts into the complete file
// bytes, and returns the bytes plus the decoded file name (from the yEnc
// header). Segments are placed by their yEnc Begin offset for multipart files;
// single-part files are concatenated in segment order. A fetch or decode error
// aborts with a wrapped error.
func DownloadFile(ctx context.Context, af ArticleFetcher, f File) (data []byte, name string, err error) {
	segs := make([]Segment, len(f.Segments))
	copy(segs, f.Segments)
	sort.Slice(segs, func(i, j int) bool { return segs[i].Number < segs[j].Number })

	var buf []byte
	for _, seg := range segs {
		if err := ctx.Err(); err != nil {
			return nil, "", fmt.Errorf("nzb: download segment %d: %w", seg.Number, err)
		}
		art, ferr := af.Article(bracketID(seg.MessageID))
		if ferr != nil {
			return nil, "", fmt.Errorf("nzb: fetch segment %d: %w", seg.Number, ferr)
		}
		part, derr := yenc.Decode([]byte(art.Body))
		if derr != nil {
			return nil, "", fmt.Errorf("nzb: decode segment %d: %w", seg.Number, derr)
		}
		if name == "" && part.Name != "" {
			name = part.Name
		}
		if part.Begin > 0 {
			// Multipart: place the part at its 1-based Begin offset.
			end := int(part.End)
			if end > len(buf) {
				grown := make([]byte, end)
				copy(grown, buf)
				buf = grown
			}
			copy(buf[part.Begin-1:], part.Data)
		} else {
			// Single-part: concatenate in segment order.
			buf = append(buf, part.Data...)
		}
	}
	return buf, name, nil
}
