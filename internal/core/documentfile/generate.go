package documentfile

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"codeberg.org/go-pdf/fpdf"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/sfnt"
)

// Generate produces an educational act with an embedded, licensed Go font.
// Every condition is included; MultiCell wraps long words and adds pages.
func Generate(a Act) ([]byte, error) {
	if a.DealVersion < 1 || a.Currency != "RUB" {
		return nil, ErrInvalid
	}
	amount, err := formatAmount(a.AmountKopecks)
	if err != nil {
		return nil, err
	}
	deadline, err := time.Parse(time.RFC3339Nano, a.DeadlineAt)
	if err != nil {
		return nil, ErrInvalid
	}
	font, err := sfnt.Parse(goregular.TTF)
	if err != nil {
		return nil, fmt.Errorf("parse embedded font: %w", err)
	}
	for _, item := range []struct {
		value string
		max   int
	}{{a.DealID, 128}, {a.Title, 200}, {a.Description, 5000}, {a.CustomerName, 200}, {a.SupplierName, 200}} {
		if strings.TrimSpace(item.value) == "" || !utf8.ValidString(item.value) || utf8.RuneCountInString(item.value) > item.max {
			return nil, ErrInvalid
		}
		for _, r := range item.value {
			if r == '\n' || r == '\r' || r == '\t' {
				continue
			}
			if unicode.IsControl(r) {
				return nil, ErrInvalid
			}
			glyph, err := font.GlyphIndex(nil, r)
			if err != nil || glyph == 0 {
				return nil, fmt.Errorf("%w: character U+%04X is not supported by the educational-act font", ErrInvalid, r)
			}
		}
	}
	pdf := fpdf.New("P", "mm", "A4", "")
	pdf.SetMargins(18, 18, 18)
	pdf.SetAutoPageBreak(true, 20)
	pdf.AddUTF8FontFromBytes("GoRegular", "", goregular.TTF)
	pdf.SetFont("GoRegular", "", 10)
	pdf.SetTitle("ПоАкту — учебный акт", true)
	pdf.SetAuthor("ПоАкту", true)
	pdf.SetCreator("ПоАкту — тестовый контур", true)
	pdf.SetFooterFunc(func() {
		pdf.SetY(-14)
		pdf.SetFont("GoRegular", "", 8)
		pdf.CellFormat(0, 5, fmt.Sprintf("ПоАкту · Учебный документ · Страница %d", pdf.PageNo()), "", 0, "C", false, 0, "")
	})
	pdf.AddPage()
	write := func(label, value string) {
		pdf.SetFont("GoRegular", "", 10)
		text := strings.NewReplacer("\r\n", "\n", "\r", "\n", "\t", "    ").Replace(label + value)
		pdf.MultiCell(0, 5.5, text, "", "L", false)
		pdf.Ln(3)
	}
	pdf.SetFont("GoRegular", "", 17)
	pdf.MultiCell(0, 9, "Учебный акт приёмки работ", "", "C", false)
	pdf.Ln(4)
	write("", "Тестовый контур. Расчёты и ЭДО эмулируются. Этот акт не является квалифицированной электронной подписью и не подтверждает фактическое исполнение работ.")
	write("Сделка: ", a.DealID)
	write("Версия условий: ", strconv.FormatInt(a.DealVersion, 10))
	write("Заказчик: ", a.CustomerName)
	write("Исполнитель: ", a.SupplierName)
	write("Наименование работ: ", a.Title)
	write("Условия и описание работ:\n", a.Description)
	write("Полная сумма расчёта: ", amount+" ₽ ("+a.AmountKopecks+" копеек), валюта "+a.Currency)
	write("Срок: ", deadline.UTC().Format(time.RFC3339Nano))
	write("", "Документ подготовлен для согласования указанной версии условий. Согласование условий, тестовая подпись и выплата являются разными действиями. Загрузка этого файла и согласие одной стороны не запускают расчёт.")
	var output bytes.Buffer
	if err := pdf.Output(&output); err != nil {
		return nil, fmt.Errorf("generate PDF: %w", err)
	}
	if err := Validate(output.Bytes()); err != nil {
		return nil, fmt.Errorf("generated PDF: %w", err)
	}
	return output.Bytes(), nil
}

func formatAmount(s string) (string, error) {
	if len(s) == 0 || len(s) > 19 || s[0] == '0' {
		return "", ErrInvalid
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return "", ErrInvalid
		}
	}
	amount, err := strconv.ParseInt(s, 10, 64)
	if err != nil || amount <= 0 {
		return "", ErrInvalid
	}
	// Quotient/remainder keep even MaxInt64 kopecks exact.
	return fmt.Sprintf("%d,%02d", amount/100, amount%100), nil
}
