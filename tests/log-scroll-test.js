'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');
const {
  captureLogScroll,
  restoreLogScroll,
  restoredScrollTop,
} = require('../internal/dashboard/web/log-scroll.js');

function fakeLog({ scrollTop, scrollHeight, clientHeight }) {
  let top = scrollTop;
  let assignments = 0;
  return {
    get scrollTop() { return top; },
    set scrollTop(value) { top = value; assignments += 1; },
    scrollHeight,
    clientHeight,
    get assignments() { return assignments; },
  };
}

test('user at bottom follows a newly appended line', () => {
  const log = fakeLog({ scrollTop: 800, scrollHeight: 1000, clientHeight: 200 });
  const snapshot = captureLogScroll(log);
  log.scrollHeight = 1080;
  restoreLogScroll(log, snapshot, true);
  assert.equal(log.scrollTop, 880);
});

test('new job log defaults to following the bottom', () => {
  const log = fakeLog({ scrollTop: 0, scrollHeight: 640, clientHeight: 160 });
  restoreLogScroll(log, null, true);
  assert.equal(log.scrollTop, 480);
});

test('user away from bottom preserves reading position when a line arrives', () => {
  const log = fakeLog({ scrollTop: 400, scrollHeight: 1000, clientHeight: 200 });
  const snapshot = captureLogScroll(log);
  log.scrollHeight = 1080;
  restoreLogScroll(log, snapshot, true);
  assert.equal(log.scrollTop, 480);
  assert.equal(log.scrollHeight - log.scrollTop - log.clientHeight, 400);
});

test('unchanged logs do not receive a scrollTop assignment', () => {
  const log = fakeLog({ scrollTop: 400, scrollHeight: 1000, clientHeight: 200 });
  restoreLogScroll(log, captureLogScroll(log), false);
  assert.equal(log.scrollTop, 400);
  assert.equal(log.assignments, 0);
});

test('poll with no running job leaves the terminal untouched', () => {
  const log = fakeLog({ scrollTop: 375, scrollHeight: 900, clientHeight: 180 });
  restoreLogScroll(log, captureLogScroll(log), false);
  assert.equal(log.scrollTop, 375);
  assert.equal(log.assignments, 0);
});

test('completed job poll leaves the terminal untouched', () => {
  const log = fakeLog({ scrollTop: 525, scrollHeight: 1100, clientHeight: 220 });
  restoreLogScroll(log, captureLogScroll(log), false);
  assert.equal(log.scrollTop, 525);
  assert.equal(log.assignments, 0);
});

test('scrolling back near the bottom resumes follow mode', () => {
  const log = fakeLog({ scrollTop: 773, scrollHeight: 1000, clientHeight: 200 });
  const snapshot = captureLogScroll(log);
  assert.equal(snapshot.followingBottom, true);
  log.scrollHeight = 1120;
  restoreLogScroll(log, snapshot, true);
  assert.equal(log.scrollTop, 920);
});

test('progress and ETA-only refresh does not move the log', () => {
  const log = fakeLog({ scrollTop: 250, scrollHeight: 800, clientHeight: 160 });
  restoreLogScroll(log, captureLogScroll(log), false);
  assert.equal(log.scrollTop, 250);
  assert.equal(log.assignments, 0);
});

test('two jobs maintain independent distance from bottom', () => {
  const first = fakeLog({ scrollTop: 300, scrollHeight: 900, clientHeight: 200 });
  const second = fakeLog({ scrollTop: 700, scrollHeight: 900, clientHeight: 200 });
  const firstSnapshot = captureLogScroll(first);
  const secondSnapshot = captureLogScroll(second);
  first.scrollHeight = 1000;
  second.scrollHeight = 1000;
  restoreLogScroll(first, firstSnapshot, true);
  restoreLogScroll(second, secondSnapshot, true);
  assert.equal(first.scrollTop, 400);
  assert.equal(second.scrollTop, 800);
});

test('changed scrollHeight preserves distance from bottom and clamps', () => {
  const snapshot = captureLogScroll(fakeLog({ scrollTop: 500, scrollHeight: 1200, clientHeight: 200 }));
  assert.equal(restoredScrollTop(snapshot, 1500, 250), 750);
  assert.equal(restoredScrollTop(snapshot, 300, 250), 0);
});

test('empty or missing logs do not throw', () => {
  assert.equal(captureLogScroll(null), null);
  assert.doesNotThrow(() => restoreLogScroll(null, null, false));
  const log = fakeLog({ scrollTop: 0, scrollHeight: 0, clientHeight: 0 });
  assert.doesNotThrow(() => restoreLogScroll(log, null, true));
  assert.equal(log.scrollTop, 0);
});
