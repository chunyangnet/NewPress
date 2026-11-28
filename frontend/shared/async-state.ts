/** A deadline is distinct from cancellation: only the former deserves an error. */
export function requestDeadline(controller: AbortController, milliseconds = 15000) {
  let timedOut = false;
  const timer = setTimeout(() => { timedOut = true; controller.abort(); }, milliseconds);
  return { get timedOut() { return timedOut; }, dispose() { clearTimeout(timer); } };
}
