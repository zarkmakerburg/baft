# Stage E current-main evidence E1

Measurement-only evidence collected locally on the authorized Mac runner.

- Benchmark branch parent production SHA: 5f1502d4f228584611db3af31d2a7725025b9ebc
- Harness commit before evidence archive: cd4d3c6d8377f177c7a8797ed8e0960631ed9d65
- No production files changed.
- B07/B06/B09/B08/B10/B11 were run sequentially to reduce cross-test interference.
- CPU/heap profile is a separate B06 run.
- Darwin does not expose /proc/self/fd, so FD fields are -1 and are not used as evidence here.
- B13 Linux netem and B15 current-main comparators are not part of this E1 batch.
