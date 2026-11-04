# Native port conflicts in worker 1.3.2

This source change extends Host revision `152139a621d5aea4579529bb5703d54b8c8ee0ff`. It does not install or reconnect a worker. The production controller last identified installed worker 1.3.0; that deployment remains unchanged by this work.

Previously, a deploy/restart stopped its tracked native process and then searched for listeners on the requested port and terminated them. A listener's port did not establish that the process belonged to this project. Worker 1.3.2 removes that fallback on Windows, Linux and macOS.

Before replacing a checkout, deployment validates the requested port and existing tracked-state record, invokes the existing tracked stop once, and refuses to continue if the tracked process remains alive. Native deployment then uses temporary, exclusive bind checks on IPv4 loopback, wildcards and the current local interface addresses. When IPv6 is detected it also checks IPv6 loopback and wildcard addresses; interface scopes are preserved. Interface lookup failure, uncertain bind errors and address disappearance refuse deployment. Only an unsupported IPv6 address family is optional. Probe errors do not print the interface inventory. Probe sockets are closed before checkout replacement.

Specific-address checks matter on Windows: a wildcard bind can succeed alongside an existing loopback listener. The regression suite includes a real listener owned by the test on an ephemeral loopback port, proving the check refuses it and leaves it listening. Other address-family, scope, error and deployment-transition cases use inert adapters.

The worker no longer invokes a port-based `netstat`/`taskkill`, `lsof` or `fuser` path. Normal tracked-process stop behavior remains, with stricter state validation on every stop path. Reserved PIDs (zero or one), the current agent PID, non-integers and values outside the positive signed 32-bit PID range are refused before signaling or deleting their state. Unreadable state or orphaned listeners now need review instead of a port-based termination fallback. The error directs the operator to stop an existing managed workload through Host or select a free port.

## Docker and conversion

Docker deployment keeps its existing build, exact named-container replacement and run sequence. It skips native bind checks, so the current managed container can continue occupying its published port while a replacement image builds. Native-to-Docker deployment still stops the tracked native workload first.

For Docker-to-native conversion, use the existing Host **Stop** operation and then deploy in native mode. If the container still occupies the requested port, native deployment refuses before replacing the checkout. This change does not infer ownership of an unlabeled container from its port or automatically remove one during native deployment.

## Preserved behavior and limits

Worker 1.3.1's removal confirmation and busy heartbeats are preserved; tracked-state validation is stricter. The static server, both installer payloads, server authentication, tenant selection, job protocol, provider integration, engine, BoxQL, custom data format and dependencies are unchanged.

This is not a PID-reuse fix, proof of tracked process ownership, OS tenant isolation or a resource-limit implementation. The original tracked-PID stop path and service privileges remain. Bind checks do not reserve the port until the workload starts: interfaces and listeners can change after a check, and address-binding semantics differ across operating systems. The main guarantee is removal of port-only process termination; a successful check never proves that an existing process is ours.

Source tests do not establish real Docker conversion, installed-worker acceptance, systemd restart safety or production readiness. Before rollout, the production controller must verify the selected source and worker version, current recoverable state, actual service/cgroup and workload ownership, prospective hello/fleet jobs and ports, and a controlled owned-workload acceptance test. If combining this with other source candidates, validate the combined revision rather than replacing it with an older worker payload.

## Validation

The new Node test reads the actual exported worker string and evaluates selected functions with inert filesystem, command, process and provider adapters. It never executes the worker imports, environment setup or `main()`. Its only real socket is an owned ephemeral loopback listener; no existing service port or process receives a signal.

The dedicated Ubuntu workflow requires the exact reviewed ancestor and five-file scope, checks protected worker/installer sections, runs the new Node tests and the existing removal/busy tests with Bun 1.3.14, and syntax-checks the complete served worker without executing it. The existing removal test receives only an inert current-PID fixture; reserved/invalid-PID tests never reach real signal, command or filesystem-removal operations. No dependencies, Docker, provider credentials or graphical CI jobs are needed.
