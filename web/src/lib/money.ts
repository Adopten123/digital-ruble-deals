const MAX_KOPECKS = 9223372036854775807n;

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
