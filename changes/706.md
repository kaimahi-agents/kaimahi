### Changed

- Run full Orka PR qualification only for the explicit Orka/shared trigger list; generated caller-manifest-only changes skip that worker while hygiene still validates the manifest. Required hygiene and e2e aggregate checks stay unchanged. Main is re-verified nightly, not per merge; manual and release qualification remain full.
