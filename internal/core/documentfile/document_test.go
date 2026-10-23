package documentfile

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"unicode/utf16"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

func sampleAct() Act {
	return Act{DealID: "deal-document-test", DealVersion: 7, Title: "Печать упаковки", Description: "Печать 1000 коробок. Макет и цвет утверждены сторонами.", CustomerName: "ООО Заказчик", SupplierName: "ООО Типография", AmountKopecks: "30000000", Currency: "RUB", DeadlineAt: "2027-01-01T12:30:00Z"}
}

func minimalPDF(catalog, page, extra string, content []byte) []byte {
	var data bytes.Buffer
	data.WriteString("%PDF-1.4\n")
	objects := []string{
		"<< /Type /Catalog /Pages 2 0 R " + catalog + " >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] /Resources << >> /Contents 4 0 R " + page + " >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%sendstream", len(content), content),
	}
	if extra != "" {
		objects = append(objects, extra)
	}
	offsets := []int{0}
	for i, o := range objects {
		offsets = append(offsets, data.Len())
		fmt.Fprintf(&data, "%d 0 obj\n%s\nendobj\n", i+1, o)
	}
	xref := data.Len()
	fmt.Fprintf(&data, "xref\n0 %d\n0000000000 65535 f \n", len(offsets))
	for _, offset := range offsets[1:] {
		fmt.Fprintf(&data, "%010d 00000 n \n", offset)
	}
	fmt.Fprintf(&data, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(offsets), xref)
	return data.Bytes()
}

func TestValidateRejectsUnsafeAndMalformedPDFs(t *testing.T) {
	good := minimalPDF("", "", "", []byte("q\nQ\n"))
	if err := Validate(good); err != nil {
		t.Fatalf("static PDF: %v", err)
	}
	tests := []struct {
		name string
		data []byte
		want error
	}{
		{"empty", nil, ErrInvalid},
		{"signature only", []byte("%PDF-1.4\nnot a PDF\n%%EOF"), ErrInvalid},
		{"oversize", bytes.Repeat([]byte("x"), MaxSize+1), ErrTooLarge},
		{"preamble", append([]byte("HTML"), good...), ErrInvalid},
		{"postamble", append(bytes.Clone(good), []byte("<html>script</html>")...), ErrInvalid},
		{"missing EOF", good[:len(good)-8], ErrInvalid},
		{"javascript", minimalPDF("/OpenAction 5 0 R", "", "<< /Type /Action /S /JavaScript /JS (app.alert\\(1\\)) >>", []byte("q\nQ\n")), ErrInvalid},
		{"escaped action name", minimalPDF("/Open#41ction 5 0 R", "", "<< /S /Java#53cript /J#53 (app.alert\\(1\\)) >>", []byte("q\nQ\n")), ErrInvalid},
		{"unreferenced action", minimalPDF("", "", "<< /S /Launch /F (file.exe) >>", []byte("q\nQ\n")), ErrInvalid},
		{"embedded file", minimalPDF("/Names << /EmbeddedFiles 5 0 R >>", "", "<< /Names [(payload) << /Type /Filespec /EF << /F 4 0 R >> >>] >>", []byte("q\nQ\n")), ErrInvalid},
		{"annotation URI", minimalPDF("", "/Annots [5 0 R]", "<< /Type /Annot /Subtype /Link /Rect [0 0 30 30] /A << /S /URI /URI (https://example.test) >> >>", []byte("q\nQ\n")), ErrInvalid},
		{"form", minimalPDF("/AcroForm << /Fields [] >>", "", "", []byte("q\nQ\n")), ErrInvalid},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := Validate(tt.data)
			if tt.want == nil {
				if err != nil {
					t.Fatal(err)
				}
			} else if !errors.Is(err, tt.want) {
				t.Fatalf("got %v, want %v", err, tt.want)
			}
		})
	}
	var encrypted bytes.Buffer
	conf := validationConfiguration()
	conf.UserPW = "secret"
	conf.OwnerPW = "owner-secret"
	if err := api.Encrypt(context.Background(), bytes.NewReader(good), &encrypted, conf); err != nil {
		t.Fatal(err)
	}
	if err := Validate(encrypted.Bytes()); !errors.Is(err, ErrInvalid) {
		t.Fatalf("encrypted: %v", err)
	}
	// Empty-user-password PDFs are still encrypted and intentionally unsupported.
	encrypted.Reset()
	conf.UserPW = ""
	if err := api.Encrypt(context.Background(), bytes.NewReader(good), &encrypted, conf); err != nil {
		t.Fatal(err)
	}
	if err := Validate(encrypted.Bytes()); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty-password encrypted: %v", err)
	}
}

