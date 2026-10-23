import assert from 'node:assert/strict';
import test from 'node:test';
import { formatRubles, parseKopecks, rublesToKopecks, validateBalances } from '../src/lib/money.ts';

test('money remains exact above the JavaScript safe integer boundary', () => {
  assert.equal(formatRubles('9223372036854775807'), '92\u00a0233\u00a0720\u00a0368\u00a0547\u00a0758,07\u00a0₽');
  assert.equal(parseKopecks('9007199254740993'), 9007199254740993n);
  assert.equal(formatRubles('1'), '0,01\u00a0₽');
  assert.equal(formatRubles('50000000'), '500\u00a0000,00\u00a0₽');
  assert.equal(formatRubles('0'), '0,00\u00a0₽');
});

test('money rejects noncanonical values and overflow', () => {
  for (const value of ['-1', '+1', '00', '01', '1.0', '1e3', ' 1', '', '9223372036854775808']) {
    assert.throws(() => parseKopecks(value), Error, value);
  }
  assert.throws(() => parseKopecks(50 as unknown as string));
});

test('inconsistent or negative available balances are not displayed as valid', () => {
  assert.doesNotThrow(() => validateBalances('50000000', '30000000', '20000000'));
  assert.doesNotThrow(() => validateBalances('0', '0', '0'));
  assert.throws(() => validateBalances('50000000', '30000000', '20000001'));
  assert.throws(() => validateBalances('100', '101', '0'));
});

test('typed rubles convert exactly without rounding, including the int64 boundary', () => {
  assert.equal(rublesToKopecks('300000,00'), '30000000');
  assert.equal(rublesToKopecks(' 0.01 '), '1');
  assert.equal(rublesToKopecks('1,2'), '120');
  assert.equal(rublesToKopecks('90071992547409,93'), '9007199254740993');
  assert.equal(rublesToKopecks('92233720368547758,07'), '9223372036854775807');
  for (const value of ['0', '0,00', '-1', '+1', '1e4', '1,234', '1.', '01', '1 000', '1,2.3', '92233720368547758,08']) {
    assert.throws(() => rublesToKopecks(value), Error, value);
  }
});
