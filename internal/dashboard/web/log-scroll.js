(function exposeLogScroll(root, factory) {
  const helpers = factory();
  if (typeof module === 'object' && module.exports) module.exports = helpers;
  root.ShrinkrayLogScroll = helpers;
}(typeof globalThis === 'undefined' ? this : globalThis, () => {
  const DEFAULT_BOTTOM_THRESHOLD = 32;

  function finiteNonNegative(value) {
    const number = Number(value);
    return Number.isFinite(number) ? Math.max(0, number) : 0;
  }

  function captureLogScroll(log, threshold = DEFAULT_BOTTOM_THRESHOLD) {
    if (!log) return null;
    const scrollTop = finiteNonNegative(log.scrollTop);
    const scrollHeight = finiteNonNegative(log.scrollHeight);
    const clientHeight = finiteNonNegative(log.clientHeight);
    const distanceFromBottom = Math.max(0, scrollHeight - scrollTop - clientHeight);
    return {
      scrollTop,
      scrollHeight,
      clientHeight,
      distanceFromBottom,
      followingBottom: distanceFromBottom <= finiteNonNegative(threshold),
    };
  }

  function restoredScrollTop(snapshot, scrollHeight, clientHeight) {
    const newScrollHeight = finiteNonNegative(scrollHeight);
    const newClientHeight = finiteNonNegative(clientHeight);
    const maximum = Math.max(0, newScrollHeight - newClientHeight);
    if (!snapshot || snapshot.followingBottom) return maximum;
    return Math.min(maximum, Math.max(0, newScrollHeight - newClientHeight - finiteNonNegative(snapshot.distanceFromBottom)));
  }

  function restoreLogScroll(log, snapshot, contentChanged) {
    if (!log || !contentChanged) return;
    log.scrollTop = restoredScrollTop(snapshot, log.scrollHeight, log.clientHeight);
  }

  return {
    DEFAULT_BOTTOM_THRESHOLD,
    captureLogScroll,
    restoredScrollTop,
    restoreLogScroll,
  };
}));
