const MAX_KOPECKS = 9223372036854775807n;

/** Parse typed rubles with no floating-point conversion or rounding. */
export function rublesToKopecks(value: string): string {
  const normalized = value.trim();
  if (!/^(0|[1-9][0-9]*)([.,][0-9]{1,2})?$/.test(normalized)) {
    throw new Error('Введите сумму в рублях без пробелов, до двух знаков после запятой.');
  }
  const [whole, fraction = ''] = normalized.split(/[.,]/);
  const amount = BigInt(whole) * 100n + BigInt(fraction.padEnd(2, '0') || '0');
  if (amount <= 0n) throw new Error('Сумма должна быть больше нуля.');
  if (amount > MAX_KOPECKS) throw new Error('Сумма превышает допустимый предел: 92 233 720 368 547 758,07 ₽.');
  return amount.toString();
}

/** Accept only the canonical nonnegative int64 decimal strings from our API. */
export function parseKopecks(value: string): bigint {
  if (typeof value !== 'string' || !/^(0|[1-9][0-9]*)$/.test(value)) {
    throw new Error('Некорректный формат суммы в ответе сервера.');
  }
  const amount = BigInt(value);
  if (amount > MAX_KOPECKS) {
    throw new Error('Сумма в ответе сервера превышает допустимый предел.');
  }
  return amount;
}

export function formatRubles(value: string): string {
  const amount = parseKopecks(value);
  const whole = (amount / 100n).toString().replace(/\B(?=(\d{3})+(?!\d))/g, '\u00a0');
  const fractional = (amount % 100n).toString().padStart(2, '0');
  return `${whole},${fractional}\u00a0₽`;
}

export function validateBalances(total: string, reserved: string, available: string): void {
  if (parseKopecks(total) - parseKopecks(reserved) !== parseKopecks(available)) {
    throw new Error('Остатки в ответе сервера не согласованы. Обновите данные.');
  }
}
