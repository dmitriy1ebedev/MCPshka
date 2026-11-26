# MCPshka 0.1.159

Integration import fix on top of 0.1.158. With the worker able to read the
source repository, the next live run copied 2989 of 4568 objects and stopped.

## Fix

- **The protected import finishes on slow storage and resumes
  (BUG-20261021-14).** Each object costs a file and a directory fsync (about 25
  objects/s on the H96 eMMC), and the import had a fixed 120 s budget inside a
  180 s unit, so a full history never fit. A rerun rewrote every object and hit
  the same wall. Now an already published object of the same kind and size is
  verified and skipped; the batch stream is still hashed against each oid, so a
  skipped object cannot hide a corrupted batch. Bounds are nested: import
  600 s < worker 660 s < process 720 s < unit `RuntimeMaxSec=780s`.
