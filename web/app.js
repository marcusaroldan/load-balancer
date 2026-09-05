const state = {
  strategy: '',
  clients: 0,
  requestRate: 0,
  pools: [],
  backends: []
};

const strategyValue = document.getElementById('strategyValue');
const clientsValue = document.getElementById('clientsValue');
const requestRateValue = document.getElementById('requestRateValue');
const backendCountValue = document.getElementById('backendCountValue');
const backendGrid = document.getElementById('backendGrid');
const trafficStream = document.getElementById('trafficStream');
const strategyPicker = document.getElementById('strategyPicker');
const requestRateInput = document.getElementById('requestRateInput');
const scenarioSelect = document.getElementById('scenarioSelect');
const newPoolInput = document.getElementById('newPoolInput');
const backendModal = document.getElementById('backendModal');
const backendModalTitle = document.getElementById('backendModalTitle');
const backendNameInput = document.getElementById('backendNameInput');
const backendIdInput = document.getElementById('backendIdInput');
const backendPoolInput = document.getElementById('backendPoolInput');
const backendWeightInput = document.getElementById('backendWeightInput');
const backendLatencyInput = document.getElementById('backendLatencyInput');
const backendHealthyInput = document.getElementById('backendHealthyInput');
const backendFailureInput = document.getElementById('backendFailureInput');
const backendForm = document.getElementById('backendForm');
const deleteBackendButton = document.getElementById('deleteBackend');
const closeModalButton = document.getElementById('closeModal');

let activeBackendId = null;
const LIVE_BACKEND_REFRESH_MS = 200;
const SUMMARY_REFRESH_MS = 1000;
let lastSummaryRenderAt = 0;

function formatDurationText(value) {
  if (value === null || value === undefined || value === '') {
    return '0ms';
  }

  if (typeof value === 'number') {
    if (value >= 1_000_000) {
      return `${Math.round(value / 1_000_000)}ms`;
    }
    return `${value}ns`;
  }

  const text = String(value).trim();
  if (!text) return '0ms';
  if (/^\d+$/.test(text)) {
    const nanos = Number(text);
    if (nanos >= 1_000_000) {
      return `${Math.round(nanos / 1_000_000)}ms`;
    }
    return `${nanos}ns`;
  }
  return text;
}

function getBackendStatus(backend) {
  if (!backend.healthy) return 'unhealthy';
  if (backend.load_percent >= 80 || backend.latency > 250) return 'slow';
  return 'healthy';
}

async function fetchState() {
  const res = await fetch('/api/state');
  if (!res.ok) return;
  const next = await res.json();
  state.strategy = next.strategy || '';
  state.clients = Number(next.clients || 0);
  state.requestRate = Number(next.request_rate || 0);
  state.pools = next.pools || [];
  state.backends = next.backends || [];

  const now = Date.now();
  if (now - lastSummaryRenderAt >= SUMMARY_REFRESH_MS) {
    renderSummary();
    lastSummaryRenderAt = now;
  }
  renderBackendView();
}

function renderPoolOptions() {
  const poolNames = [...new Set(['default', ...state.pools])].filter(Boolean);
  const selected = backendPoolInput.value || 'default';
  backendPoolInput.innerHTML = poolNames.map((pool) => `
    <option value="${pool}">${pool}</option>
  `).join('');
  if (poolNames.includes(selected)) {
    backendPoolInput.value = selected;
  } else {
    backendPoolInput.value = 'default';
  }
}

function renderSummary() {
  strategyValue.textContent = state.strategy || '-';
  clientsValue.textContent = String(state.clients);
  requestRateValue.textContent = `${state.requestRate} req/s`;
  backendCountValue.textContent = String(state.backends.length);

  if (document.activeElement !== strategyPicker) {
    strategyPicker.value = state.strategy || 'round_robin';
  }
  if (document.activeElement !== requestRateInput) {
    requestRateInput.value = String(state.requestRate || 0);
  }
}

