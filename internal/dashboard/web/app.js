const state = {
  currentPath: '',
  selected: null,
  jobs: [],
  expandedLogJobs: new Set(),
  knownLogJobs: new Set(),
};
const $ = (selector) => document.querySelector(selector);

function formatBytes(bytes) {
  if (!Number.isFinite(bytes) || bytes < 1) return '0 B';
  const units = ['B', 'KB', 'MB', 'GB', 'TB'];
  const index = Math.min(Math.floor(Math.log(bytes) / Math.log(1024)), units.length - 1);
  return `${(bytes / (1024 ** index)).toFixed(index > 1 ? 1 : 0)} ${units[index]}`;
}

function formatDuration(seconds) {
  const total = Math.max(0, Math.round(seconds || 0));
  const hours = Math.floor(total / 3600);
  const mins = Math.floor((total % 3600) / 60);
  const secs = total % 60;
  return hours ? `${hours}h ${mins}m` : `${mins}m ${secs}s`;
}

function formatElapsed(seconds) {
  const mins = Math.floor(seconds / 60);
  const secs = seconds % 60;
  return mins ? `${mins}m ${secs}s` : `${secs}s`;
}

function formatQueued(value) {
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? 'unknown time' : date.toLocaleString([], { dateStyle: 'medium', timeStyle: 'short' });
}

function escapeHTML(value) {
  const node = document.createElement('span');
  node.textContent = String(value ?? '');
  return node.innerHTML;
}

async function api(url, options = {}) {
  const response = await fetch(url, options);
  const data = await response.json().catch(() => ({}));
  if (!response.ok) throw new Error(data.error || `Request failed (${response.status})`);
  return data;
}

function showNotice(message, kind = 'error') {
  const notice = $('#notice');
  notice.textContent = message;
  notice.className = `notice ${kind}`;
  notice.hidden = false;
  clearTimeout(showNotice.timer);
  showNotice.timer = setTimeout(() => { notice.hidden = true; }, 6000);
}

async function loadHealth() {
  try {
    const health = await api('/api/health');
    $('#server-status').textContent = `Online · v${health.version}`;
    $('#movie-root').textContent = health.root;
    $('#movie-root').title = health.root;
  } catch (error) {
    $('#server-status').textContent = 'Offline';
    showNotice(error.message);
  }
}

async function loadFiles(folder = '') {
  try {
    const response = await api(`/api/files?path=${encodeURIComponent(folder)}`);
    const listing = response && typeof response === 'object' ? response : {};
    const entries = Array.isArray(listing.entries)
      ? listing.entries.filter((entry) => entry && typeof entry === 'object')
      : [];
    state.currentPath = typeof listing.path === 'string' ? listing.path : '';
    renderBreadcrumbs();
    const list = $('#file-list');
    if (!entries.length) {
      list.innerHTML = '<div class="empty">No supported movies or folders here.</div>';
      return;
    }
    list.innerHTML = entries.map((entry) => `
      <button class="file-row ${entry.type}" data-path="${escapeHTML(entry.path)}" data-type="${entry.type}">
        <span class="file-icon" aria-hidden="true">${entry.type === 'directory' ? '⌑' : '▶'}</span>
        <span class="file-name">${escapeHTML(entry.name)}</span>
        ${entry.type === 'file' ? `<span class="file-size">${formatBytes(entry.size)}</span>` : '<span class="file-open">Open</span>'}
        <span class="chevron" aria-hidden="true">›</span>
      </button>`).join('');
  } catch (error) {
    showNotice(error.message);
  }
}

function renderBreadcrumbs() {
  const parts = state.currentPath ? state.currentPath.split('/') : [];
  let accumulated = '';
  const crumbs = ['<button data-path="">Movies</button>'];
  parts.forEach((part) => {
    accumulated = accumulated ? `${accumulated}/${part}` : part;
    crumbs.push(`<span>›</span><button data-path="${escapeHTML(accumulated)}">${escapeHTML(part)}</button>`);
  });
  $('#breadcrumbs').innerHTML = crumbs.join('');
}