func TestValidateBoundsDecodedStreams(t *testing.T) {
	var compressed bytes.Buffer
	z := zlib.NewWriter(&compressed)
	_, _ = z.Write(bytes.Repeat([]byte(" "), maxDecodedStream+1))
	_ = z.Close()
	data := minimalPDF("", "", "", []byte("q\nQ\n"))
	// Build a small input whose actual stream expands beyond the configured cap.
	data = bytes.Replace(data, []byte("/Length 4 >>\nstream\nq\nQ\n"), []byte(fmt.Sprintf("/Length %d /Filter /FlateDecode >>\nstream\n%s", compressed.Len(), compressed.Bytes())), 1)
	// Cross-reference offsets change after stream replacement; rebuild them.
	data = rebuildXref(data)
	if len(data) > MaxSize {
		t.Fatal("fixture itself is too large")
	}
	if err := Validate(data); !errors.Is(err, ErrInvalid) {
		t.Fatalf("decoded limit: %v", err)
	}
}

func TestValidateInspectsCompressedObjectsAndRejectsRepairs(t *testing.T) {
	conf := validationConfiguration()
	conf.WriteObjectStream = true
	conf.WriteXRefStream = true
	for _, active := range []bool{false, true} {
		input := minimalPDF("", "", "", []byte("% /JavaScript is just a comment, not an action\nq\nQ\n"))
		if active {
			input = minimalPDF("/OpenAction 5 0 R", "", "<< /S /JavaScript /JS (app.alert\\(1\\)) >>", []byte("q\nQ\n"))
		}
		var compacted bytes.Buffer
		if err := api.Optimize(context.Background(), bytes.NewReader(input), &compacted, conf, nil); err != nil {
			t.Fatal(err)
		}
		ctx, err := api.ReadContext(context.Background(), bytes.NewReader(compacted.Bytes()), conf)
		if err != nil {
			t.Fatal(err)
		}
		if !ctx.Read.UsingObjectStreams {
			t.Fatal("fixture must use compressed objects")
		}
		err = Validate(compacted.Bytes())
		if active && !errors.Is(err, ErrInvalid) {
			t.Fatalf("compressed active object accepted: %v", err)
		}
		if !active && err != nil {
			t.Fatalf("static compressed PDF: %v", err)
		}
	}
	valid := minimalPDF("", "", "", []byte("q\nQ\n"))
	// Alter the catalog offset, irrespective of the fixture's header length.
	start := bytes.Index(valid, []byte("xref\n"))
	lines := strings.Split(string(valid[start:]), "\n")
	lines[3] = "9999999999 00000 n "
	broken := append(bytes.Clone(valid[:start]), []byte(strings.Join(lines, "\n"))...)
	if err := Validate(broken); !errors.Is(err, ErrInvalid) {
		t.Fatalf("repaired xref accepted: %v", err)
	}
}