function renderBackendView() {
  const clientNodes = Array.from({ length: Math.max(1, state.clients) }, (_, index) => `
    <div class="client-node">
      <strong>Client ${index + 1}</strong>
      <small>${state.requestRate} req/s</small>
    </div>
  `).join('');

  const poolGroups = Object.entries(
    state.backends.reduce((groups, backend) => {
      const pool = backend.pool || 'default';
      if (!groups[pool]) groups[pool] = [];
      groups[pool].push(backend);
      return groups;
    }, {})
  ).sort(([left], [right]) => left.localeCompare(right)).map(([pool, backends]) => {
    const backendNodes = backends.map((backend) => {
      const status = getBackendStatus(backend);
      const load = Math.max(8, Number(backend.load_percent || 0));
      return `
        <div class="backend-node ${status === 'healthy' ? '' : status}" data-backend-id="${backend.id}" title="Edit ${backend.id}">
          <div>
            <strong>${backend.id}</strong><br />
            <small>${backend.active_connections ?? 0} active / ${backend.latency} ms</small>
          </div>
          <div class="load-display">
            <small>${load}% busy</small>
            <div class="load-bar"><span style="width: ${load}%"></span></div>
          </div>
        </div>
      `;
    }).join('');

    return `
      <div class="pool-group">
        <div class="pool-label">${pool}</div>
        <div class="pool-body">${backendNodes || '<div class="backend-node"><strong>No backends</strong></div>'}</div>
      </div>
    `;
  }).join('');

  trafficStream.innerHTML = `
    <div class="traffic-scene">
      <div class="client-column">${clientNodes}</div>
      <div class="lb-column">
        <div class="lb-node">LB</div>
      </div>
      <div class="backend-column">${poolGroups || '<div class="backend-node"><strong>No backends</strong></div>'}</div>
    </div>
  `;

  trafficStream.querySelectorAll('[data-backend-id]').forEach((elem) => {
    elem.addEventListener('click', () => openBackendModal(elem.dataset.backendId));
  });

  backendGrid.innerHTML = '';
  state.backends.forEach((backend) => {
    const card = document.createElement('article');
    card.className = 'backend-card';
    const status = getBackendStatus(backend);
    card.classList.add(`status-${status}`);
    card.dataset.backendId = backend.id;
    const label = status === 'healthy' ? 'healthy' : status === 'slow' ? 'slow' : 'unhealthy';
    const load = Math.max(0, Number(backend.load_percent || 0));
    const displayName = backend.name || backend.id;

    card.innerHTML = `
      <div class="pool-header-row">
        <span class="pool-mini-label">${backend.pool || 'default'}</span>
        <span class="badge ${label}">${label}</span>
      </div>
      <div class="backend-header">
        <strong>${displayName}</strong>
      </div>
      <p>Pool: ${backend.pool || 'default'}</p>
      <p>URL: ${backend.url}</p>
      <p>Latency: ${backend.latency} ms</p>
      <p>Weight: ${backend.weight}</p>
      <p>Active connections: ${backend.active_connections ?? 0}</p>
      <p>Failure rate: ${backend.failure_rate}</p>
      <div class="backend-meter"><span style="width: ${load}%"></span></div>
    `;

    card.addEventListener('click', () => openBackendModal(backend.id));
    backendGrid.appendChild(card);
  });

  renderPoolOptions();
}

function render() {
  renderSummary();
  renderBackendView();
  lastSummaryRenderAt = Date.now();
}

function openBackendModal(id) {
  const backend = state.backends.find((entry) => entry.id === id);
  if (!backend) return;
  activeBackendId = id;
  backendModalTitle.textContent = `Backend: ${backend.name || backend.id}`;
  backendNameInput.value = backend.name || backend.id;
  backendIdInput.value = backend.id;
  backendWeightInput.value = String(backend.weight || 1);
  backendLatencyInput.value = formatDurationText(backend.latency);
  backendHealthyInput.checked = !!backend.healthy;
  backendFailureInput.value = String(backend.failure_rate || 0);
  backendPoolInput.value = backend.pool || 'default';
  backendModal.classList.remove('hidden');
  backendModal.setAttribute('aria-hidden', 'false');
}

