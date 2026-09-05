const state = {
  currentPath: '',
  selected: null,
  roots: [],
  rootID: '',
  jobs: [],
  capabilities: { encoders: { software: true, qsv: false, vaapi: false, nvenc: false }, auto_selected: 'software' },
  expandedLogJobs: new Set(),
  knownLogJobs: new Set(),
  logScrollJobs: new Map(),
};
const $ = (selector) => document.querySelector(selector);
const { captureLogScroll, restoreLogScroll } = window.ShrinkrayLogScroll;

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
  if (hours) return `${hours}h ${mins}m`;
  if (mins) return `${mins}m ${secs}s`;
  return `${secs}s`;
}

function formatElapsed(seconds) {
  return formatDuration(seconds);
}

function formatPercent(value) {
  const percent = Math.max(0, Math.min(100, Number(value) || 0));
  return percent === 100 ? '100%' : `${percent.toFixed(1)}%`;
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
    const roots = Array.isArray(health?.roots) ? health.roots : [];
    state.roots = roots.filter((root) => root && typeof root.id === 'string' && root.id && typeof root.label === 'string');
    if (!state.roots.some((root) => root.id === state.rootID)) state.rootID = state.roots[0]?.id || '';
    $('#server-status').textContent = `Online · v${health.version}`;
    renderLibrarySelector();
    return true;
  } catch (error) {
    $('#server-status').textContent = 'Offline';
    state.roots = [];
    state.rootID = '';
    renderLibrarySelector();
    showNotice(error.message);
    return false;
  }
}

async function loadCapabilities() {
  try {
    const result = await api('/api/capabilities');
    const encoders = result?.encoders && typeof result.encoders === 'object' ? result.encoders : {};
    state.capabilities = {
      encoders: {
        software: encoders.software === true,
        qsv: encoders.qsv === true,
        vaapi: encoders.vaapi === true,
        nvenc: encoders.nvenc === true,
      },
      auto_selected: typeof result?.auto_selected === 'string' ? result.auto_selected : '',
    };
  } catch (error) {
    showNotice(`Encoder capabilities unavailable: ${error.message}`);
  }
  const options = window.ShrinkrayEncoderOptions.buildEncoderOptions(state.capabilities.encoders);
  $('#encoder').innerHTML = options.map((option) => `<option value="${option.value}">${option.label}</option>`).join('');
}

function currentRoot() {
  return state.roots.find((root) => root.id === state.rootID) || null;
}

function renderLibrarySelector() {
  const selector = $('#library-selector');
  selector.innerHTML = state.roots.map((root) => `
    <button type="button" data-root-id="${escapeHTML(root.id)}" class="${root.id === state.rootID ? 'active' : ''}" aria-pressed="${root.id === state.rootID}">${escapeHTML(root.label)}</button>
  `).join('');
  const selected = currentRoot();
  const summary = selected ? `${selected.label}${state.roots.length > 1 ? ` · ${state.roots.length} libraries` : ''}` : 'No media libraries';
  $('#movie-root').textContent = summary;
  $('#movie-root').title = summary;
}

function clearMovieSelection() {
  state.selected = null;
  $('#details-hint').textContent = 'Select a movie to inspect it.';
  $('#movie-details').innerHTML = '<div class="details-placeholder">No movie selected</div>';
  $('#job-form').reset();
  $('#settings-fieldset').disabled = true;
  $('#target-size').textContent = '—';
  $('#exact-size').disabled = true;
}

async function switchLibrary(rootID) {
  if (!state.roots.some((root) => root.id === rootID) || rootID === state.rootID) return;
  state.rootID = rootID;
  state.currentPath = '';
  clearMovieSelection();
  renderLibrarySelector();
  renderBreadcrumbs();
  $('#file-list').innerHTML = '<div class="empty">Loading movies…</div>';
  await loadFiles('');
}