async function selectMovie(path) {
  $('#movie-details').innerHTML = '<div class="details-placeholder">Inspecting movie…</div>';
  try {
    state.selected = await api(`/api/probe?path=${encodeURIComponent(path)}`);
    const movie = state.selected;
    $('#details-hint').textContent = movie.filename;
    $('#movie-details').innerHTML = `
      <div class="selected-movie"><span class="movie-icon">▶</span><div><strong>${escapeHTML(movie.filename)}</strong><small>${escapeHTML(movie.path)}</small></div></div>
      <dl class="detail-grid">
        <div><dt>File size</dt><dd>${formatBytes(movie.size)}</dd></div>
        <div><dt>Duration</dt><dd>${formatDuration(movie.duration_seconds)}</dd></div>
        <div><dt>Resolution</dt><dd>${movie.width} × ${movie.height}</dd></div>
        <div><dt>Video codec</dt><dd>${escapeHTML(movie.video_codec).toUpperCase()}</dd></div>
        <div><dt>Audio tracks</dt><dd>${movie.audio_tracks}</dd></div>
        <div><dt>Subtitle tracks</dt><dd>${movie.subtitle_tracks}</dd></div>
      </dl>`;
    $('#settings-fieldset').disabled = false;
    updateTarget();
    document.querySelectorAll('.file-row').forEach((row) => row.classList.toggle('selected', row.dataset.path === path));
  } catch (error) {
    state.selected = null;
    $('#settings-fieldset').disabled = true;
    showNotice(error.message);
  }
}

function selectedPreset() {
  return document.querySelector('input[name="preset"]:checked')?.value || 'balanced';
}

function updateTarget() {
  if (!state.selected) return;
  const preset = selectedPreset();
  const percentages = { balanced: 0.6, smaller: 0.4, better: 0.75 };
  const exact = Number.parseInt($('#exact-size').value, 10);
  const mb = preset === 'exact' ? exact : Math.max(1, Math.ceil((state.selected.size * percentages[preset]) / 1048576));
  $('#target-size').textContent = Number.isInteger(mb) && mb > 0 ? `${mb.toLocaleString()} MB` : 'Enter a size';
  $('#exact-size').disabled = preset !== 'exact';
}

async function submitJob(event) {
  event.preventDefault();
  if (!state.selected) return;
  const preset = selectedPreset();
  const exact = Number.parseInt($('#exact-size').value, 10);
  if (preset === 'exact' && (!Number.isInteger(exact) || exact < 1)) {
    showNotice('Enter a positive whole number of megabytes.');
    return;
  }
  const body = {
    path: state.selected.path,
    preset,
    container: $('#container').value,
    keep_all_audio: $('#audio').value === 'all',
    exact_mb: preset === 'exact' ? exact : 0,
  };
  try {
    await api('/api/jobs', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) });
    showNotice('Movie added to the queue.', 'success');
    await loadJobs();
    document.querySelector('.queue-section').scrollIntoView({ behavior: 'smooth' });
  } catch (error) {
    showNotice(error.message);
  }
}

function jobSettings(job) {
  const labels = { balanced: 'Balanced', smaller: 'Smaller', better: 'Better quality', exact: 'Exact size' };
  const preset = labels[job.settings.preset] || 'Custom';
  const container = String(job.settings.container || 'mkv').toUpperCase();
  return `${preset} · ${job.settings.quality || 'good'} · ${container} · ${job.settings.keep_all_audio ? 'all audio' : 'first audio'}`;
}

function normalizeJob(job) {
  if (!job || typeof job !== 'object' || !['string', 'number'].includes(typeof job.id)) return null;
  const id = String(job.id);
  if (!id) return null;
  const settings = job.settings && typeof job.settings === 'object' ? job.settings : {};
  const numberOrZero = (value) => Number.isFinite(Number(value)) ? Number(value) : 0;
  return {
    ...job,
    id,
    filename: typeof job.filename === 'string' ? job.filename : 'Unknown job',
    state: typeof job.state === 'string' ? job.state : 'unknown',
    stage: typeof job.stage === 'string' ? job.stage : 'Unknown stage',
    failure: typeof job.failure === 'string' ? job.failure : '',
    queued_at: job.queued_at,
    elapsed_seconds: numberOrZero(job.elapsed_seconds),
    result_size: numberOrZero(job.result_size),
    saved_percent: numberOrZero(job.saved_percent),
    logs: Array.isArray(job.logs) ? job.logs : [],
    settings: {
      preset: typeof settings.preset === 'string' ? settings.preset : 'balanced',
      quality: typeof settings.quality === 'string' ? settings.quality : 'good',
      container: typeof settings.container === 'string' ? settings.container : 'mkv',
      keep_all_audio: settings.keep_all_audio === true,
      target_mb: numberOrZero(settings.target_mb),
    },
  };
}

function rememberLogPanelState(container) {
  container.querySelectorAll('details[data-job-logs]').forEach((details) => {
    const id = details.dataset.jobLogs;
    state.knownLogJobs.add(id);
    if (details.open) state.expandedLogJobs.add(id);
    else state.expandedLogJobs.delete(id);
  });
}

