// Package patterna is the Go client for the Patterna API (contracts/openapi.yaml): the serve
// scope (Recommend, ReportInteractions) and the ingest scope (events, catalog, user consent and
// deletion).
//
// It is built for a customer backend sitting in front of a mobile app, where a slow
// recommendation must never slow a screen down:
//
//   - every call runs under a context deadline (150 ms for Recommend, 5 s otherwise), and
//     Recommend asks the engine to answer within 120 ms (X-Deadline-Ms);
//   - the client mints every request id (X-Request-Id), so a slate it gave up on is reported as
//     abandoned and Patterna does not count it as shown;
//   - a circuit breaker per scope opens after 5 consecutive failures and stays open for 30 s,
//     so a Patterna outage costs nothing but the fallback;
//   - RecommendWithFallback returns your own list whenever Patterna cannot answer in time;
//   - ReportInteractions is fire-and-forget through a bounded worker pool;
//   - idempotent calls are retried once on 5xx and network errors; Recommend never is, because
//     every Recommend logs an impression.
//
// Consent is enforced before anything leaves the process: a request needs a mode, and a
// non_profiled request must carry a session id and no user id. User ids must be the random
// per-tenant ids you mint; emails and identity-provider subjects are refused.
//
// All errors are *Error with a stable Code; compare with errors.Is(err, patterna.ErrTimeout).
//
// The admin subpackage and the cmd/patterna CLI manage flows, surfaces, experiments and the
// ledger with an admin key.
package patterna
