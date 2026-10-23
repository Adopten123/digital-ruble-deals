# PDF handling in the prototype

`Validate` accepts at most 5 MiB, 100 pages and 5,000 objects. It reads a real
cross-reference table and page graph using pinned **pdfcpu v0.16.3**, strict,
stateless and offline, with a three-second cooperative context deadline. There
is no shell command, external renderer, configuration-file discovery, URL fetch
or source-file execution in this backend package.

The prototype deliberately accepts a restricted static-PDF subset. Encrypted
files (including empty-password encryption), forms, annotations and hyperlinks,
JavaScript, actions, attachments, multimedia, external streams and unsupported
stream filters are refused. Reported compatibility repairs are also refused.
All parsed objects are inspected, including unused objects and compressed object
streams, rather than looking for literal keywords in raw bytes. An exact empty
attachment-name tree emitted by fpdf is allowed; it contains no file.

Limits include 1 MiB per object, 8 MiB per decoded stream and 32 MiB aggregate
decoded streams inspected by the policy scanner. These are input limits, **not
a complete process-memory budget or an antivirus guarantee**. Strict validation
implements the parser's checks and may still have bounded low-level recovery.
Authorized HTTP reads, SHA256 verification against document metadata and isolated
previews remain the responsibility of the feature / HTTP layers. Keep this
policy explicit when rejecting PDFs exported by other software; do not silently
retry in a relaxed parser mode.

`Generate` writes every supplied term using pinned **fpdf v0.12.0** and the
embedded **Go Regular** font from **x/image v0.47.0**. It supports Cyrillic,
wraps long text and words, and adds pages. An unsupported font glyph produces
an explicit error rather than removing a condition. Decimal integer kopecks are
formatted using integer quotient and remainder, including MaxInt64. Text and
currency are not interpreted as HTML, PDF instructions or paths. The generated
act prominently labels the test contour and distinguishes agreement, signature
and payment. Generation does not assert that work has actually been completed.

`Store` owns an `os.Root` handle; call `Close` when the application shuts down.
Only 128-bit random hexadecimal `.pdf` blob keys are accepted. `Put` validates,
writes a private temporary file, syncs it, and atomically publishes a hard link
with create-if-absent semantics. Existing files are never overwritten. Linux
also syncs the containing directory; Windows does not offer that operation via
an ordinary read-only directory handle. `Read` accepts only regular files inside
the root and enforces the input-size cap. Store names do not replace feature
authorization. A filesystem administrator can change files, so metadata hashes
must be checked when reading them.

A failed or uncertain database commit can leave an unreachable blob. `Store`
never deletes a published blob based on such an outcome. No automatic cleanup or
public static storage is provided. Keep the private directory on a filesystem
supporting hard links (the Linux Compose volume and local NTFS do).

The accompanying unit tests exercise actual parser / generator / filesystem
behavior: malformed and active PDFs, both encrypted variants, decoded-size
limits, Cyrillic and exact monetary content, multi-page output, unsupported
glyphs, concurrent writes, invalid paths and persistence across reopening.

Primary upstream documentation and release notes:

- [pdfcpu v0.16.3 security update](https://github.com/pdfcpu/pdfcpu/releases/tag/v0.16.3)
- [pdfcpu Go API](https://pdfcpu.io/getting_started/install_api/)
- [fpdf package and UTF-8 font API](https://pkg.go.dev/codeberg.org/go-pdf/fpdf@v0.12.0)
- [Embedded Go Regular font](https://pkg.go.dev/golang.org/x/image@v0.47.0/font/gofont/goregular)
- [Go font family](https://go.dev/blog/go-fonts)

License texts are retained in [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).
Include them with application distributions that contain these libraries and
the embedded font.
