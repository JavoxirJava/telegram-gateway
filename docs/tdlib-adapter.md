# Governed typed TDLib adapter

`internal/tdadapter.Adapter` implements `telegram.Session` and the single-account
`telegram.Sessions` lookup. It consumes the existing governed `tdlib.Session.Read`
interface, not a raw native transport. No new HTTP/authentication/MCP endpoint is
added by this checkpoint. The operator agent is still live-only; the global
JetStream consumer must not be started with this single-account registry.

## Capabilities

- Profile, update-driven main/archive chat lists, paginated chat history,
  contacts, basic-group/supergroup members and bounded media downloads.
- A required `Authorize(context.Context)` callback before operations and after
  RPC responses. Production wiring must check verified identity, current owner
  lease and connection state; a callback that always permits is test-only.
- `Index.Observe` consumes each native update in receive order before the live
  persistence callback. It holds minimal chat/user metadata, not message bodies,
  phone numbers, local paths or remote file credentials. An index can be bound
  to only one adapter. Observe does not call TDLib from inside its callback.
- Account/operation/chat/session-generation-bound cursors. Recreating an adapter
  invalidates old chat/member cursors, requiring a fresh scan. They are internal
  progress positions, not signed authorization grants or immutable snapshots.
- Normalization of text/captions and supported photo, video, document, animation,
  audio, voice/video-note and sticker metadata. Unsupported service content is
  skipped explicitly. Secret/protected/expiring content is not archived. Raw
  forward/reply previews and file credentials are not copied into public models.

## History progress contract

`GetChatHistory` now returns `telegram.HistoryPage`, not a bare message slice.
`Items` contains eligible messages, `SourceCount` describes the native page and
`NextBeforeMessageID` advances using source IDs even when every item is excluded.
`Exhausted` is true only for an explicit, valid empty native `messages` array.
A missing/null collection, malformed item, cross-chat response or inconsistent
cursor is an error, never a successful empty snapshot.

TDLib may return fewer messages than requested. The adapter removes inclusive
anchors/duplicate IDs and bounds each page to 100 native messages. A nonempty
anchor-only page returns a typed five-second `Pending` retry instead of spinning
or falsely declaring completion. The queue's retry-exhaustion/reconciliation
policy must surface this condition; it cannot claim a complete archive on that
basis. Real-TDLib boundary behavior is still an end-to-end release test.

The history worker validates this contract before writing, queues continuation
from the source cursor and records both eligible/source counts. Existing sticky
soft deletion, tombstones and newer-live-edit protection remain unchanged. A
fully excluded page therefore does not stop subsequent history synchronization.

## File boundary

This Linux adapter opens one private `os.Root` and accepts only regular files
inside it, with a consistent native/local length and a configured maximum of at
most 4 GiB. The native download also receives a byte limit, including unknown-size
transfers. Nonblocking open prevents a substituted FIFO from hanging the worker;
root-relative opening rejects traversal and symlink escape. The session directory
must remain owner-only and not writable by untrusted processes.

Downloads are streamed in chunks of at most 64 KiB, not buffered in memory, and
check the caller context and current authorization between reads. Incomplete
native downloads return Pending; already delivered bytes cannot be recalled.
Resolve file IDs from an authorized active media row, never untrusted public
input. IDs require re-resolution if the TDLib database is recreated. MinIO upload
and public delivery remain separate; the deletion-aware proxy is described in
`media-access.md`.

## Integration contract / still gated

Create an Index for each new native session and deliver ordered updates to its
Observe method before the existing identity-gated live sink. Construct the
adapter using that same session, account and private files directory. Supply a
real ownership/identity callback and stop all calls/readers before Adapter.Close.
An adapter does not own/close the native session itself.

Before enabling historical workers, implement explicit account-owner routing,
fence **all** history/media writes, and remove the duplicate generic-worker
charge in favor of the already governed native RPC boundary. Do not disable the
native policy to avoid double charging. A single typed operation may issue more
than one governed native call. Metadata caches avoid member/contact lookup
fan-out when preceding TDLib updates are available.

Durable update replay, reconnect reconciliation, live attachment scheduling,
metadata/protection changes, media-ID recreation, retry/dead-letter visibility,
public MCP/OAuth and production storage/security setup are separate release
gates. This package and its tests do not resolve those gates or establish that a
real Telegram account has been tested.

## Verification

Tests cover short/filtered/overlapping history pages, malformed collections,
content exclusions, native update ordering through the real Go TDLib session
with a fake transport, cached contacts, member pagination, isolation and cursor
recreation, file caps/paths/FIFOs/truncation, permission loss and cancellation.
The normal CI and TDLib-validation workflows compile and run this package; the
latter also runs the existing Redis/PostgreSQL and native C ABI fixture tests.
No live Telegram account or real native library is required by these tests.

## Primary references

- https://core.telegram.org/tdlib/getting-started
- https://core.telegram.org/tdlib/docs/classtd_1_1td__api_1_1get_chat_history.html
- https://core.telegram.org/tdlib/docs/classtd_1_1td__api_1_1load_chats.html
- https://core.telegram.org/tdlib/docs/classtd_1_1td__api_1_1get_supergroup_members.html
- https://core.telegram.org/tdlib/docs/classtd_1_1td__api_1_1download_file.html
- https://pkg.go.dev/os#Root
