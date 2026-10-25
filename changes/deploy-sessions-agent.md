### Added

- Deploy evaluated AgentSessions packages by digest to a named destination as immutable Agent resources. Exact acknowledged specs are GET-only no-ops; `--name` selects a new deployment for changed inputs. New creates wait for current registration, and destination status reports it without host calls. Registered is last controller-observed admission, not live availability or target evaluation. The offline `--harness-image --plan` render remains available; updates are unsupported.
