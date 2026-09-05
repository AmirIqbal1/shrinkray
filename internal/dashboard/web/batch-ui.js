(function (root, factory) {
  const api = factory();
  if (typeof module === 'object' && module.exports) module.exports = api;
  if (root) root.ShrinkrayBatchUI = api;
}(typeof window !== 'undefined' ? window : globalThis, function () {
  function selectionKey(rootID, path) {
    return `${String(rootID || '')}\u0000${String(path || '')}`;
  }

  function visibleMoviePaths(entries) {
    const result = [];
    const seen = new Set();
    (Array.isArray(entries) ? entries : []).forEach((entry) => {
      if (!entry || entry.type !== 'file' || typeof entry.path !== 'string' || !entry.path || seen.has(entry.path)) return;
      seen.add(entry.path);
      result.push(entry.path);
    });
    return result;
  }

  function memberProgress(job) {
    if (!job || typeof job !== 'object') return 0;
    if (job.state === 'completed') return 100;
    if (job.state === 'queued') return 0;
    const progress = Number(job.progress_percent);
    if (!Number.isFinite(progress)) return 0;
    return Math.max(0, Math.min(100, progress));
  }

  function aggregateBatch(jobs) {
    const members = Array.isArray(jobs) ? jobs : [];
    const counts = { queued: 0, running: 0, completed: 0, failed: 0, cancelled: 0 };
    let progress = 0;
    members.forEach((job) => {
      if (Object.prototype.hasOwnProperty.call(counts, job?.state)) counts[job.state]++;
      progress += memberProgress(job);
    });
    return {
      size: members.length,
      ...counts,
      progress: members.length ? progress / members.length : 0,
    };
  }

  return { selectionKey, visibleMoviePaths, memberProgress, aggregateBatch };
}));