async function loadFiles(folder = '') {
  const requestedRootID = state.rootID;
  if (!requestedRootID) {
    $('#file-list').innerHTML = '<div class="empty">No media library is configured.</div>';
    return;
  }
  try {
    const response = await api(`/api/files?root=${encodeURIComponent(requestedRootID)}&path=${encodeURIComponent(folder)}`);
    if (requestedRootID !== state.rootID) return;
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
  const rootLabel = currentRoot()?.label || 'Library';
  const crumbs = [`<button data-path="">${escapeHTML(rootLabel)}</button>`];
  parts.forEach((part) => {
    accumulated = accumulated ? `${accumulated}/${part}` : part;
    crumbs.push(`<span>›</span><button data-path="${escapeHTML(accumulated)}">${escapeHTML(part)}</button>`);
  });
  $('#breadcrumbs').innerHTML = crumbs.join('');
}

async function selectMovie(path) {
  const requestedRootID = state.rootID;
  if (!requestedRootID) return;
  $('#movie-details').innerHTML = '<div class="details-placeholder">Inspecting movie…</div>';
  try {
    const movie = await api(`/api/probe?root=${encodeURIComponent(requestedRootID)}&path=${encodeURIComponent(path)}`);
    if (requestedRootID !== state.rootID) return;
    state.selected = movie;
    $('#details-hint').textContent = movie.filename;
    $('#movie-details').innerHTML = `
      <div class="selected-movie"><span class="movie-icon">▶</span><div><strong>${escapeHTML(movie.filename)}</strong><small>${escapeHTML(movie.root_label)} · ${escapeHTML(movie.path)}</small></div></div>
      <dl class="detail-grid">
        <div><dt>Library</dt><dd>${escapeHTML(movie.root_label)}</dd></div>
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
    root_id: state.selected.root_id,
    path: state.selected.path,
    preset,
    container: $('#container').value,
    keep_all_audio: $('#audio').value === 'all',
    replace_original: $('#replace-original').checked,
    requested_encoder: $('#encoder').value,
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
  const requested = encoderDisplayName(job.settings.requested_encoder || 'software');
  const outputMode = job.settings.replace_original ? 'replace original' : 'separate output';
  return `${preset} · ${job.settings.quality || 'good'} · ${container} · ${job.settings.keep_all_audio ? 'all audio' : 'first audio'} · requested ${requested} · ${outputMode}`;
}

function encoderDisplayName(encoder) {
  return ({ auto: 'Auto', software: 'Software x265', qsv: 'Intel QSV', vaapi: 'VAAPI', nvenc: 'NVIDIA NVENC' })[encoder] || 'Legacy software';
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
    root_id: typeof job.root_id === 'string' ? job.root_id : '',
    root_label: typeof job.root_label === 'string' ? job.root_label : 'Unknown library',
    queued_at: job.queued_at,
    elapsed_seconds: numberOrZero(job.elapsed_seconds),
    progress_percent: numberOrZero(job.progress_percent),
    stage_progress_percent: numberOrZero(job.stage_progress_percent),
    duration_seconds: numberOrZero(job.duration_seconds),
    processed_seconds: numberOrZero(job.processed_seconds),
    eta_seconds: job.eta_seconds === null || job.eta_seconds === undefined ? null : numberOrZero(job.eta_seconds),
    encode_speed: numberOrZero(job.encode_speed),
    eta_is_estimate: job.eta_is_estimate === true,
    disk_available_bytes: numberOrZero(job.disk_available_bytes),
    disk_required_bytes: numberOrZero(job.disk_required_bytes),
    disk_safety_reserve_bytes: numberOrZero(job.disk_safety_reserve_bytes),
    disk_space_warning: job.disk_space_warning === true,
    original_size: numberOrZero(job.original_size),
    result_size: numberOrZero(job.result_size),
    saved_percent: numberOrZero(job.saved_percent),
    actual_encoder: typeof job.actual_encoder === 'string' ? job.actual_encoder : '',
    source_replaced: job.source_replaced === true,
    original_kept: job.original_kept === true,
    final_path: typeof job.final_path === 'string' ? job.final_path : '',
    logs: Array.isArray(job.logs) ? job.logs : [],
    settings: {
      preset: typeof settings.preset === 'string' ? settings.preset : 'balanced',
      quality: typeof settings.quality === 'string' ? settings.quality : 'good',
      container: typeof settings.container === 'string' ? settings.container : 'mkv',
      keep_all_audio: settings.keep_all_audio === true,
      target_mb: numberOrZero(settings.target_mb),
      requested_encoder: typeof settings.requested_encoder === 'string' ? settings.requested_encoder : 'software',
      replace_original: settings.replace_original === true,
    },
  };
}

function rememberLogPanelState(container) {
  container.querySelectorAll('details[data-job-logs]').forEach((details) => {
    const id = details.dataset.jobLogs;
    state.knownLogJobs.add(id);
    if (details.open) state.expandedLogJobs.add(id);
    else state.expandedLogJobs.delete(id);
    const log = details.querySelector('pre');
    if (details.open && log) state.logScrollJobs.set(id, captureLogScroll(log));
  });
}

function pruneLogPanelState() {
  const currentJobIDs = new Set(state.jobs.map((job) => job.id));
  [state.expandedLogJobs, state.knownLogJobs].forEach((storedIDs) => {
    storedIDs.forEach((id) => {
      if (!currentJobIDs.has(id)) storedIDs.delete(id);
    });
  });
  state.logScrollJobs.forEach((_, id) => {
    if (!currentJobIDs.has(id)) state.logScrollJobs.delete(id);
  });
}

function renderJob(job) {
  const active = job.state === 'running';
  const cancellable = active || job.state === 'queued';
  const showProgress = active || job.state === 'completed' || job.progress_percent > 0;
  const overallPercent = job.state === 'completed' ? 100 : Math.max(0, Math.min(99, job.progress_percent));
  const showPassProgress = job.stage.startsWith('HEVC pass');
  const showDisk = job.disk_required_bytes > 0 && (
    active || job.state === 'queued' || job.stage === 'Insufficient disk space' || job.stage === 'Critical low disk space'
  );
  const disk = showDisk ? `<div class="disk-space ${job.disk_space_warning ? 'warning' : ''}">
    <span>${job.disk_space_warning ? '⚠ ' : ''}Disk free: <strong>${formatBytes(job.disk_available_bytes)}</strong></span>
    <span>Required: <strong>${formatBytes(job.disk_required_bytes)}</strong></span>
  </div>` : '';
  const progress = showProgress ? `<div class="job-progress">
      <div class="progress-labels"><strong>${formatPercent(overallPercent)} overall</strong>${showPassProgress ? `<span>${formatPercent(job.stage_progress_percent)} current pass</span>` : ''}</div>
      <div class="progress-track" role="progressbar" aria-label="Overall encoding progress" aria-valuemin="0" aria-valuemax="100" aria-valuenow="${overallPercent.toFixed(1)}"><span style="width: ${overallPercent.toFixed(1)}%"></span></div>
      <div class="progress-stats">
        <span><small>Elapsed</small><strong>${formatElapsed(job.elapsed_seconds)}</strong></span>
        ${job.duration_seconds > 0 ? `<span><small>Media</small><strong>${formatDuration(job.processed_seconds)} / ${formatDuration(job.duration_seconds)}</strong></span>` : ''}
        ${job.encode_speed > 0 ? `<span><small>Speed</small><strong>${job.encode_speed.toFixed(2)}x</strong></span>` : ''}
        ${active && job.eta_seconds !== null && job.eta_seconds > 0 ? `<span><small>${job.eta_is_estimate ? 'Estimated ETA' : 'ETA'}</small><strong>${formatDuration(job.eta_seconds)}</strong></span>` : ''}
      </div>
    </div>` : '';
  const resultTitle = job.source_replaced ? 'Original replaced safely' : '100% · Completed';
  const resultSizes = job.source_replaced
    ? `${formatBytes(job.original_size)} → ${formatBytes(job.result_size)} · Saved ${job.saved_percent.toFixed(1)}%`
    : `${formatBytes(job.result_size)} · ${job.saved_percent >= 0 ? `${job.saved_percent.toFixed(1)}% saved` : 'output is larger'}`;
  const result = job.state === 'completed'
    ? `<div class="result"><strong>${resultTitle}</strong><span>${resultSizes}</span>${job.final_path ? `<small>${escapeHTML(job.final_path)}</small>` : ''}</div>`
    : '';
  const originalKept = job.settings.replace_original && job.original_kept ? '<p class="failure"><strong>Original kept</strong></p>' : '';
  const failure = `${originalKept}${job.failure ? `<p class="failure">${escapeHTML(job.failure)}</p>` : ''}`;
  const actualEncoder = job.actual_encoder ? `<p class="encoder-used">Encoder: <strong>${escapeHTML(encoderDisplayName(job.actual_encoder))}</strong></p>` : '';
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
  return `<article class="job ${job.state}" data-job-id="${escapeHTML(job.id)}">
      <div class="job-main">
        <div class="job-state-icon">${active ? '<i class="spinner"></i>' : job.state === 'completed' ? '✓' : job.state === 'failed' ? '!' : job.state === 'cancelled' ? '×' : '…'}</div>
        <div class="job-copy"><div class="job-title"><strong>${escapeHTML(job.filename)}</strong><span class="state-pill">${escapeHTML(job.state)}</span></div>
          <p>${escapeHTML(job.root_label)} · ${job.settings.target_mb.toLocaleString()} MB target · ${escapeHTML(jobSettings(job))} · queued ${escapeHTML(formatQueued(job.queued_at))}</p>
          <div class="stage"><span>${escapeHTML(job.stage)}</span><small>${formatElapsed(job.elapsed_seconds)}</small></div>
          ${progress}${disk}${actualEncoder}${failure}${logs}
        </div>
        ${result}
        ${cancellable ? `<button class="cancel" data-cancel="${job.id}">Cancel</button>` : ''}
      </div>
    </article>`;
}

function createJobCard(job) {
  const template = document.createElement('template');
  template.innerHTML = renderJob(job).trim();
  return template.content.firstElementChild;
}

function reconcileLogPanel(previousCard, nextCard, restorations) {
  const previousDetails = previousCard?.querySelector('details[data-job-logs]');
  const nextDetails = nextCard.querySelector('details[data-job-logs]');
  if (!nextDetails) return;

  const nextLog = nextDetails.querySelector('pre');
  const id = nextDetails.dataset.jobLogs;
  if (!previousDetails) {
    restorations.push({ id, details: nextDetails, log: nextLog, snapshot: state.logScrollJobs.get(id) || null });
    return;
  }

  const previousLog = previousDetails.querySelector('pre');
  if (!previousLog || !nextLog) return;
  const nextText = nextLog.textContent;
  const contentChanged = previousLog.textContent !== nextText;
  const snapshot = contentChanged
    ? (previousDetails.open ? captureLogScroll(previousLog) : state.logScrollJobs.get(id) || captureLogScroll(previousLog))
    : null;
  if (contentChanged) previousLog.textContent = nextText;
  nextDetails.replaceWith(previousDetails);
  if (contentChanged) restorations.push({ id, details: previousDetails, log: previousLog, snapshot });
}

function renderJobs() {
  const container = $('#jobs');
  rememberLogPanelState(container);
  pruneLogPanelState();
  const previousCards = new Map(Array.from(container.querySelectorAll('.job[data-job-id]'), (card) => [card.dataset.jobId, card]));
  const restorations = [];
  const createCards = (jobs) => jobs.map((job) => {
    const card = createJobCard(job);
    reconcileLogPanel(previousCards.get(job.id), card, restorations);
    return card;
  });
  const activeJobs = state.jobs.filter((job) => job.state === 'queued' || job.state === 'running');
  const historyJobs = state.jobs.filter((job) => job.state === 'completed' || job.state === 'failed' || job.state === 'cancelled');
  const activeContainer = $('#active-jobs');
  const historyContainer = $('#history-jobs');
  const historySection = $('#history-section');
  const activeCards = createCards(activeJobs);
  if (activeCards.length) activeContainer.replaceChildren(...activeCards);
  else activeContainer.innerHTML = '<div class="empty queue-empty">No active or queued jobs.</div>';
  historyContainer.replaceChildren(...createCards(historyJobs));
  historySection.hidden = historyJobs.length === 0;
  restorations.forEach(({ id, details, log, snapshot }) => {
    if (details.open) {
      restoreLogScroll(log, snapshot, true);
      state.logScrollJobs.set(id, captureLogScroll(log));
    } else if (!state.logScrollJobs.has(id)) {
      state.logScrollJobs.set(id, snapshot || { followingBottom: true, distanceFromBottom: 0 });
    }
  });
}

async function clearHistory() {
  if (!window.confirm('Clear completed, failed and cancelled job history?')) return;
  try {
    const result = await api('/api/jobs/history', { method: 'DELETE' });
    await loadJobs();
    showNotice(`${Number(result.removed) || 0} history record(s) cleared. Media files were not changed.`, 'success');
  } catch (error) {
    showNotice(error.message);
  }
}

async function loadJobs() {
  try {
    const previouslyReplaced = new Set(state.jobs.filter((job) => job.source_replaced).map((job) => job.id));
    const data = await api('/api/jobs');
    const jobs = Array.isArray(data?.jobs) ? data.jobs : [];
    state.jobs = jobs.map(normalizeJob).filter((job) => job !== null);
    renderJobs();
    if (state.jobs.some((job) => job.source_replaced && !previouslyReplaced.has(job.id))) {
      await loadFiles(state.currentPath);
      clearMovieSelection();
    }
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
$('#library-selector').addEventListener('click', (event) => {
  const button = event.target.closest('[data-root-id]');
  if (button) switchLibrary(button.dataset.rootId);
});
$('#job-form').addEventListener('submit', submitJob);
document.querySelectorAll('input[name="preset"]').forEach((input) => input.addEventListener('change', updateTarget));
$('#exact-size').addEventListener('input', updateTarget);
$('#jobs').addEventListener('click', (event) => {
  const clearButton = event.target.closest('#clear-history');
  if (clearButton) {
    clearHistory();
    return;
  }
  const button = event.target.closest('[data-cancel]');
  if (button) cancelJob(button.dataset.cancel);
});
$('#jobs').addEventListener('toggle', (event) => {
  const details = event.target.closest('details[data-job-logs]');
  if (!details) return;
  const id = details.dataset.jobLogs;
  state.knownLogJobs.add(id);
  if (details.open) {
    state.expandedLogJobs.add(id);
    requestAnimationFrame(() => {
      const log = details.querySelector('pre');
      if (!log) return;
      restoreLogScroll(log, state.logScrollJobs.get(id) || null, true);
      state.logScrollJobs.set(id, captureLogScroll(log));
    });
  } else {
    state.expandedLogJobs.delete(id);
  }
}, true);
$('#jobs').addEventListener('scroll', (event) => {
  const log = event.target.closest('details[data-job-logs] > pre');
  if (!log) return;
  const details = log.closest('details[data-job-logs]');
  state.logScrollJobs.set(details.dataset.jobLogs, captureLogScroll(log));
}, true);

async function initialize() {
  await loadHealth();
  await loadCapabilities();
  await loadFiles();
  await loadJobs();
  setInterval(loadJobs, 1000);
}

initialize();
