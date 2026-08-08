'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');
const { buildEncoderOptions } = require('../internal/dashboard/web/encoder-options.js');

test('encoder selector always offers auto and software', () => {
  assert.deepEqual(
    buildEncoderOptions({ software: true, qsv: false, vaapi: false, nvenc: false }).map((option) => option.value),
    ['auto', 'software'],
  );
});

test('encoder selector offers only runtime-verified hardware backends', () => {
  assert.deepEqual(
    buildEncoderOptions({ software: true, qsv: true, vaapi: false, nvenc: true }).map((option) => option.value),
    ['auto', 'software', 'qsv', 'nvenc'],
  );
});

test('missing capability data does not expose hardware choices', () => {
  assert.deepEqual(buildEncoderOptions(null).map((option) => option.value), ['auto', 'software']);
});