function closeBackendModal() {
  backendModal.classList.add('hidden');
  backendModal.setAttribute('aria-hidden', 'true');
  activeBackendId = null;
}

async function addPool() {
  const name = newPoolInput.value.trim();
  if (!name) return;
  await fetch('/api/pool/add', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ value: name })
  });
  newPoolInput.value = '';
  await fetchState();
}

async function addBackend() {
  const pool = newPoolInput.value.trim() || 'default';
  const id = `backend-${Date.now().toString().slice(-6)}`;
  await fetch('/api/backend/add', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      id,
      address: '127.0.0.1',
      port: 0,
      weight: 1,
      latency: '50ms',
      healthy: true,
      pool
    })
  });
  await fetchState();
}

async function resetScenario() {
  const name = scenarioSelect.value;
  await fetch('/api/reset', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ value: name })
  });
  await fetchState();
}

async function applyStrategy() {
  const strategy = strategyPicker.value;
  await fetch('/api/strategy', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ value: strategy })
  });
  await fetchState();
}

async function applyRequestRate() {
  const value = requestRateInput.value;
  await fetch('/api/request-rate', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ value })
  });
  await fetchState();
}

async function addClient() {
  await fetch('/api/client/add', { method: 'POST' });
  await fetchState();
}

async function removeClient() {
  await fetch('/api/client/remove', { method: 'POST' });
  await fetchState();
}

async function updateBackendField(endpoint, payload) {
  const res = await fetch(endpoint, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(payload)
  });
  if (!res.ok) {
    throw new Error(`Failed to update backend field: ${endpoint}`);
  }
}

async function saveBackendChanges(event) {
  event.preventDefault();
  if (!activeBackendId) return;

  const payload = { id: activeBackendId };
  const name = backendNameInput.value.trim();
  const weight = Number(backendWeightInput.value || 1);
  const latency = backendLatencyInput.value.trim();
  const healthy = backendHealthyInput.checked;
  const failureRate = Number(backendFailureInput.value || 0);
  const pool = backendPoolInput.value || 'default';

  try {
    if (name) {
      await updateBackendField('/api/backend/name', { ...payload, name });
    }

    if (Number.isFinite(weight) && weight > 0) {
      await updateBackendField('/api/backend/weight', { ...payload, weight });
    }

    if (latency) {
      await updateBackendField('/api/backend/latency', { ...payload, latency });
    }

    await updateBackendField('/api/backend/health', { ...payload, healthy });
    await updateBackendField('/api/backend/failure-rate', { ...payload, rate: failureRate });

    if (pool) {
      await updateBackendField('/api/backend/pool', { ...payload, pool });
    }
  } catch (error) {
    console.error(error);
    return;
  }

  closeBackendModal();
  await fetchState();
}

async function deleteBackend() {
  if (!activeBackendId) return;
  await fetch('/api/backend/remove', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ id: activeBackendId })
  });
  closeBackendModal();
  await fetchState();
}

document.getElementById('resetScenario').addEventListener('click', resetScenario);
document.getElementById('applyStrategy').addEventListener('click', applyStrategy);
document.getElementById('applyRequestRate').addEventListener('click', applyRequestRate);
document.getElementById('addClient').addEventListener('click', addClient);
document.getElementById('removeClient').addEventListener('click', removeClient);
document.getElementById('addPool').addEventListener('click', addPool);
document.getElementById('addBackend').addEventListener('click', addBackend);
closeModalButton.addEventListener('click', closeBackendModal);
deleteBackendButton.addEventListener('click', deleteBackend);
backendForm.addEventListener('submit', saveBackendChanges);

render();
setInterval(fetchState, LIVE_BACKEND_REFRESH_MS);