function pruneLogPanelState() {
  const currentJobIDs = new Set(state.jobs.map((job) => job.id));
  [state.expandedLogJobs, state.knownLogJobs].forEach((storedIDs) => {
    storedIDs.forEach((id) => {
      if (!currentJobIDs.has(id)) storedIDs.delete(id);
    });
  });
}

function renderJobs() {
  const container = $('#jobs');
  rememberLogPanelState(container);
  pruneLogPanelState();
  if (!state.jobs.length) {
    container.innerHTML = '<div class="empty queue-empty">No jobs yet. Choose a movie above to get started.</div>';
    return;
  }
  container.innerHTML = state.jobs.map((job) => {
    const active = job.state === 'running';
    const cancellable = active || job.state === 'queued';
    const result = job.state === 'completed'
      ? `<div class="result"><strong>${formatBytes(job.result_size)}</strong><span>${job.saved_percent >= 0 ? `${job.saved_percent.toFixed(1)}% saved` : 'output is larger'}</span></div>`
      : '';
    const failure = job.failure ? `<p class="failure">${escapeHTML(job.failure)}</p>` : '';
    const logLines = Array.isArray(job.logs) ? job.logs : [];
    const hasStoredLogState = state.knownLogJobs.has(job.id);
    const logsOpen = hasStoredLogState ? state.expandedLogJobs.has(job.id) : active;
    if (logLines.length) {
      state.knownLogJobs.add(job.id);
      if (logsOpen) state.expandedLogJobs.add(job.id);
    }
    const logs = logLines.length
      ? `<details data-job-logs="${escapeHTML(job.id)}" ${logsOpen ? 'open' : ''}><summary>Latest log messages</summary><pre>${logLines.map(escapeHTML).join('\n')}</pre></details>`
      : '';
    return `<article class="job ${job.state}">
      <div class="job-main">
        <div class="job-state-icon">${active ? '<i class="spinner"></i>' : job.state === 'completed' ? '✓' : job.state === 'failed' ? '!' : job.state === 'cancelled' ? '×' : '…'}</div>
        <div class="job-copy"><div class="job-title"><strong>${escapeHTML(job.filename)}</strong><span class="state-pill">${escapeHTML(job.state)}</span></div>
          <p>${job.settings.target_mb.toLocaleString()} MB target · ${escapeHTML(jobSettings(job))} · queued ${escapeHTML(formatQueued(job.queued_at))}</p>
          <div class="stage"><span>${escapeHTML(job.stage)}</span><small>${formatElapsed(job.elapsed_seconds)}</small></div>
          ${failure}${logs}
        </div>
        ${result}
        ${cancellable ? `<button class="cancel" data-cancel="${job.id}">Cancel</button>` : ''}
      </div>
    </article>`;
  }).join('');
}

async function loadJobs() {
  try {
    const data = await api('/api/jobs');
    const jobs = Array.isArray(data?.jobs) ? data.jobs : [];
    state.jobs = jobs.map(normalizeJob).filter((job) => job !== null);
    renderJobs();
  } catch (error) {
    showNotice(error.message);
  }
}

async function cancelJob(id) {
  try {
    await api(`/api/jobs/${encodeURIComponent(id)}/cancel`, { method: 'POST' });
    await loadJobs();
  } catch (error) {
    showNotice(error.message);
  }
}

$('#file-list').addEventListener('click', (event) => {
  const row = event.target.closest('.file-row');
  if (!row) return;
  if (row.dataset.type === 'directory') loadFiles(row.dataset.path);
  else selectMovie(row.dataset.path);
});
$('#breadcrumbs').addEventListener('click', (event) => {
  const crumb = event.target.closest('button');
  if (crumb) loadFiles(crumb.dataset.path);
});
$('#job-form').addEventListener('submit', submitJob);
document.querySelectorAll('input[name="preset"]').forEach((input) => input.addEventListener('change', updateTarget));
$('#exact-size').addEventListener('input', updateTarget);
$('#jobs').addEventListener('click', (event) => {
  const button = event.target.closest('[data-cancel]');
  if (button) cancelJob(button.dataset.cancel);
});
$('#jobs').addEventListener('toggle', (event) => {
  const details = event.target.closest('details[data-job-logs]');
  if (!details) return;
  const id = details.dataset.jobLogs;
  state.knownLogJobs.add(id);
  if (details.open) state.expandedLogJobs.add(id);
  else state.expandedLogJobs.delete(id);
}, true);

loadHealth();
loadFiles();
loadJobs();
setInterval(loadJobs, 2000);
