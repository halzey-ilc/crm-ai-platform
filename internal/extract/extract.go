// Package extract turns uploaded company documents into plain text for the
// Knowledge Assistant: plain text, DOCX (Office Open XML, read with the standard
// library) and PDF (text layer only; scanned pages have none and need OCR).
//
// The text is what the passages and the model's verbatim quotes are made of,
// so whitespace is normalised but nothing is rewritten.
package extract

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/ledongthuc/pdf"
)

// Content types accepted for upload.
const (
	TypeText = "text/plain"
	TypeDOCX = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	TypePDF  = "application/pdf"
)

// ErrNoText means the file has no extractable text (e.g. a scanned PDF).
var ErrNoText = errors.New("extract: the file has no text layer")

// ErrUnsupported means the content type is not one of the accepted ones.
var ErrUnsupported = errors.New("extract: unsupported content type (text/plain, docx, pdf)")

// Text returns the document's text.
func Text(contentType string, data []byte) (string, error) {
	var (
		text string
		err  error
	)
	switch contentType {
	case TypeText:
		if !utf8.Valid(data) {
			return "", errors.New("extract: text must be UTF-8")
		}
		text = string(data)
	case TypeDOCX:
		text, err = docxText(data)
	case TypePDF:
		text, err = pdfText(data)
	default:
		return "", ErrUnsupported
	}
	if err != nil {
		return "", err
	}
	text = tidy(text)
	if text == "" {
		return "", ErrNoText
	}
	return text, nil
}

// tidy trims each line, collapses runs of spaces and keeps at most one empty line
// between paragraphs (passages are split on empty lines).
func tidy(s string) string {
	s = strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), " ", " ")
	var out []string
	blank := false
	for _, line := range strings.Split(s, "\n") {
		line = strings.Join(strings.Fields(line), " ")
		if line == "" {
			if !blank && len(out) > 0 {
				out = append(out, "")
			}
			blank = true
			continue
		}
		out = append(out, line)
		blank = false
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

// docxText reads word/document.xml: text runs (w:t), tabs, line breaks, and an
// empty line after every paragraph (w:p), table cells included.
func docxText(data []byte) (string, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", errors.New("extract: not a DOCX file")
	}
	var doc *zip.File
	for _, f := range zr.File {
		if f.Name == "word/document.xml" {
			doc = f
		}
	}
	if doc == nil {
		return "", errors.New("extract: DOCX without word/document.xml")
	}
	if doc.UncompressedSize64 > 64<<20 {
		return "", errors.New("extract: DOCX text part too large")
	}
	rc, err := doc.Open()
	if err != nil {
		return "", err
	}
	defer rc.Close()
	dec := xml.NewDecoder(io.LimitReader(rc, 64<<20))
	var b strings.Builder
	inText := false
	cells := 0 // depth of table cells: a row reads as one line, its cells joined by spaces
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", fmt.Errorf("extract: DOCX XML: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "t":
				inText = true
			case "tc":
				cells++
			case "tab":
				b.WriteByte(' ')
			case "br", "cr":
				b.WriteByte('\n')
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "t":
				inText = false
			case "p":
				if cells > 0 {
					b.WriteByte(' ')
				} else {
					b.WriteString("\n\n")
				}
			case "tc":
				cells--
			case "tr":
				b.WriteString("\n\n")
			}
		case xml.CharData:
			if inText {
				b.Write(t)
			}
		}
	}
	return b.String(), nil
}

// pdfText reads the text layer page by page, an empty line between pages.
func pdfText(data []byte) (text string, err error) {
	// The PDF parser panics on some malformed files; a bad upload must not take
	// the gateway down.
	defer func() {
		if r := recover(); r != nil {
			text, err = "", errors.New("extract: unreadable PDF")
		}
	}()
	r, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", errors.New("extract: not a PDF file (or encrypted)")
	}
	var b strings.Builder
	for i := 1; i <= r.NumPage(); i++ {
		page := r.Page(i)
		if page.V.IsNull() {
			continue
		}
		rows, err := page.GetTextByRow()
		if err != nil {
			return "", errors.New("extract: unreadable PDF page")
		}
		for _, row := range rows {
			for _, w := range row.Content {
				b.WriteString(w.S)
			}
			b.WriteByte('\n')
		}
		b.WriteString("\n")
	}
	return b.String(), nil
}
