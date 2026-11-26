package simplepdf

import (
	"os"
	"strings"
	"testing"
)

func TestGenerateValidationAndMissingFont(t *testing.T) {
	for _, v := range [][3]string{{"", "", ""}, {"", "one", "two"}, {"", strings.Repeat("x", MaxText+1), ""}, {"", "\xff", ""}} {
		if _, e := Generate(v[0], v[1], v[2]); e == nil {
			t.Fatal("invalid input accepted")
		}
	}
	if _, e := generate("", "text", "", "/missing/dejavu.ttf", "/missing/mono.ttf"); e == nil || !strings.Contains(e.Error(), "DejaVu") {
		t.Fatal("missing font error:", e)
	}
}
func TestGenerateBoundsAndFonts(t *testing.T) {
	if _, e := os.Stat("/usr/share/fonts/truetype/dejavu/DejaVuSans.ttf"); e != nil {
		t.Skip("DejaVu absent")
	}
	if _, e := Generate("", "😀", ""); e == nil {
		t.Fatal("silently dropped unsupported character")
	}
	data, e := Generate("Тест", "", "# Заголовок\n- пункт\n```\nкод\n```")
	if e != nil {
		t.Fatal(e)
	}
	for _, want := range []string{"%PDF-1.7", "/FontFile2", "/ToUnicode", "/DejaVuSansMono", "/Subtype /Type0", "%%EOF"} {
		if !strings.Contains(string(data), want) {
			t.Error("missing", want)
		}
	}
	if len(data) > 4<<20 {
		t.Fatal("oversized PDF")
	}
}
