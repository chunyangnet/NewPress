# Security Policy

NewPress 1.0.0-beta.1 is a prerelease, not a stable production release. File storage is single-writer. Login sessions are persisted in private files or PostgreSQL; the complete application retains single-instance deployment boundaries. See [session persistence and recovery](SESSION_PERSISTENCE.md), including clearing sessions after a database restore. Review the documented deployment boundaries before testing with real data.

## Reporting a vulnerability

Contact **ChunYang** privately at **[Chunnel@linux.do](mailto:Chunnel@linux.do)**. Do not open a public issue for a suspected vulnerability. The canonical repository is [chunyangnet/NewPress on GitHub](https://github.com/chunyangnet/NewPress); the domestic mirror is [chunyangnet_0/newpress on Gitee](https://gitee.com/chunyangnet_0/newpress). If private vulnerability reporting is enabled on GitHub, its private report form is also an option; email remains available when that feature is not enabled.

Include the affected version/commit, deployment topology, reproduction steps, expected impact, privileges required, and relevant logs with secrets and personal data removed. Do not send production passwords, access tokens or private keys. No fixed response-time commitment is published yet.

## Scope

Security-sensitive components include package extraction, theme rendering, HTML sanitization, WASM runtime/Host API, authentication/authorization, media parsing, migrations, secrets, Docker configuration and update/rollback flows.

The project does not accept reports whose only claim is that an administrator can alter their own development files, or that WordPress PHP plugins cannot run; those are intended boundaries. Sandbox escapes, capability bypasses, XSS, SSRF, auth bypass, package traversal, signature bypass and data loss are in scope.

## Supported versions

No stable version is supported yet. The planned public prerelease is 1.0.0-beta.1; subsequent beta builds may require migration or deployment changes described in their release notes. Stable support branches and end-of-support dates will be published separately.

Security changes should include regression coverage for the affected authentication, authorization, input validation and data-isolation boundaries in the corresponding packages.

## Request and screenshot boundaries

Deep readiness (`/health/ready`) is restricted to reliably resolved loopback
probes. Public monitoring should use `/health/live`; container readiness uses
`newpress healthcheck --ready` inside the application container. External
reverse proxies must preserve the original client address and Host.

Public origins require HTTPS unless the configured hostname is localhost or a
literal loopback IP. Session cookies default to Secure outside those local HTTP
origins. TLS termination remains the reverse proxy's responsibility.

Resource download quotas and quiz attempts require an authenticated account.
Guest cookies are not identities suitable for enforcing strict quotas. Resource
writes also have a source-address budget independent of those cookies, and full
rate-limit tables reject new keys rather than evicting active budgets.

Media-library objects are public assets. Gating a link in an article does not
make the underlying file private; confidential deliveries require a separately
access-controlled storage/download endpoint.

Credential-free admin previews require a reliably resolved loopback client and
a loopback Host or the configured public Host. Forwarded host headers do not
grant access. Configure trusted proxy networks narrowly and preserve the
original Host at the reverse proxy.

Browser writes reject duplicate or malformed Origin/Fetch Metadata headers and
check Referer when Origin is absent. Public unauthenticated writes require an
explicit same-origin source; authenticated writes additionally require their
session CSRF token. Admin login and logout accept only bounded UTF-8 form bodies,
never credentials or CSRF tokens in query parameters.

Session requests accept at most four session cookies, including legacy path
cookies, and reject larger sets before reading or mutating session storage.
Repeated tokens are processed once. Only canonical 256-bit session tokens reach
the storage backend; malformed or obsolete cookie values are ignored.

Human-verification proofs are reserved while the provider request is in flight.
Failed checks release their own reservation so invalid tickets cannot exhaust
the global replay cache. Successful proofs remain single-use across clients and
scenes, including when local receipt capacity is exhausted. Retried proofs must
pass a fresh provider check; verification errors never issue a receipt.

OAuth completion pages recheck the current account status and provider binding
before returning contact details. A pending browser proof cannot read those
details after the account is suspended or the provider identity is removed or
replaced; the browser must authorize again.

AI background work rechecks the owner's current account and plugin management
permission before each model request, including fallback, and before content or
media writes. Image generation does not carry media write permission across a
role change. Interactive metadata and paragraph tools recheck their existing
content permissions; authors and editors can use them without plugin management
permission.

Extension task workers check cancellation before claiming and again before
invoking an executor, including the reserved completion window at the end of a
lease. Shutdown after a committed claim leaves recoverable work rather than
acknowledging an unexecuted task. Executors still revalidate authority and use
durable idempotency keys at each side effect.

File-backed task authorization and administrative lists inspect state under the
backend lock without copying unrelated private payloads. Returned summaries own
their nested review values. External file edits are detected by streaming the
complete file through SHA-256, with size, identity and timestamp checks retained;
matching file size and modification time alone never authorize a cached claim.

Interactive image generation revalidates its original login session and content
and media permissions before contacting the provider and before saving each
returned image. Multipart media uploads recheck the session and upload permission
after reading the body; content imports recheck the session and import permissions
before applying the prepared batch. These checks stop subsequent writes after
logout, suspension, or a role change observed at those boundaries; they cannot
undo a provider request or a write that has already committed. Media upload
staging and the final library lock also honor cancellation before committing.

Navigation imports recheck the original login session and navigation management
permission after reading either preview or confirmation bodies, after loading the
preview snapshot, and immediately before replacement. A second session cookie
cannot take over a revoked import. Session-store failures stop the operation with
a temporary error and preserve the browser cookie. These are operation-boundary
checks; they do not roll back a replacement that has already begun committing.

Notification delivery refreshes subscription preferences and the active account's
verified email address after generating the body, before calling SMTP. Revocation
observed there cancels delivery; a lookup failure before SMTP retains the existing
retry/backoff policy. Preparation and revalidation share the persisted mail lease.
SMTP acceptance with an uncertain outcome is still never automatically retried.
Mail already handed to the SMTP sender cannot be recalled by later changes.

Notification file readiness and mutation checks stream all bytes through SHA-256
with a bounded buffer and verify the opened file and pathname identity, size and
modification time. Matching metadata alone never bypasses the digest. Cancellation
is checked between reads and before returning readiness. Pending mail with future
availability and unexpired leases do not force a full JSON state copy on idle
polls; due work and expired-lease recovery still use the durable mutation path.
Mutation candidates deep-copy preference and receipt maps, inbox and job event
slices, read/clear timestamps, and mail leases before any changes are applied.
After staging a mutation, the store rechecks the original file and cancellation
before replacing it. Detected changes preserve the external file and discard the
staged candidate. This narrows the conflict window but is not an atomic lock or
compare-and-swap against another process; the single-writer requirement remains.

Media uploads verify retained local object bytes against their expected SHA-256
before reusing an unindexed object or returning an active duplicate. Missing or
corrupt objects are not reported as successful uploads, and failed verification
does not replace the retained file or change the library index. These checks
run at upload/reuse boundaries; ordinary media reads do not rehash every file.

Remote file copy/move operations compare spooled bytes with the source's declared
size and SHA-256 when provided, then verify the uploaded destination before a move
deletes its source. Cancellation is checked after the final read as well as between
reads. File sessions synchronously observe revoked configuration/installation
contexts at operation boundaries, independently of asynchronous cancellation
callbacks. A failed or cancelled transfer retains its source; a destination already
uploaded before a later cancellation may remain and is not automatically deleted.

Administrative cloud file requests retain their original login session and
revalidate account status and plugin management permission at file-operation
boundaries: after request decoding, before uploading staged bytes, before
deleting a source, and before returning remote list/download results. A fallback
cookie cannot take over a revoked request. A session-store outage stops the
operation without exposing storage errors or expiring the browser cookie.
Downloads check cancellation before and after every read, including buffered
bytes returned together with EOF. Session revalidation is limited to every
1 MiB, reads after one second, and stream completion to avoid a lookup per chunk.
Revocation observed after output starts aborts the HTTP stream rather than
appending an error to a partial file. Already transmitted bytes and remote writes
that began before a recheck cannot be recalled; a later failed move retains both
its source and any destination already uploaded.

Circle attachment uploads retain the original session and refresh account status
and administrator privileges after reading multipart input and again after staging,
before submitting attachment metadata. The community store still enforces the
current circle membership, posting rules, and expected version. Authorization
failures before the metadata call use the existing reference-aware object cleanup;
ambiguous storage failures continue to retain the object for retry or recovery.
Attachment staging and local reads check cancellation before and after input reads
and after acquiring their object lock, avoiding continued input consumption or
returning private bytes once cancellation has been observed.

Circle attachment downloads recheck the original session, current membership,
topic/reply visibility, and attachment identity after local or remote reads,
including a local fallback from a failed remote read. Public published attachments
remain available to anonymous readers. Stream output repeats these checks at
1 MiB boundaries, on writes after one second, and before the final response chunk;
all ranges share the same budget. Cancellation is checked around every write.
Revocation after output begins aborts the HTTP response without appending an error
body. Already transmitted bytes cannot be recalled, and checks across separate
session and community stores are boundary checks, not a shared transaction.
Session-store outages return an unavailable response before output or abort an
in-progress stream without expiring cookies or revealing storage details.

Autosave status and signed draft previews refresh their original session after
storage reads. Previews repeat checks after rendering and during bounded output:
current author access, canonical/source versions, recovery identity, expiration,
and token expiry must still match. Discarded or replaced recovery points are not
returned after a failed check. Built-in stores provide a metadata-only projection
for these checks; repository adapters without it use full reads. The preview
signature and identity binding remain required. Autosave writes/discards refresh
authority after form decoding and before mutation, including after snapshot
preparation. Memory autosave operations observe cancellation after acquiring the
store lock. Session/content checks are not a shared transaction, and already sent
bytes or writes already submitted cannot be recalled.

Editorial forms refresh the original session and CSRF credentials after reading
the body. Content creation, updates, status changes and revision restores check
current account/object permissions again before mutation, including after field
preparation. Editor submission lookups and successful receipts also refresh access;
a committed retry still precedes time-sensitive validation and can return its
original version after later edits. Losing access before submission does not
consume its idempotency key. These checks do not roll back writes already started.
Editor pages, including validation/conflict pages, recheck access after rendering
and during bounded output. A recovery form is bound to the actual loaded owner,
source version and update time, so a later replacement cannot authorize stale
input. Canonical permission checks use an optional post-state projection to avoid
cloning content bodies, metadata and terms; adapters retain a full-read fallback.

Content lists and revision comparisons recheck the original session after
rendering. The administrative content API refreshes that session after decoding,
storage reads and receipt lookups, before starting writes, and after encoding
successful responses. Rechecks include the current API enablement and author
scope; conditional reads and version-precondition errors cannot return validators
after a denied check. Scoped list checks read the current page's object states in
one batch where supported. A narrowed author scope rejects an earlier unscoped
list, including its counts and empty pages. JSON output uses bounded private
response checks and aborts an interrupted stream. Session outages preserve the
cookie; a rejected new submission does not consume its idempotency key. API
replays still return the current resource representation. These checks do not
recall bytes already sent or roll back a storage write that has already begun.

Memory and file administrative pagination filter and sort indices under the
storage lock and copy nested values only for the selected page. Returned values
remain isolated from the stored state. Cancelled list reads discard their result
at the lock, scan, and output-copy boundaries.

Dashboard responses recheck the original session, loaded-section permissions,
and content author scope after rendering and during bounded output. Accounts
with only dashboard or content-writing permissions retain their existing access.
Quick drafts refresh the original session and CSRF after form decoding and before
and after each create attempt, including slug-conflict retries. A failed final
check suppresses a success redirect but does not undo an already committed draft.
Quick drafts have no idempotency receipt; after an interrupted response, check the
draft list before resubmitting.

Personal article and comment lists recheck the same live account after JSON
encoding and during bounded output. Subscribers retain access to their own
content. Rechecks cover current article ownership, comment ownership, moderation
state and version, and the visibility of any displayed parent article title/link.
Disabled comment or flash modules are observed at these boundaries. Withdrawn
parent articles use a placeholder instead of exposing private titles or slugs.
Built-in stores batch lightweight article headers and comment states without
copying bodies, metadata, or comment text; adapters use deduplicated full reads.
Session outages retain browser cookies. These checks span independent stores,
are not a transaction, and cannot recall response bytes already sent.

File-backed comment lists keep only bounded pagination positions while scanning,
then copy the selected page. Public ordering, moderation filters, owner-only
visibility and status-count scope remain unchanged. Comment snapshots count
directly without copying all records. List, snapshot, individual comment,
revision-list and readiness reads observe cancellation before locking, after
acquiring the lock and before returning; long scans check it periodically.
Cancelled reads discard partial values. These checks do not interrupt a mutex
wait, a running filesystem operation or the bounded-page sort in progress.

Administrative comment reads and mutations retain the original session and
refresh both administration and comment-moderation capabilities. Form and API
writes recheck authority, origin and CSRF after decoding and around storage
operations. The comments module switch is also checked during in-flight requests;
disabling it keeps the existing route-level policy for comment management.
Comment lists, editors, revisions and version-conflict responses recheck after
preparation and during bounded output, before publishing private headers. A second
cookie cannot replace a revoked session; a session-store outage returns a retryable
error without clearing the browser cookie. Local read-only preview remains available
when credentials have not been configured and the module is enabled.

Administrative comment lists and editors batch parent article headers by unique
article ID. Built-in stores omit bodies and nested metadata; repository adapters
without header projection read each unique article once. Link construction shares
one request-local permalink configuration. Missing parents use the existing ID
placeholder; a failed batch discards all partial parent metadata without per-row
retries. Authority and cancellation checks still run before private output. The
dashboard reads comment counts instead of loading an unused comment queue and its
parent articles. Its common administration view retains its separate count read.

Comment mutations observe cancellation after acquiring the write lock and before
persistence. Direct create, edit, restore, reply, moderation and delete operations
also check before locking. File serialization is followed by a final cancellation check
before the audit/file commit. Cancellation observed before that check leaves comments,
revisions, notification events and submission receipts unchanged. Once that check
passes, a successful file commit is published in memory even if cancellation arrives
during disk I/O. These boundaries cannot interrupt a mutex wait or a running encode
or filesystem operation. Authority checks and comment storage are separate operations;
later revocation suppresses output without undoing a committed mutation. Staff replies
have no idempotency receipt: check the thread after an interrupted response before
resubmitting. Public comment submission receipts retain their existing replay behavior.

AI comment-reply drafts refresh the original session and moderation rights after
request decoding and context reads. Before sending an article/comment snapshot to
the configured model, they recheck comment and article versions, current article
visibility/ownership, and the AI plugin/module lease. Version changes or deletions
discard the prepared context or result. Private context requires permission for
both the original article snapshot and its current object. Built-in stores use
lightweight state/header reads for these repeated checks.

Draft generation checks the first model text, reasoning or usage event, then at
most once per second when such events arrive, and again at completion and before
response output. A denied check closes the upstream response and releases the
generation slot. This is not continuous polling while the model is silent; request
and plugin/module cancellation retain their existing deadlines. It cannot retract
context already sent to the configured provider. Draft generation does not create
or publish a comment. Session-store outages keep browser cookies and suppress the
draft instead of returning partially authorized content.

Interactive AI metadata and paragraph tools also retain their original session.
They refresh administration and content-write rights, origin/CSRF and the original
plugin/module lease after decoding and preparation, before each provider request
(including fallback), on the first model text/reasoning/usage event and at most
once per second on later events, at completion and before JSON output. Denial
closes the upstream response, prevents further fallback and releases the generation
slot. Session-store outages return a retryable error without clearing cookies.
The separate content API switch does not gate these plugin editor tools. Background
jobs retain their account-based authority checks and do not require a browser
session. These checks cannot retract input already sent to a provider, do not poll
while a model is silent, and cannot revoke response bytes already transmitted.

Article generation streams retain their original session and AI module lease.
They refresh administration/content-write rights and origin/CSRF after decoding,
team-template and category reads, before the model request, at its first event
and completion, and before the first payload or terminal SSE event is written.
Buffered terminal results receive the same output check. Disabling and restarting
the module does not revive an existing stream. Plain article generation does not
require the Pro Max plugin or the separate content API switch.

The producer and HTTP writer share a one-second cache for ordinary stream checks.
A periodic check also runs while the model is silent; scheduling and session-store
latency mean this is not a strict one-second revocation deadline. Writes check the
context in chunks of at most 64 KiB and force a fresh authority check after each
1 MiB of output since the preceding forced writer check. Denial cancels the
upstream request and suppresses pending private results. Before SSE begins it
returns an appropriate JSON error; afterwards it emits only a generic terminal
error without model output, usage or storage diagnostics. Mid-frame revocation
and failed writes abort the response instead of appending error JSON to an
incomplete frame. The generation slot remains held until the producer exits.
Session-store outages preserve browser cookies. Previously transmitted input or
output cannot be retracted, and authority checks are not atomic with network IO.

AI settings, connection lists, prompt catalogs, model discovery and document
preparation also retain the original browser session and AI module lease. They
refresh their own administration/capability requirements after request decoding,
blocking reads and computation, and before returning private JSON. Read-only
loopback access without configured credentials and settings-manager-only access
to model discovery retain their existing behavior. Content-write permissions do
not authorize settings changes; a temporary HTTP/private-endpoint approval also
requires current settings-management rights throughout discovery. Previously
approved saved endpoints remain usable by authorized content writers.

Settings saves check authority after obtaining the settings lock and after
normalization, before starting persistence. Rejection leaves the previous
revision, credentials and durable file intact. Persistence already in progress
is not rolled back by later revocation; final output is still checked. Model
discovery refreshes authority periodically while waiting for headers or body
data, and cancels its upstream context on denial or original-module shutdown.
Each model-list page also checks authority before its request, including native
pagination, image protocols and approved local endpoints; the check is scoped to
the individual connection copy rather than installed on a shared HTTP client.
Its polling goroutine stops before the handler finishes. The checks use the same
bounded private-JSON writer, preserve cookies on session-store outages, and do
not make revocation atomic with disk or network operations.

AI usage reports and pages retain the original session and analytics module
lease, independently of the AI generation switch. They recheck system-read
authority after loading data and before private JSON or HTML output. Balance
queries retain settings-management authority, request-origin/CSRF checks and
their original AI lease, including while awaiting a shared query or upstream
headers/body. Revoked requests cannot use another cookie to finish the response.
Before output, pages preserve login redirects and HTML errors; after output
starts, failed authority checks abort the response.

The access-analytics report and its recovery page retain the original session,
system-read capability and analytics lease too. Waiting data reads periodically
refresh authority; logout, capability removal, session-store failure or module
restart stops the request even without new data. Before JSON/HTML output, public
article references are rechecked against their collected versions. Withdrawn or
changed sources require a fresh report; source lookup failures do not become a
successful empty report or expose storage diagnostics. The AI module is not a
dependency of access analytics. Final output uses bounded private writers.

Analytics ranks candidate article IDs before loading metadata in batches of at
most 64, skipping missing/nonpublic entries until 20 public articles are found.
Stores without metadata batching retain the original one-ID read budget.
Approved comment counts for selected articles use one file-store scan or a
PostgreSQL grouped count query, with a deduplicated list-query fallback for
custom stores. Count reads exclude unapproved comments, check cancellation,
and return independent maps without comment bodies. These are live snapshots,
not an atomic transaction with subsequent source updates or response writes.

System-status pages and APIs retain the original session and system-read
capability while waiting for diagnostics, preparing the page and writing its
private output. Optional AI/analytics module switches do not gate these core
diagnostics. Revocation or a session-store outage stops a waiting caller on the
next periodic check; cancelled requests cannot read cached status data.

System diagnostics use one shared collection worker with independent cancellable
waiters and a five-second caller/worker context budget. The last waiter cancels
the worker; its slot remains occupied until native calls and probes actually
exit, preventing replacement storms when a dependency ignores cancellation.
Other waiters survive cancellation of the initiating request. Only the immutable
site snapshot is passed to the worker. The two-second cache is keyed by settings
revision and timezone, and returns copies of every nested slice and numeric
pointer. Cancelled/failed collections are not cached. Unexpected collector
failures release waiters and the slot; individual probe panics become redacted
unhealthy checks. Native OS calls cannot be forcibly interrupted, so a stuck call
can occupy the single worker while callers time out without receiving a report.
The system-status page reuses the collected content/media readiness results in
its administrative summary, avoiding synchronous duplicate probes outside that
budget. Other administrative summary readers retain their existing behavior.

The feature-status API revalidates its original session and the feature's
alternative capabilities after loading configuration and before private JSON
output. Article diagnostics also revalidate both the prepared and current
ownership/type scope; disappearance or loss of object access returns 404,
whereas a source storage failure returns a redacted 503. The existing five-second
context budget now includes the first object read. Dependency implementations
must honor cancellation for that budget to bound their wait. Diagnostics remain
available to authorized users while their module is paused. Comment status
reuses one loaded article's fields; related-topic diagnostics and repeated
authority checks use lightweight post state when supported. Legacy repositories
retain full-read fallback for authority checks.

Audit-event JSON uses the same private output barrier as administrative
downloads. API and export handlers revalidate the original session after audit
reads, including failed reads, before selecting their response. A result that
arrives after the audit read context expires is discarded. The tools page and
feature recovery pages revalidate after preparation, template rendering and at
bounded output boundaries, including recovery responses with 404/503 status.
Their checks retain the effective capabilities used for the loaded sections,
navigation and action hints; a surviving alternative page capability cannot
authorize an already prepared audit section after system-read is revoked.
Equivalent wildcard/explicit roles and unrelated account permissions do not
change diagnostic access. Paused modules still expose authorized recovery
pages, while an existing analytics recovery lease remains enforced. These
checks do not turn configuration reads or response writes into a transaction.

Recovery pages prepare only their visible navigation and status. They avoid
unrendered content/user/session/media totals and storage readiness probes. The
optional pending-comment badge is read only when its navigation entry is visible,
under a 500 ms context derived from the request; late results are discarded.
Plugin lists already read for an administrative summary or plugin catalog are
reused for that page's settings links, with the same manifest and trust filtering.
These display snapshots are refreshed on the next request and never replace
plugin execution authorization.

Public navigation reads inherit request cancellation and the earlier of the
request deadline and a 350 ms read budget. Rendered navigation discards late
snapshots and category aliases; JSON and stylesheet endpoints reject late reads
before emitting successful cache validators or a 304 response. Failed endpoint
reads use no-store. Filtered discovery pages reuse their already loaded category
list for navigation matching, including an empty result, within that request.
Navigation-only category lookups run only for assigned, visible local links that
need category aliases. These budgets require storage to honor context; they do
not interrupt synchronous filesystem operations or retract sent bytes.

Each balance caller waits with its own cancellable context. A cancelled initiator
returns promptly while other callers can continue the shared probe; the last
departing waiter cancels the worker and removes its pending cache entry. Workers
retain the two-request concurrency limit and ten-second timeout. Cancelled
callers cannot start a fresh cache lookup, and returned numeric values are
independent copies. Configuration is checked again after a pending probe, so a
removed/disabled provider or changed credentials cannot return its stale result.
These checks are not a transaction with configuration updates or network IO;
upstream requests already sent cannot be recalled.

AI workbench pages, catalogs, previews and private reports also retain their
original session and AI module lease. The workbench continues to require its
own system-read capability rather than article-write or settings-management
permissions. Request decoding, blocking context reads, report lookups and final
JSON/HTML output refresh that authority. Preview replacement and consumption,
report deletion and report persistence check again under their storage locks;
rejection preserves the previous token or report. Report writes check once more
after serialization, before persistence starts. These callbacks must not reenter
the same preview/report store. Already committed writes are not rolled back.

Workbench article choices and confirmed previews also retain the collected
article versions. Before output, generation and report persistence, the server
checks current public visibility and versions through batched article headers
(with a repository fallback). Withdrawn, missing or changed sources require a
new preview; confirmed content is never silently replaced. Previews containing
access analytics retain that module's original lease, including across a disable
and re-enable. Manual material and independent tasks keep their existing access.
Source checks run before the final session check, are shared and throttled during
streaming, and stop a silent upstream request on the next periodic check. Source
storage errors fail closed without exposing storage details. In-memory checks
are not serialized or retained in saved historical reports. Token consumption
also includes time spent awaiting locks and authority checks in its expiry test.
These checks are not atomic with source updates or network IO, and cannot recall
data already sent to a model or client.

Workbench generation uses the article generator's checked SSE frame writer with
the workbench's own authority predicate. The original module lease and periodic
checks cancel silent upstream waits; the producer rechecks before delivering
private results and before saving a completed report. Denial suppresses pending
completion data, and failed writes or mid-frame cancellation abort the response.
The shared generation slot remains occupied until the producer exits. Bounded
preview text extraction scans only the retained Unicode prefix; report context
and output limits are unchanged.

Writing template management refreshes its original session after request decoding,
after loading the library (including idempotent replays), and before submitting a
replacement to storage. A denied check does not publish template changes or consume
a request key. Template lists, histories, import previews, and exports refresh
authority after encoding and use the bounded private-response output checks.
These are boundary checks across separate stores; a storage write that has already
begun is not rolled back by later session revocation.

Sensitive read boundaries retain the session chosen on entry. Cookie fallback
remains supported when a request begins, but a second cookie (including another
session for the same account) cannot take over after revocation during that
request. Audit reads/exports, navigation exports, support reports, circle
exports, feedback CSV exports, and management media previews refresh authority
after reading their data. Prepared tool exports are rechecked after JSON encoding
and before attachment headers are set. Circle and feedback exports also recheck
current ownership or moderation settings using header projections, avoiding a
copy of every body and reply at each output boundary.
Management media previews and these file exports share the bounded output checks
used by circle attachments. Feedback export checks again before flushing buffered
CSV rows. The original public media path and unauthenticated local read-only
administration retain their existing policies. These checks stop newly observed
revocations; they do not recall earlier transmitted bytes or create a transaction
between independent permission and content stores.

The optional screenshot worker (protocol version 6) routes each browser context
through its own bounded proxy. HTTP and HTTPS CONNECT dial only the numeric IPs
approved by the request policy, including redirects and subresources. Public
requests cannot select local/reserved addresses. Local resources in explicit
local previews are confined to the selected origin and its initially approved addresses. Metadata
endpoints and IPv6 transition ranges are denied; WebSockets, direct WebRTC UDP,
and QUIC are disabled. Live previews expire after ten minutes. Chromium's sandbox
is explicitly enabled and TLS certificate verification remains enabled. Run the
worker as an unprivileged user in an environment that supports the browser sandbox.
Deploy the updated core and worker together;
the core refuses older workers. Keep the worker bound to loopback.

The screenshot worker receives local-network permission only for a local public
origin and a reliably resolved loopback client with an approved Host. Missing,
malformed or entirely trusted forwarded chains retain the public-only scope,
even when the immediate proxy peer is loopback.

Each screenshot proxy permits at most 32 in-flight operations, including pending
DNS resolution and dialing; HTTP pipelining cannot bypass this budget. HTTP
requests have a 30-second absolute lifetime and CONNECT tunnels a ten-minute
absolute lifetime, in addition to idle timeouts and byte limits. Disconnects
cancel pending dials and completed requests release their operation slots.

The shared public outbound client validates every request before transport:
HTTPS on port 443, no URL credentials or fragments, no Host override, and public
IP addresses only. IPv6 addresses must be in the allowed global-unicast space
and outside excluded special-purpose ranges. Responses have a 64 KiB header
budget. Administrative exceptions use a separate explicitly approved endpoint
client. The GitHub profile/avatar fetcher uses the public client too.

Regression checks include `node --test tests/frontend/device-mockup-network.test.cjs`
and `npm run test:security:browser` (requires the project's Playwright Chromium).
The browser integration test uses isolated local fixtures and does not contact
production services.