func rebuildXref(data []byte) []byte {
	cut := bytes.Index(data, []byte("xref\n"))
	prefix := bytes.Clone(data[:cut])
	var out bytes.Buffer
	out.Write(prefix)
	offsets := []int{0}
	for n := 1; n <= 4; n++ {
		offsets = append(offsets, bytes.Index(prefix, []byte(fmt.Sprintf("%d 0 obj\n", n))))
	}
	fmt.Fprint(&out, "xref\n0 5\n0000000000 65535 f \n")
	for _, offset := range offsets[1:] {
		fmt.Fprintf(&out, "%010d 00000 n \n", offset)
	}
	fmt.Fprintf(&out, "trailer\n<< /Size 5 /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(prefix))
	return out.Bytes()
}

func utf16BE(s string) []byte {
	values := utf16.Encode([]rune(s))
	out := make([]byte, len(values)*2)
	for i, v := range values {
		binary.BigEndian.PutUint16(out[i*2:], v)
	}
	return out
}

func TestGenerateIncludesCyrillicExactMoneyAndAllConditions(t *testing.T) {
	a := sampleAct()
	a.AmountKopecks = "9223372036854775807"
	a.Description = strings.Repeat("Условия печати упаковки должны сохраняться полностью. ", 80) + "КОНЕЦ УСЛОВИЙ"
	data, err := Generate(a)
	if err != nil {
		t.Fatal(err)
	}
	ctx, err := api.ReadContext(context.Background(), bytes.NewReader(data), validationConfiguration())
	if err != nil {
		t.Fatal(err)
	}
	if err := api.ValidateContext(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	if ctx.PageCount < 2 {
		t.Fatalf("long conditions must have several pages, got %d", ctx.PageCount)
	}
	var contents []byte
	embedded, toUnicode := false, false
	for _, entry := range ctx.Table {
		if entry == nil || entry.Free {
			continue
		}
		if d, ok := entry.Object.(types.Dict); ok {
			if _, ok := d["FontFile2"]; ok {
				embedded = true
			}
			if _, ok := d["ToUnicode"]; ok {
				toUnicode = true
			}
		}
	}
	for n := 1; n <= ctx.PageCount; n++ {
		page, _, _, err := ctx.PageDict(context.Background(), n, false)
		if err != nil {
			t.Fatal(err)
		}
		content, err := ctx.PageContent(page, n)
		if err != nil {
			t.Fatal(err)
		}
		contents = append(contents, content...)
	}
	if !embedded || !toUnicode {
		t.Fatalf("font not self-contained: embedded=%v unicode=%v", embedded, toUnicode)
	}
	for _, required := range []string{a.Title, a.CustomerName, a.SupplierName, "КОНЕЦ УСЛОВИЙ", "92233720368547758,07", "9223372036854775807", "Версия условий: 7"} {
		if !bytes.Contains(contents, utf16BE(required)) {
			t.Errorf("PDF omits exact terms: %q", required)
		}
	}
	if directory := os.Getenv("DOCUMENTFILE_PREVIEW_DIR"); directory != "" {
		if err := os.MkdirAll(directory, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(directory, "act-long.pdf"), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestGenerateRefusesUnrepresentableOrInvalidTerms(t *testing.T) {
	for _, amount := range []string{"0", "01", "1.5", "-1", "+1", "1e3", "9223372036854775808"} {
		a := sampleAct()
		a.AmountKopecks = amount
		if _, err := Generate(a); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%q: %v", amount, err)
		}
	}
	a := sampleAct()
	a.Title = "Нельзя молча потерять символ 🧪"
	if _, err := Generate(a); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unsupported glyph: %v", err)
	}
	for _, amount := range []string{"1", "100", "30000000", "9223372036854775807"} {
		if _, err := formatAmount(amount); err != nil {
			t.Fatalf("exact money %q: %v", amount, err)
		}
	}
}

func TestStoreConcurrentImmutablePrivateBlobsAndBoundedReads(t *testing.T) {
	directory := t.TempDir()
	store, err := NewStore(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	data := minimalPDF("", "", "", []byte("q\nQ\n"))
	const count = 12
	keys := make(chan string, count)
	failures := make(chan error, count)
	var wg sync.WaitGroup
	for n := 0; n < count; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			key, err := store.Put(data)
			if err != nil {
				failures <- err
				return
			}
			keys <- key
		}()
	}
	wg.Wait()
	close(keys)
	close(failures)
	for err := range failures {
		t.Error(err)
	}
	seen := map[string]bool{}
	for key := range keys {
		if !blobKey.MatchString(key) || seen[key] {
			t.Fatalf("invalid or reused key %q", key)
		}
		seen[key] = true
		read, err := store.Read(key)
		if err != nil || !bytes.Equal(data, read) {
			t.Fatalf("read %q: %v", key, err)
		}
	}
	if len(seen) != count {
		t.Fatalf("stored %d of %d", len(seen), count)
	}
	for _, key := range []string{"../secret.pdf", "..\\secret.pdf", "/secret.pdf", "C:\\secret.pdf", "a.pdf", "00000000000000000000000000000000.PDF", "00000000000000000000000000000000.pdf:stream"} {
		if _, err := store.Read(key); !errors.Is(err, ErrNotFound) {
			t.Fatalf("unsafe key %q: %v", key, err)
		}
	}
	if _, err := store.Put([]byte("%PDF-1.4\nfake\n%%EOF")); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid put: %v", err)
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != count {
		t.Fatalf("leaked temporary files: %v", entries)
	}
	oversize := "11111111111111111111111111111111.pdf"
	if err := os.WriteFile(filepath.Join(directory, oversize), bytes.Repeat([]byte("x"), MaxSize+1), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Read(oversize); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("oversize stored file: %v", err)
	}
	if _, err := store.Read("22222222222222222222222222222222.pdf"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing: %v", err)
	}
	// Reopening the same private directory preserves published immutable bytes.
	reopened, err := NewStore(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	for key := range seen {
		if _, err := reopened.Read(key); err != nil {
			t.Fatal(err)
		}
	}
}

func TestStoreRejectsSymlinksAndDirectories(t *testing.T) {
	directory := t.TempDir()
	store, err := NewStore(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	key := "33333333333333333333333333333333.pdf"
	if err := os.Mkdir(filepath.Join(directory, key), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Read(key); !errors.Is(err, ErrInvalid) {
		t.Fatalf("directory: %v", err)
	}
	outside := filepath.Join(t.TempDir(), "private.pdf")
	if err := os.WriteFile(outside, []byte("must not be read"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(directory, "44444444444444444444444444444444.pdf")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink not available on this host: %v", err)
	}
	if _, err := store.Read(filepath.Base(link)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("external symlink: %v", err)
	}
	if _, err := NewStore(link); err == nil {
		t.Fatal("symlink root accepted")
	}
}
