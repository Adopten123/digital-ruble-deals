/** Small valid PDFs with deterministic xref offsets; no production documents. */
export function syntheticPdf(text = 'Synthetic acceptance test', active = false): Buffer {
  const escaped = text.replace(/[\\()]/g, value => `\\${value}`);
  const stream = `BT /F1 12 Tf 30 100 Td (${escaped}) Tj ET\n`;
  const objects = [
    `<< /Type /Catalog /Pages 2 0 R${active ? ' /OpenAction 6 0 R' : ''} >>`,
    '<< /Type /Pages /Kids [3 0 R] /Count 1 >>',
    '<< /Type /Page /Parent 2 0 R /MediaBox [0 0 300 200] /Resources << /Font << /F1 4 0 R >> >> /Contents 5 0 R >>',
    '<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>',
    `<< /Length ${Buffer.byteLength(stream)} >>\nstream\n${stream}endstream`,
    ...(active ? ['<< /S /JavaScript /JS (app.alert\\(1\\)) >>'] : []),
  ];
  let pdf = '%PDF-1.7\n';
  const offsets = [0];
  for (const [index, body] of objects.entries()) {
    offsets.push(Buffer.byteLength(pdf));
    pdf += `${index + 1} 0 obj\n${body}\nendobj\n`;
  }
  const xref = Buffer.byteLength(pdf);
  pdf += `xref\n0 ${offsets.length}\n0000000000 65535 f \n`;
  for (const offset of offsets.slice(1)) pdf += `${String(offset).padStart(10, '0')} 00000 n \n`;
  pdf += `trailer\n<< /Size ${offsets.length} /Root 1 0 R >>\nstartxref\n${xref}\n%%EOF\n`;
  return Buffer.from(pdf);
}
