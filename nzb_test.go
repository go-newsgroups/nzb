package nzb

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/go-newsgroups/nntp"
	"github.com/go-newsgroups/yenc"
)

// fakeFetcher is a network-free ArticleFetcher backed by a lookup table.
type fakeFetcher struct {
	arts map[string]*nntp.Article
	errs map[string]error
}

func (f fakeFetcher) Article(msgIDorNum string) (*nntp.Article, error) {
	if e, ok := f.errs[msgIDorNum]; ok {
		return nil, e
	}
	if a, ok := f.arts[msgIDorNum]; ok {
		return a, nil
	}
	return nil, fmt.Errorf("no such article: %s", msgIDorNum)
}

// yencBytes encodes raw bytes into yEnc data characters (mirrors the encoder).
func yencBytes(data []byte) string {
	var b strings.Builder
	for _, d := range data {
		e := d + 42
		if e == 0x00 || e == 0x0a || e == 0x0d || e == 0x3d {
			b.WriteByte('=')
			b.WriteByte(e + 64)
			continue
		}
		b.WriteByte(e)
	}
	return b.String()
}

// multipartBody hand-crafts a multipart yEnc article body (no CRC trailer, so
// yenc.Decode does not verify a checksum).
func multipartBody(name string, part, total int, begin, end int64, data []byte) string {
	return fmt.Sprintf("=ybegin part=%d total=%d line=128 size=100 name=%s\r\n"+
		"=ypart begin=%d end=%d\r\n%s\r\n=yend size=%d part=%d\r\n",
		part, total, name, begin, end, yencBytes(data), len(data), part)
}

const sampleNZB = `<?xml version="1.0" encoding="iso-8859-1"?>
<nzb xmlns="http://www.newzbin.com/DTD/2003/nzb">
  <file poster="Poster &lt;p@ex.org&gt;" date="1071674882" subject="Here's the file">
    <groups>
      <group>alt.binaries.test</group>
      <group>alt.binaries.misc</group>
    </groups>
    <segments>
      <segment bytes="102400" number="1">&lt;part1@ex.org&gt;</segment>
      <segment bytes="102400" number="2">part2@ex.org</segment>
    </segments>
  </file>
</nzb>`

func TestParse(t *testing.T) {
	n, err := Parse([]byte(sampleNZB))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(n.Files) != 1 {
		t.Fatalf("want 1 file, got %d", len(n.Files))
	}
	f := n.Files[0]
	if f.Subject != "Here's the file" {
		t.Errorf("subject = %q", f.Subject)
	}
	if f.Poster != "Poster <p@ex.org>" {
		t.Errorf("poster = %q", f.Poster)
	}
	if f.Date != 1071674882 {
		t.Errorf("date = %d", f.Date)
	}
	if len(f.Groups) != 2 || f.Groups[0] != "alt.binaries.test" {
		t.Errorf("groups = %v", f.Groups)
	}
	if len(f.Segments) != 2 {
		t.Fatalf("want 2 segments, got %d", len(f.Segments))
	}
	// Angle brackets stripped (segment 1) and left bare (segment 2).
	if f.Segments[0].MessageID != "part1@ex.org" {
		t.Errorf("seg1 id = %q", f.Segments[0].MessageID)
	}
	if f.Segments[1].MessageID != "part2@ex.org" {
		t.Errorf("seg2 id = %q", f.Segments[1].MessageID)
	}
	if f.Segments[0].Bytes != 102400 || f.Segments[0].Number != 1 {
		t.Errorf("seg1 = %+v", f.Segments[0])
	}
}

// errReader yields one filled byte followed by a non-EOF error.
type errReader struct{ done bool }

func (e *errReader) Read(p []byte) (int, error) {
	if e.done {
		return 0, errors.New("read failed")
	}
	e.done = true
	p[0] = 'A'
	return 1, errors.New("read failed")
}

func TestLatin1Reader(t *testing.T) {
	// High byte 0xe9 ('é' in Latin-1) transcodes to two UTF-8 bytes, then EOF.
	l := &latin1Reader{r: bytes.NewReader([]byte{0xe9})}
	got, err := io.ReadAll(l)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(got) != "é" {
		t.Errorf("transcode = %q", got)
	}

	// A one-byte destination forces the pending-tail path across two Reads.
	l = &latin1Reader{r: bytes.NewReader([]byte{0xe9})}
	one := make([]byte, 1)
	n, _ := l.Read(one)
	if n != 1 || one[0] != 0xc3 {
		t.Fatalf("first read = %d %#x", n, one[0])
	}
	n, _ = l.Read(one)
	if n != 1 || one[0] != 0xa9 {
		t.Fatalf("tail read = %d %#x", n, one[0])
	}

	// A reader that returns data alongside an error propagates the error.
	l = &latin1Reader{r: &errReader{}}
	buf := make([]byte, 4)
	n, err = l.Read(buf)
	if n != 1 || buf[0] != 'A' || err == nil {
		t.Fatalf("errReader path = %d %q %v", n, buf[:n], err)
	}
}

