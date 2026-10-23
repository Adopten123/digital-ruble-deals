package documentfile

import (
	"bytes"
	"context"
	"fmt"
	"time"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

const (
	maxPages         = 100
	maxDecodedStream = 8 << 20
	maxDecodedTotal  = 32 << 20
	maxObjects       = 5000
)

func validationConfiguration() *model.Configuration {
	c := model.NewStatelessConfiguration()
	c.ValidationMode = model.ValidationStrict
	c.Cmd = model.VALIDATE
	c.Offline = true
	c.ValidateLinks = false
	c.LocalImageRoot = ""
	c.DecodeAllStreams = false
	c.Limits = model.ResourceLimits{
		MaxInputBytes:        MaxSize,
		MaxObjectBytes:       1 << 20,
		MaxStreamBytes:       MaxSize,
		MaxDecodeBytes:       maxDecodedStream,
		MaxImagePixels:       20_000_000,
		MaxImageBytes:        maxDecodedStream,
		MaxObjectCount:       maxObjects,
		MaxObjectStreamCount: maxObjects,
		MaxObjectStreamFirst: 1 << 20,
		MaxXRefEntries:       maxObjects,
		MaxRecursionDepth:    40,
	}
	return c
}

// Validate parses the cross-reference table, object graph and pages, then
// enforces a static-PDF subset. Encrypted files, forms, actions, annotations,
// embedded files, multimedia and external streams are intentionally unsupported.
// Parser limits and cancellation reduce resource exposure but do not constitute
// a complete process-memory budget or protection against every viewer defect.
func Validate(data []byte) (err error) {
	if len(data) > MaxSize {
		return ErrTooLarge
	}
	if len(data) < 20 || !bytes.HasPrefix(data, []byte("%PDF-")) || !bytes.HasSuffix(bytes.TrimSpace(data), []byte("%%EOF")) {
		return ErrInvalid
	}
	defer func() {
		if recover() != nil {
			err = ErrInvalid
		}
	}()
	c, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	ctx, err := api.ReadContext(c, bytes.NewReader(data), validationConfiguration())
	if err != nil {
		return fmt.Errorf("%w: structure", ErrInvalid)
	}
	if ctx.Encrypt != nil || ctx.E != nil || ctx.AdditionalStreams != nil {
		return fmt.Errorf("%w: encrypted or attached content", ErrInvalid)
	}
	if !ctx.ValidationReport().Empty() || ctx.Read.RepairOffset != 0 {
		return fmt.Errorf("%w: repaired PDF", ErrInvalid)
	}
	scanner := objectScanner{ctx: c}
	// Include unused objects: an inactive action must not become accepted merely
	// because one parser does not currently follow its reference.
	for _, entry := range ctx.Table {
		if entry == nil || entry.Free {
			continue
		}
		if err := scanner.object(entry.Object, 0); err != nil {
			return err
		}
	}
	if err := api.ValidateContext(c, ctx); err != nil {
		return fmt.Errorf("%w: validation: %v", ErrInvalid, err)
	}
	if !ctx.ValidationReport().Empty() {
		return fmt.Errorf("%w: compatibility repair", ErrInvalid)
	}
	if ctx.PageCount < 1 || ctx.PageCount > maxPages {
		return fmt.Errorf("%w: page count", ErrInvalid)
	}
	for n := 1; n <= ctx.PageCount; n++ {
		page, _, _, err := ctx.PageDict(c, n, false)
		if err != nil {
			return ErrInvalid
		}
		if _, err := ctx.PageContent(page, n); err != nil && err != model.ErrNoContent {
			return fmt.Errorf("%w: page content", ErrInvalid)
		}
	}
	if c.Err() != nil {
		return fmt.Errorf("%w: parsing deadline", ErrInvalid)
	}
	return nil
}

type objectScanner struct {
	ctx     context.Context
	decoded int
	visited int
}

var forbiddenKeys = map[string]bool{
	"OpenAction": true, "AA": true, "JS": true, "JavaScript": true,
	"AcroForm": true, "XFA": true, "EmbeddedFiles": true, "EF": true,
	"AF": true, "Collection": true, "Annots": true, "RichMediaContent": true,
	"RichMediaSettings": true, "PresSteps": true, "Requirements": true,
}
var forbiddenNames = map[string]bool{
	"JavaScript": true, "Launch": true, "GoToR": true, "GoToE": true,
	"URI": true, "SubmitForm": true, "ImportData": true, "Rendition": true,
	"Movie": true, "Sound": true, "RichMedia": true, "3D": true,
	"EmbeddedFile": true, "Filespec": true, "FileAttachment": true,
	"Action": true, "XFA": true,
	"GoTo": true, "Thread": true, "Hide": true, "Named": true,
	"SetOCGState": true, "Trans": true, "GoTo3DView": true,
}

func (s *objectScanner) object(obj types.Object, depth int) error {
	s.visited++
	if depth > 40 || s.visited > 100_000 || s.ctx.Err() != nil {
		return fmt.Errorf("%w: object limits", ErrInvalid)
	}
	switch v := obj.(type) {
	case types.Dict:
		for k, child := range v {
			// pdfcpu parses name escapes, including /J#53 and /Java#53cript.
			// fpdf emits an empty attachment-name tree even without attachments.
			// Only this exact direct empty dictionary is harmless; references,
			// Kids, names or any extra keys remain unsupported.
			if k == "EmbeddedFiles" && emptyFileTree(child) {
				continue
			}
			if forbiddenKeys[k] {
				return fmt.Errorf("%w: active or embedded content (%s)", ErrInvalid, k)
			}
			if err := s.object(child, depth+1); err != nil {
				return err
			}
		}
	case types.Array:
		for _, child := range v {
			if err := s.object(child, depth+1); err != nil {
				return err
			}
		}
	case types.Name:
		if forbiddenNames[v.Value()] {
			return fmt.Errorf("%w: unsupported PDF feature", ErrInvalid)
		}
	case types.StreamDict:
		return s.stream(&v, depth)
	case types.ObjectStreamDict:
		return s.stream(&v.StreamDict, depth)
	case types.XRefStreamDict:
		return s.stream(&v.StreamDict, depth)
	case types.LazyObjectStreamObject:
		decoded, err := v.DecodedObject(s.ctx)
		if err != nil {
			return ErrInvalid
		}
		return s.object(decoded, depth+1)
	case *types.LazyObjectStreamObject:
		decoded, err := v.DecodedObject(s.ctx)
		if err != nil {
			return ErrInvalid
		}
		return s.object(decoded, depth+1)
	}
	return nil
}

func emptyFileTree(obj types.Object) bool {
	d, ok := obj.(types.Dict)
	if !ok || len(d) != 1 {
		return false
	}
	names, ok := d["Names"].(types.Array)
	return ok && len(names) == 0
}

func (s *objectScanner) stream(v *types.StreamDict, depth int) error {
	if _, ok := v.Dict["F"]; ok {
		return fmt.Errorf("%w: external stream", ErrInvalid)
	}
	if err := s.object(v.Dict, depth+1); err != nil {
		return err
	}
	for _, f := range v.FilterPipeline {
		switch f.Name {
		case "FlateDecode", "LZWDecode", "ASCII85Decode", "ASCIIHexDecode", "RunLengthDecode", "DCTDecode":
		default:
			return fmt.Errorf("%w: unsupported stream filter", ErrInvalid)
		}
	}
	if err := v.DecodeWithLimit(maxDecodedStream); err != nil {
		return fmt.Errorf("%w: stream", ErrInvalid)
	}
	s.decoded += len(v.Content)
	if s.decoded > maxDecodedTotal {
		return fmt.Errorf("%w: total decoded content", ErrInvalid)
	}
	return nil
}
