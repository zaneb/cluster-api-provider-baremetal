# Baremetal Machine Controller readiness

The process will be considered ready and healthy by kubernetes based solely on whether it can respond to an HTTP request. This is done via periodic checks on `livenessProbe` and `readinessProbe` in `machine-controllers` deployment.

The deployment is exposing port `9442` for each machine controller, and expecting response from `/healthz` and `/readyz` endpoints. The overall setup looks like that:

```yaml
livenessProbe:
  failureThreshold: 3
  httpGet:
    path: /readyz
    port: healthz
    scheme: HTTP
  periodSeconds: 10
  successThreshold: 1
  timeoutSeconds: 1
ports:
- containerPort: 9442
  name: healthz
  protocol: TCP
readinessProbe:
  failureThreshold: 3
  httpGet:
    path: /healthz
    port: healthz
    scheme: HTTP
  periodSeconds: 10
  successThreshold: 1
  timeoutSeconds: 1
```
