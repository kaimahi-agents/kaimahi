### Fixed

- Save GitHub MCP file-resource recordings without losing their nested result. Recorded arguments and results each allow 32 payload-relative nesting levels; other Examples structure stays at 12, with unchanged shared node and byte limits. Older kmx versions may refuse these deeper recordings; update the reader rather than truncate the data.
