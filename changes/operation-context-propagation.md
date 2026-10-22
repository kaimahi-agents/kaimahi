### Fixed

- Operational CLI adapters now pass the Cobra invocation context to an
  Runner copy on the invocation's App, so existing context-aware subprocesses,
  network requests and waits observe caller cancellation and deadlines. The
  App identity and recovery metadata are preserved, and the original shared
  Runner retains its context. Target detection
  uses the same binding; offline/help behavior, signal ownership and explicit
  child/cleanup context policies are unchanged.
