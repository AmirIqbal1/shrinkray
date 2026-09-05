'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');
const {
  selectionKey,
  visibleMoviePaths,
  memberProgress,
  aggregateBatch,
} = require('../internal/dashboard/web/batch-ui.js');

test('selection keys include both media root and relative path', () => {
  assert.notEqual(selectionKey('movies', 'Film.mkv'), selectionKey('tv', 'Film.mkv'));
  assert.notEqual(selectionKey('movies', 'A/Film.mkv'), selectionKey('movies', 'B/Film.mkv'));
});

test('select all returns only visible file rows in display order', () => {
  const entries = [
    { type: 'directory', path: 'Classics' },
    { type: 'file', path: 'First.mkv' },
    { type: 'file', path: 'Folder/Second.mp4' },
    { type: 'file', path: 'First.mkv' },
  ];
  assert.deepEqual(visibleMoviePaths(entries), ['First.mkv', 'Folder/Second.mp4']);
});

test('batch progress treats completed, queued and running jobs independently', () => {
  const summary = aggregateBatch([
    { state: 'completed', progress_percent: 12 },
    { state: 'queued', progress_percent: 88 },
    { state: 'running', progress_percent: 29 },
  ]);
  assert.equal(memberProgress({ state: 'completed', progress_percent: 0 }), 100);
  assert.equal(memberProgress({ state: 'queued', progress_percent: 99 }), 0);
  assert.equal(memberProgress({ state: 'running', progress_percent: 43 }), 43);
  assert.equal(summary.completed, 1);
  assert.equal(summary.queued, 1);
  assert.equal(summary.running, 1);
  assert.equal(summary.progress, 43);
});

test('batch aggregate counts terminal outcomes and clamps progress', () => {
  const summary = aggregateBatch([
    { state: 'failed', progress_percent: 130 },
    { state: 'cancelled', progress_percent: -1 },
  ]);
  assert.equal(summary.failed, 1);
  assert.equal(summary.cancelled, 1);
  assert.equal(summary.progress, 50);
});
