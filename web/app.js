const state = {
  strategy: '',
  clients: 0,
  requestRate: 0,
  pools: [],
  backends: [],
  loadBalancer: {
    request_count: 0,
    successful_requests: 0,
    failed_requests: 0,
    average_latency_ms: 0,
    pool_count: 0,
    backend_count: 0,
    healthy_backends: 0
  }
};

const strategyValue = document.getElementById('strategyValue');
const clientsValue = document.getElementById('clientsValue');
const requestRateValue = document.getElementById('requestRateValue');
const backendCountValue = document.getElementById('backendCountValue');
const lbRequestCountValue = document.getElementById('lbRequestCountValue');
const lbSuccessRateValue = document.getElementById('lbSuccessRateValue');
const lbFailureCountValue = document.getElementById('lbFailureCountValue');
const lbAverageLatencyValue = document.getElementById('lbAverageLatencyValue');
const lbHealthValue = document.getElementById('lbHealthValue');
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

function parseDurationNanoseconds(value) {
  if (value === null || value === undefined || value === '') {
    return 0;
  }

  if (typeof value === 'number') {
    if (!Number.isFinite(value)) return 0;
    return value;
  }

  const text = String(value).trim();
  if (!text) return 0;

  if (/^\d+$/.test(text)) {
    return Number(text);
  }

  const match = text.match(/^(-?\d+(?:\.\d+)?)(ns|us|µs|ms|s)?$/i);
  if (!match) {
    return 0;
  }

  const amount = Number(match[1]);
  const unit = (match[2] || 'ms').toLowerCase();

  switch (unit) {
    case 'ns':
      return amount;
    case 'us':
    case 'µs':
      return amount * 1_000;
    case 'ms':
      return amount * 1_000_000;
    case 's':
      return amount * 1_000_000_000;
    default:
      return amount;
  }
}

function formatDurationText(value) {
  const nanos = parseDurationNanoseconds(value);

  if (nanos === 0) {
    return '0ms';
  }

  if (nanos >= 1_000_000_000) {
    return `${(nanos / 1_000_000_000).toFixed(2)}s`;
  }
  if (nanos >= 1_000_000) {
    return `${Math.round(nanos / 1_000_000)}ms`;
  }
  if (nanos >= 1_000) {
    return `${(nanos / 1_000).toFixed(2)}µs`;
  }
  return `${Math.round(nanos)}ns`;
}

function getBackendStatus(backend) {
  if (!backend.healthy) return 'unhealthy';
  const latencyMs = parseDurationNanoseconds(backend.latency) / 1_000_000;
  if (backend.load_percent >= 80 || latencyMs > 250) return 'slow';
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
  state.loadBalancer = next.load_balancer || state.loadBalancer;

  const now = Date.now();
  if (now - lastSummaryRenderAt >= SUMMARY_REFRESH_MS) {
    renderSummary();
    lastSummaryRenderAt = now;
  }
  updateBackendView();
}

// ---------------------------------------------------------------------------
// DOM reconciliation
//
// The dashboard polls /api/state every 200ms. Early builds threw the whole
// traffic scene and backend grid away on every tick (innerHTML rebuilds),
// which repaints constantly, drops hover/focus states, and re-attaches
// listeners. Everything below instead reconciles keyed nodes in place:
// create once, then patch text/widths/classes that actually changed.
// ---------------------------------------------------------------------------

function setText(el, text) {
  if (el && el.textContent !== text) el.textContent = text;
}

function setWidth(span, percent) {
  const next = `${percent}%`;
  if (span && span.style.width !== next) span.style.width = next;
}

// keyedSync reconciles a parent's children against `items` using each item's
// `key`. `create` builds the DOM for a new item; `update` patches an existing
// node in place. Removals and reordering are handled by comparing the desired
// key order against the current child list.
function keyedSync(parent, items, keyOf, create, update) {
  const existing = new Map();
  Array.from(parent.children).forEach((child) => {
    if (child.dataset && child.dataset.syncKey !== undefined) {
      existing.set(child.dataset.syncKey, child);
    }
  });

  let cursor = null;
  items.forEach((item) => {
    const key = keyOf(item);
    let node = existing.get(key);
    if (!node) {
      node = create(item);
      node.dataset.syncKey = key;
      existing.set(key, node);
    }
    update(node, item);
    // Maintain document order without touching settled nodes.
    if (cursor) {
      if (cursor.nextElementSibling !== node) {
        parent.insertBefore(node, cursor.nextElementSibling);
      }
    } else if (parent.firstElementChild !== node) {
      parent.insertBefore(node, parent.firstElementChild);
    }
    cursor = node;
  });

  existing.forEach((node, key) => {
    if (!items.some((item) => keyOf(item) === key)) {
      node.remove();
      existing.delete(key);
    }
  });
}

// --- traffic scene (clients | load balancer | pooled backends) --------------

let trafficSceneRefs = null;

