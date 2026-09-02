

## Component Overview:
1. Virtual IP / DNS: stable entry point for client requests
2. Load Balancer Nodes: receive connections and route to backends
3. Session Store (in memory): stores sticky session mappings
4. Health Checker: monitors status of backend pool(s)/servers, updates LB nodes with status
5. Configuration Manager: manages and applies LB configurations for backend pools
6. Configuraiton Storer: persistent storage of LB configurations
7. Backend Pools: logical groups of application servers which process client requests and return responses

## API specifications:
- Data Plane: no api route, general client request traffic
- Control Plane:
    - POST /backend: add new server to a backend pool
        request params:
        - address (string, required): address of server
        - port (int, required): port server is listening on
        - pool (str, optional when single pool, required with multiple pools): identifier of pool to add server to, if no pools exist, create one
        - weight (int, optional default 1): weight of server for certain routing algos
        - max_connections (int, optional default = -1 meaning no limit)
        - health_check_path (string, optional default '/health'): path of heath check endpoint

        responses:
        - 201 Created:
            - backend_id (string): identifier of backend server
            - pool_id (string): identifier of pool backend server is in
            - message: explain if a new pool was created
        - 4xx Pool not found
            - message: explain that no pool of created ID is found
    - DELETE /backend/{backend_id}: remove backend from its pool
        responses:
        - 200 OK:
            - backend_id (string)
            - pool_id (string)
            - drained_conenctions (int): how many active connections server had before shutdown
        - 4xx Backend not found
    - GET /backend/{backend_id}/health: check health status of a backend
    - POST /pool: create new backend pool
    - DELETE /pool/{pool_id}: delete backend pool
    - PUT /config/algorithm
        request params:
        - algorithm (enum): one of [round_robin, weighted_round_robin, least_connections, ip_hash, random]
        - sticky_session (bool): enable sticky sessions for the algorithm
        - sticky_ssl_seconds (int, required if sticky_sessions is true): how long sticky sessions last (TTL)


## Request Flow Outline:
1. Connection arrives to listener
2. Listener accepts connection, inspects any HTTP/S information relavent to routing
3. Listener asks LB router for a backend, checks pools, filters unhealthy servers, applies relavent algo, returns server
4. Listener forwards request to the selected server
5. Server processes request, returns it to listener
6. Listener returns request to the client

## Load Balancer Node Availability
- support for active-passive and active-active

## Data Storage:
- In LB Node Memory:
    - active connections
    - backend health status
    - connection counters
- Shared Cache:
    - Session mappings for sticky sessions when applicable
- Persistent Storage
    - Backend configuration addresses and weights
    - Pool configuration algorithms and settigns

### Data Storage Schemas
- Backend Servers
    - backend_id (string): unique ID
    - pool_id (string): UID of pool server belongs to
    - address (string): IP Address or hostname
    - port (int): server listening port
    - weight (int): server weight for weighted algorithms
    - max_connections (int): configured maximum connections for this server, -1 means no limit
    - enabled (bool): flag to toggle if backend server is active
- Backend Pools:
    - pool_id (string): unique ID
    - name (string): human readable label for the pool
    - algorithm (enum): type of routing algorithm for this pool
    - health_check_path, health_check_interval (string, int): path and interval for health checks
    - healthy_threshold (int): how many sucessful health checks to mark an unhealthy node as healthy
    - unhealthy_threshold (int): how many unsuccessful health checks to mark a healthy node as unhealthy
    - sticky_sessions (boolean)
    - sticky_ttl (int): TTL in seconds for sticky sessions