func TestParseBadDate(t *testing.T) {
	n, err := Parse([]byte(`<nzb><file date="notanumber" subject="s"></file></nzb>`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if n.Files[0].Date != 0 {
		t.Errorf("bad date should yield 0, got %d", n.Files[0].Date)
	}
}

func TestParseMalformed(t *testing.T) {
	if _, err := Parse([]byte("<nzb><file")); err == nil {
		t.Fatal("expected error for malformed XML")
	}
}

func TestDownloadFileSinglePart(t *testing.T) {
	af := fakeFetcher{arts: map[string]*nntp.Article{
		"<s1@ex>": {Body: string(yenc.Encode("greeting.txt", []byte("HELLO"), 128))},
		"<s2@ex>": {Body: string(yenc.Encode("greeting.txt", []byte("WORLD"), 128))},
	}}
	f := File{Segments: []Segment{
		{Number: 2, MessageID: "s2@ex"},
		{Number: 1, MessageID: "s1@ex"}, // out of order → must be sorted
	}}
	data, name, err := DownloadFile(context.Background(), af, f)
	if err != nil {
		t.Fatalf("DownloadFile: %v", err)
	}
	if string(data) != "HELLOWORLD" {
		t.Errorf("data = %q", data)
	}
	if name != "greeting.txt" {
		t.Errorf("name = %q", name)
	}
}

func TestDownloadFileMultipart(t *testing.T) {
	// payload "HELLOWORLD"; seg #1 carries the tail (offset 6-10), seg #2 the
	// head (offset 1-5), exercising both the buffer-grow and no-grow branches.
	af := fakeFetcher{arts: map[string]*nntp.Article{
		"<b@ex>": {Body: multipartBody("out.bin", 2, 2, 6, 10, []byte("WORLD"))},
		"<a@ex>": {Body: multipartBody("out.bin", 1, 2, 1, 5, []byte("HELLO"))},
	}}
	f := File{Segments: []Segment{
		{Number: 1, MessageID: "b@ex"},
		{Number: 2, MessageID: "a@ex"},
	}}
	data, name, err := DownloadFile(context.Background(), af, f)
	if err != nil {
		t.Fatalf("DownloadFile: %v", err)
	}
	if string(data) != "HELLOWORLD" {
		t.Errorf("data = %q", data)
	}
	if name != "out.bin" {
		t.Errorf("name = %q", name)
	}
}

func TestDownloadFileEmpty(t *testing.T) {
	data, name, err := DownloadFile(context.Background(), fakeFetcher{}, File{})
	if err != nil {
		t.Fatalf("DownloadFile: %v", err)
	}
	if len(data) != 0 || name != "" {
		t.Errorf("empty file = %q / %q", data, name)
	}
}

func TestDownloadFileFetchError(t *testing.T) {
	sentinel := errors.New("boom")
	af := fakeFetcher{errs: map[string]error{"<x@ex>": sentinel}}
	f := File{Segments: []Segment{{Number: 1, MessageID: "x@ex"}}}
	_, _, err := DownloadFile(context.Background(), af, f)
	if !errors.Is(err, sentinel) {
		t.Fatalf("want wrapped sentinel, got %v", err)
	}
}

func TestDownloadFileDecodeError(t *testing.T) {
	af := fakeFetcher{arts: map[string]*nntp.Article{
		"<x@ex>": {Body: "not a yenc body"},
	}}
	f := File{Segments: []Segment{{Number: 1, MessageID: "x@ex"}}}
	_, _, err := DownloadFile(context.Background(), af, f)
	if err == nil || !strings.Contains(err.Error(), "decode segment 1") {
		t.Fatalf("want decode error, got %v", err)
	}
}

func TestDownloadFileContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	f := File{Segments: []Segment{{Number: 1, MessageID: "x@ex"}}}
	_, _, err := DownloadFile(ctx, fakeFetcher{}, f)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
}