function buildTrafficScene() {
  trafficStream.innerHTML = `
    <div class="traffic-scene">
      <div class="client-column"></div>
      <div class="lb-column">
        <div class="lb-node node-card">
          <span class="node-icon" aria-hidden="true">
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round">
              <path d="M3 12h6l2-6 4 12 2-6h4"></path>
              <circle cx="12" cy="12" r="10"></circle>
            </svg>
          </span>
          <span class="node-label">Load Balancer</span>
        </div>
      </div>
      <div class="backend-column"></div>
    </div>
  `;
  const scene = trafficStream.querySelector('.traffic-scene');
  trafficSceneRefs = {
    clients: scene.querySelector('.client-column'),
    backends: scene.querySelector('.backend-column')
  };
}

function clientNode() {
  const wrap = document.createElement('div');
  wrap.className = 'client-node node-card';
  wrap.innerHTML = `
    <div class="node-header">
      <span class="node-icon" aria-hidden="true">
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round">
          <rect x="3" y="4" width="18" height="12" rx="2"></rect>
          <path d="M8 20h8"></path>
          <path d="M12 16v4"></path>
        </svg>
      </span>
      <div>
        <strong></strong>
        <small></small>
      </div>
    </div>
  `;
  return wrap;
}

function syncClientNodes() {
  const count = Math.max(1, state.clients);
  const items = Array.from({ length: count }, (_, index) => ({ key: String(index), index }));
  keyedSync(
    trafficSceneRefs.clients,
    items,
    (item) => item.key,
    clientNode,
    (node, item) => {
      setText(node.querySelector('strong'), `Client ${item.index + 1}`);
      setText(node.querySelector('small'), `${state.requestRate} req/s`);
    }
  );
}

function backendNode(backend) {
  const wrap = document.createElement('div');
  wrap.className = 'backend-node node-card';
  wrap.innerHTML = `
    <div class="node-header">
      <span class="node-icon" aria-hidden="true">
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round">
          <rect x="4" y="4" width="16" height="4" rx="1.4"></rect>
          <circle cx="8" cy="6" r="0.9" fill="currentColor" stroke="none"></circle>
          <circle cx="12" cy="6" r="0.9" fill="currentColor" stroke="none"></circle>
          <circle cx="16" cy="6" r="0.9" fill="currentColor" stroke="none"></circle>
          <rect x="4" y="10" width="16" height="4" rx="1.4"></rect>
          <circle cx="8" cy="12" r="0.9" fill="currentColor" stroke="none"></circle>
          <circle cx="12" cy="12" r="0.9" fill="currentColor" stroke="none"></circle>
          <circle cx="16" cy="12" r="0.9" fill="currentColor" stroke="none"></circle>
          <rect x="4" y="16" width="16" height="4" rx="1.4"></rect>
          <circle cx="8" cy="18" r="0.9" fill="currentColor" stroke="none"></circle>
          <circle cx="12" cy="18" r="0.9" fill="currentColor" stroke="none"></circle>
          <circle cx="16" cy="18" r="0.9" fill="currentColor" stroke="none"></circle>
        </svg>
      </span>
      <div>
        <strong></strong><br />
        <small></small>
      </div>
    </div>
    <div class="load-display">
      <small></small>
      <div class="load-bar"><span></span></div>
    </div>
  `;
  return wrap;
}

function updateBackendNode(node, backend) {
  const status = getBackendStatus(backend);
  const statusClass = status === 'healthy' ? '' : status;
  node.className = `backend-node node-card ${statusClass}`.trim();
  node.title = `Edit ${backend.id}`;
  setText(node.querySelector('strong'), backend.id);
  setText(node.querySelector('small'), `${backend.active_connections ?? 0} active / ${formatDurationText(backend.latency)}`);
  const load = Math.max(8, Number(backend.load_percent || 0));
  setText(node.querySelector('.load-display small'), `${load}% busy`);
  setWidth(node.querySelector('.load-bar span'), load);
  if (!node.dataset.bound) {
    node.dataset.bound = '1';
    node.addEventListener('click', () => openBackendModal(backend.id));
  }
}

function syncPoolGroups() {
  const groups = state.backends.reduce((acc, backend) => {
    const pool = backend.pool || 'default';
    (acc[pool] = acc[pool] || []).push(backend);
    return acc;
  }, {});

  const poolIDs = Object.keys(groups).sort((left, right) => left.localeCompare(right));

  // A pool key exists even when it currently has no backends so the group
  // does not blink out of the scene during reconfiguration.
  const allPoolIDs = [...new Set([...poolIDs, ...state.pools])].sort((left, right) => left.localeCompare(right));

  keyedSync(
    trafficSceneRefs.backends,
    allPoolIDs,
    (id) => id,
    (id) => {
      const group = document.createElement('div');
      group.className = 'pool-group';
      group.innerHTML = `
        <div class="pool-label"></div>
        <div class="pool-body"></div>
      `;
      return group;
    },
    (node, poolID) => {
      setText(node.querySelector('.pool-label'), poolID);
      const body = node.querySelector('.pool-body');
      const backends = groups[poolID] || [];
      if (backends.length === 0) {
        if (!body.querySelector('.empty-pool-note')) {
          body.innerHTML = '<div class="backend-node node-card empty-pool-note"><strong>No backends</strong></div>';
        }
        return;
      }
      keyedSync(body, backends, (b) => b.id, backendNode, updateBackendNode);
    }
  );
}

