# Pure Simulator Design Contract

## Purpose

The simulator is a presentation layer for the real load balancer. Its job is to model the environment around the LB, not the LB's internal routing logic.

The simulator exists to answer one question: "Given a scenario with clients, backend pools, health, latency, and traffic volume, how does the load balancer behave?"

It must not answer: "Which exact backend was chosen by the LB for this specific request?" or "What is the LB's internal routing state?"

## Contract

### 1. Boundary of responsibility

The simulator is responsible for:
- creating client traffic and request volume
- creating backend listeners and backend pools
- collecting backend-observed state from the backends themselves
- exposing scenario configuration such as the active strategy
- surfacing backend health, latency, failure behavior, and load metrics

The simulator is not responsible for:
- choosing backend destinations for a request
- tracking the LB's internal routing decisions
- exposing selected backend IDs, routing history, or request destination metadata
- modeling LB-only data structures or cached routing state

### 2. Allowed controls

The simulator may allow scenario changes that are external to the LB's routing engine, including:
- strategy selection for the LB demo configuration
- backend weight adjustments
- adding/removing backends
- adding/removing pools
- backend environment adjustments that are visible at the backend layer (health, latency, failure rate, pool membership, and labels)

### 3. Forbidden exposure

The simulator must not expose:
- per-request target selection
- routing history or route traces
- internal LB state, active selection, session mappings, or hot-path counters
- any API value that reveals the destination or choice of the LB on a given request

If a field names a backend target, route, selection, or internal LB decision, it is forbidden.

### 4. Observation model

The simulator must observe the world by polling the actual backends and reading the backend listeners' runtime state. It must not call into the LB to ask which backend was chosen or to inspect internal routing tables.

In practice:
- backend health is observed via backend responses and backend state
- active connections are read from the backend server runtime
- latency and failure behavior are read from backend configuration and backend responses
- strategy is a user-configurable scenario knob, not an internal LB decision trace

### 5. Isolation rule

The simulator is a diagnostic tool for isolating defects:
- if the environment data changes but the LB still behaves unexpectedly, the issue likely belongs to the LB
- if backend behavior or state reporting is wrong, the issue belongs to the simulator

This keeps the demo clean and ensures the object under test remains the real load balancer.
