package extract

import (
	"os"
	"strings"
	"testing"
)

// testdata/returns.{pdf,docx}: synthetic documents made with reportlab (DejaVu
// font embedded) and python-docx; Russian and Kyrgyz lines, two PDF pages, a DOCX table.
var want = []string{
	"Правила возврата товара",
	"Клиент может вернуть товар надлежащего качества в течение 14 календарных дней.",
	"Возврат денег производится в течение 10 рабочих дней.",
	"Кардар товарды 14 күндүн ичинде кайтара алат.",
}

func TestDocx(t *testing.T) {
	data, err := os.ReadFile("testdata/returns.docx")
	if err != nil {
		t.Fatal(err)
	}
	text, err := Text(TypeDOCX, data)
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range want {
		if !strings.Contains(text, w) {
			t.Fatalf("missing %q in:\n%s", w, text)
		}
	}
	if !strings.Contains(text, "Срок 14 дней") {
		t.Fatalf("table cells must be kept:\n%s", text)
	}
	if !strings.Contains(text, "\n\n") {
		t.Fatal("paragraphs must be separated by an empty line (passages split there)")
	}
}

func TestPdf(t *testing.T) {
	data, err := os.ReadFile("testdata/returns.pdf")
	if err != nil {
		t.Fatal(err)
	}
	text, err := Text(TypePDF, data)
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range append(want, "Страница 2: скидка для новых клиентов не более 10 процентов.") {
		if !strings.Contains(text, w) {
			t.Fatalf("missing %q in:\n%s", w, text)
		}
	}
}

func TestRejects(t *testing.T) {
	if _, err := Text(TypePDF, []byte("not a pdf")); err == nil {
		t.Fatal("garbage is not a PDF")
	}
	if _, err := Text(TypeDOCX, []byte("PK not a zip")); err == nil {
		t.Fatal("garbage is not a DOCX")
	}
	if _, err := Text("image/png", []byte{1}); err != ErrUnsupported {
		t.Fatalf("unsupported type: %v", err)
	}
	if _, err := Text(TypeText, []byte("  \n\n ")); err != ErrNoText {
		t.Fatalf("blank text: %v", err)
	}
	if _, err := Text(TypeText, []byte{0xff, 0xfe}); err == nil {
		t.Fatal("text must be UTF-8")
	}
	got, _ := Text(TypeText, []byte("a  b\r\n\r\n\r\n c d "))
	if got != "a b\n\nc d" {
		t.Fatalf("tidy: %q", got)
	}
}