// --- backend detail grid -----------------------------------------------------

function backendCard() {
  const card = document.createElement('article');
  card.className = 'backend-card';
  card.innerHTML = `
    <div class="pool-header-row">
      <span class="pool-mini-label"></span>
      <span class="badge"></span>
    </div>
    <div class="backend-header">
      <strong></strong>
    </div>
    <p class="line-pool"></p>
    <p class="line-url"></p>
    <p class="line-latency"></p>
    <p class="line-weight"></p>
    <p class="line-connections"></p>
    <p class="line-failure"></p>
    <div class="backend-meter"><span></span></div>
  `;
  return card;
}

function updateBackendCard(card, backend) {
  const status = getBackendStatus(backend);
  const label = status === 'healthy' ? 'healthy' : status === 'slow' ? 'slow' : 'unhealthy';
  card.className = `backend-card status-${status}`;
  setText(card.querySelector('.pool-mini-label'), backend.pool || 'default');
  setText(card.querySelector('.badge'), label);
  card.querySelector('.badge').className = `badge ${label}`;
  setText(card.querySelector('.backend-header strong'), backend.name || backend.id);
  setText(card.querySelector('.line-pool'), `Pool: ${backend.pool || 'default'}`);
  setText(card.querySelector('.line-url'), `URL: ${backend.url || ''}`);
  setText(card.querySelector('.line-latency'), `Latency: ${formatDurationText(backend.latency)}`);
  setText(card.querySelector('.line-weight'), `Weight: ${backend.weight}`);
  setText(card.querySelector('.line-connections'), `Active connections: ${backend.active_connections ?? 0}`);
  setText(card.querySelector('.line-failure'), `Failure rate: ${backend.failure_rate}`);
  setWidth(card.querySelector('.backend-meter span'), Math.max(0, Number(backend.load_percent || 0)));
  if (!card.dataset.bound) {
    card.dataset.bound = '1';
    card.addEventListener('click', () => openBackendModal(backend.id));
  }
}

function syncBackendGrid() {
  keyedSync(backendGrid, state.backends, (b) => b.id, backendCard, updateBackendCard);
}

// --- summary strip -----------------------------------------------------------

function renderSummary() {
  setText(strategyValue, state.strategy || '-');
  setText(clientsValue, String(state.clients));
  setText(requestRateValue, `${state.requestRate} req/s`);
  setText(backendCountValue, String(state.backends.length));

  const requests = Number(state.loadBalancer.request_count || 0);
  const successful = Number(state.loadBalancer.successful_requests || 0);
  const failed = Number(state.loadBalancer.failed_requests || 0);
  const total = successful + failed;
  const successRate = total > 0 ? Math.round((successful / total) * 100) : 0;
  setText(lbRequestCountValue, String(requests));
  setText(lbSuccessRateValue, `${successRate}%`);
  setText(lbFailureCountValue, String(failed));
  setText(lbAverageLatencyValue, `${Number(state.loadBalancer.average_latency_ms || 0)}ms`);
  setText(lbHealthValue, `${Number(state.loadBalancer.healthy_backends || 0)} / ${Number(state.loadBalancer.backend_count || 0)}`);

  if (document.activeElement !== strategyPicker) {
    if (strategyPicker.value !== (state.strategy || 'round_robin')) {
      strategyPicker.value = state.strategy || 'round_robin';
    }
  }
  if (document.activeElement !== requestRateInput) {
    const rate = String(state.requestRate || 0);
    if (requestRateInput.value !== rate) {
      requestRateInput.value = rate;
    }
  }
}

// --- pool picker (rebuilt only when the pool set changes) ---------------------

let lastPoolOptions = '';

function renderPoolOptions() {
  const poolNames = [...new Set(['default', ...state.pools])].filter(Boolean);
  const signature = poolNames.join('|');
  if (signature === lastPoolOptions) return;
  lastPoolOptions = signature;

  const selected = backendPoolInput.value || 'default';
  backendPoolInput.innerHTML = poolNames.map((pool) => `<option value="${pool}">${pool}</option>`).join('');
  backendPoolInput.value = poolNames.includes(selected) ? selected : 'default';
}

function updateBackendView() {
  if (!trafficSceneRefs) buildTrafficScene();
  syncClientNodes();
  syncPoolGroups();
  syncBackendGrid();
  renderPoolOptions();
}

function render() {
  renderSummary();
  updateBackendView();
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
