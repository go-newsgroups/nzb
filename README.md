# nzb

[![CI](https://github.com/go-newsgroups/nzb/actions/workflows/ci.yml/badge.svg)](https://github.com/go-newsgroups/nzb/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/go-newsgroups/nzb.svg)](https://pkg.go.dev/github.com/go-newsgroups/nzb)
[![License: BSD-3-Clause](https://img.shields.io/badge/License-BSD--3--Clause-blue.svg)](LICENSE)

A pure-Go library to **parse NZB files** and **download and reassemble** their
binary content over NNTP, decoding [yEnc](https://en.wikipedia.org/wiki/YEnc)
article bodies. It builds with `CGO_ENABLED=0` and depends only on the sibling
[`go-newsgroups/nntp`](https://github.com/go-newsgroups/nntp) and
[`go-newsgroups/yenc`](https://github.com/go-newsgroups/yenc) modules — no other
third-party dependencies.

`Parse` reads the newzbin NZB DTD (`<nzb><file><groups><group>…`
`<segments><segment>…`), stripping the angle brackets around segment message
IDs and accepting the non-UTF-8 charset declarations real NZB files often carry.
`DownloadFile` fetches each segment in order through any `ArticleFetcher`
(satisfied by `*nntp.Conn`), yEnc-decodes it, and tiles the parts by their yEnc
`begin` offset for multipart posts (or concatenates single-part posts).

## Install

```sh
go get github.com/go-newsgroups/nzb
```

Requires Go 1.26.4 or newer.

## Usage

```go
package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/go-newsgroups/nntp"
	"github.com/go-newsgroups/nzb"
)

func main() {
	// Parse an .nzb document.
	raw, err := os.ReadFile("release.nzb")
	if err != nil {
		log.Fatal(err)
	}
	doc, err := nzb.Parse(raw)
	if err != nil {
		log.Fatal(err)
	}

	// Connect to a news server (any *nntp.Conn satisfies nzb.ArticleFetcher).
	ctx := context.Background()
	conn, err := nntp.Dial(ctx, "news.example.org")
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close()

	// Download and reassemble each file.
	for _, f := range doc.Files {
		data, name, err := nzb.DownloadFile(ctx, conn, f)
		if err != nil {
			log.Fatal(err)
		}
		if err := os.WriteFile(name, data, 0o644); err != nil {
			log.Fatal(err)
		}
		fmt.Printf("wrote %s (%d bytes)\n", name, len(data))
	}
}
```

## API

| Symbol | Purpose |
| ------ | ------- |
| `Parse(data []byte) (*NZB, error)` | Parse NZB XML into files, groups and segments |
| `NZB`, `File`, `Segment` | The parsed document tree |
| `ArticleFetcher` | Interface (satisfied by `*nntp.Conn`) that fetches an article |
| `DownloadFile(ctx, af, f) ([]byte, string, error)` | Download, decode and reassemble one file |

## License

BSD-3-Clause. See [LICENSE](LICENSE). Copyright the go-newsgroups/nzb authors.
